package deployment

import (
	"encoding/json"
	"errors"
	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/store"
	"net/http"
)

func (s *Service) Register(api *httpapi.Server) {
	api.Register("GET /api/v1/deployments/targets", "viewer", func(w http.ResponseWriter, r *http.Request) {
		items := []map[string]any{}
		for _, t := range s.Config.Targets {
			if s.authorize(r.Context(), httpapi.CurrentUser(r), t, "view") == nil {
				items = append(items, map[string]any{"id": t.ID, "name": t.Name, "kind": t.Kind, "application_id": t.ApplicationID, "environment_id": t.EnvironmentID, "config_digest": hash(t)})
			}
		}
		httpapi.WriteJSON(w, 200, map[string]any{"items": items})
	})
	api.Register("POST /api/v1/deployments/plans", "operator", func(w http.ResponseWriter, r *http.Request) {
		var in Request
		if !httpapi.Decode(w, r, &in) {
			return
		}
		p, e := s.Prepare(r.Context(), httpapi.CurrentUser(r), in)
		respond(w, p, e)
	})
	api.Register("GET /api/v1/deployments/plans/{id}", "viewer", func(w http.ResponseWriter, r *http.Request) {
		p, e := s.Plan(r.Context(), httpapi.CurrentUser(r), r.PathValue("id"))
		respond(w, p, e)
	})
	api.Register("POST /api/v1/deployments/plans/{id}/approve", "operator", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Digest string `json:"digest"`
		}
		if !httpapi.Decode(w, r, &in) {
			return
		}
		p, e := s.Approve(r.Context(), httpapi.CurrentUser(r), r.PathValue("id"), in.Digest)
		respond(w, p, e)
	})
	api.Register("POST /api/v1/deployments/plans/{id}/execute", "operator", func(w http.ResponseWriter, r *http.Request) {
		x, e := s.Execute(r.Context(), httpapi.CurrentUser(r), r.PathValue("id"))
		respond(w, x, e)
	})
	api.Register("GET /api/v1/deployments/executions", "viewer", func(w http.ResponseWriter, r *http.Request) {
		scope, e := httpapi.ParseReadScope(r.URL.Query())
		if e != nil {
			respond(w, nil, e)
			return
		}
		ids, e := api.VisibleApplicationIDs(r.Context(), httpapi.CurrentUser(r))
		if e != nil {
			respond(w, nil, e)
			return
		}
		rows, e := s.DB.Query(r.Context(), "SELECT x.id FROM deployment_executions x JOIN deployment_plans p ON p.id=x.plan_id WHERE p.application_id=ANY($1::text[]) AND (cardinality($2::text[])=0 OR p.application_id=ANY($2::text[])) AND (cardinality($3::text[])=0 OR p.environment_id=ANY($3::text[])) ORDER BY x.created_at DESC LIMIT 100", ids, scope.ApplicationIDs, scope.EnvironmentIDs)
		if e != nil {
			respond(w, nil, e)
			return
		}
		keys := []string{}
		for rows.Next() {
			var id string
			if e = rows.Scan(&id); e != nil {
				break
			}
			keys = append(keys, id)
		}
		rows.Close()
		if e == nil {
			e = rows.Err()
		}
		if e != nil {
			respond(w, nil, e)
			return
		}
		items := []Execution{}
		for _, id := range keys {
			x, e := s.Execution(r.Context(), httpapi.CurrentUser(r), id)
			if e != nil {
				respond(w, nil, e)
				return
			}
			items = append(items, x)
		}
		respond(w, map[string]any{"items": items}, nil)
	})
	api.Register("GET /api/v1/deployments/executions/{id}", "viewer", func(w http.ResponseWriter, r *http.Request) {
		x, e := s.Execution(r.Context(), httpapi.CurrentUser(r), r.PathValue("id"))
		respond(w, x, e)
	})
	api.Register("POST /api/v1/deployments/executions/{id}/cancel", "operator", func(w http.ResponseWriter, r *http.Request) {
		e := s.Cancel(r.Context(), httpapi.CurrentUser(r), r.PathValue("id"))
		respond(w, map[string]bool{"requested": e == nil}, e)
	})
	api.Register("POST /api/v1/deployments/executions/{id}/reconcile", "operator", func(w http.ResponseWriter, r *http.Request) {
		x, e := s.Reconcile(r.Context(), httpapi.CurrentUser(r), r.PathValue("id"))
		respond(w, x, e)
	})
	api.Register("POST /api/v1/deployments/executions/{id}/restore-plan", "operator", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			IntentID string `json:"intent_id"`
		}
		if !httpapi.Decode(w, r, &in) {
			return
		}
		if in.IntentID == "" {
			respond(w, nil, ErrInvalid)
			return
		}
		x, e := s.Execution(r.Context(), httpapi.CurrentUser(r), r.PathValue("id"))
		if e != nil {
			respond(w, nil, e)
			return
		}
		p, e := s.loadPlan(r.Context(), x.PlanID)
		if e != nil {
			respond(w, nil, e)
			return
		}
		if x.Status != "succeeded" {
			respond(w, nil, store.ErrConflict)
			return
		}
		next, e := s.Prepare(r.Context(), httpapi.CurrentUser(r), Request{IntentID: "restore:" + x.ID + ":" + in.IntentID, RunID: p.Source.RunID, NodeID: p.Source.NodeID, ArtifactDigest: p.Source.ArtifactDigest, TargetID: p.TargetID})
		respond(w, next, e)
	})
	api.Register("POST /api/v1/deployments/executions/{id}/evidence", "operator", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			InvestigationID string `json:"investigation_id"`
			BranchID        string `json:"branch_id"`
		}
		if !httpapi.Decode(w, r, &in) {
			return
		}
		u := httpapi.CurrentUser(r)
		x, e := s.Execution(r.Context(), u, r.PathValue("id"))
		if e != nil {
			respond(w, nil, e)
			return
		}
		p, e := s.loadPlan(r.Context(), x.PlanID)
		if e != nil {
			respond(w, nil, e)
			return
		}
		inv, e := s.Store.Investigation(r.Context(), in.InvestigationID)
		if e != nil {
			respond(w, nil, e)
			return
		}
		if inv.ApplicationID != p.Source.ApplicationID || inv.EnvironmentID != p.Source.EnvironmentID {
			respond(w, nil, store.ErrForbidden)
			return
		}
		if e = s.Store.AuthorizeApplication(r.Context(), u, inv.ApplicationID, "operate"); e != nil {
			respond(w, nil, e)
			return
		}
		raw, _ := json.Marshal(map[string]any{"plan": p, "execution": x})
		evidence, e := s.Store.PinEvidence(r.Context(), inv.ID, inv.EnvironmentID, in.BranchID, core.Evidence{Kind: "deployment", Title: "Deployment change: " + p.TargetID, Content: string(raw), Source: "deployment://" + x.ID})
		respond(w, evidence, e)
	})
}
func respond(w http.ResponseWriter, v any, e error) {
	if e != nil {
		switch {
		case errors.Is(e, store.ErrForbidden):
			httpapi.Error(w, 403, "forbidden", "Application operation permission required")
		case errors.Is(e, store.ErrNotFound):
			httpapi.Error(w, 404, "not_found", "Deployment record or registered target not found")
		case errors.Is(e, store.ErrConflict), errors.Is(e, ErrDrift):
			httpapi.Error(w, 409, "deployment_conflict", "Intent, approval or target changed; inspect before retry")
		case errors.Is(e, ErrInvalid):
			httpapi.Error(w, 422, "invalid_deployment", "A verified artifact and registered target are required")
		default:
			httpapi.Error(w, 503, "deployment_unavailable", "Provider could not be verified; no success is assumed")
		}
		return
	}
	httpapi.WriteJSON(w, 200, v)
}
