package actions

import (
	"context"
	"errors"
	"github.com/wans/aiops-canvas/internal/core"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/store"
)

// Register serves the legacy rollback flow. Only a legacy deployment calls
// it: a V3 instance registers none of these routes (PA-19 P4).
func (s *Service) Register(api *httpapi.Server) {
	s.Authorize = func(ctx context.Context, user, inv, action string) error {
		app, e := s.Repository.Application(ctx, inv)
		if e != nil {
			return e
		}
		return api.AuthorizeApplication(ctx, core.User{ID: user}, app, action)
	}
	s.AuthorizePlan = func(ctx context.Context, user string, p Plan, action string) error {
		checker, ok := api.Store.(interface {
			AuthorizeRecoveryTarget(context.Context, string, string, string, string, string, string, string, string) error
		})
		if !ok {
			return store.ErrForbidden
		}
		app, err := s.Repository.Application(ctx, p.InvestigationID)
		if err != nil {
			return err
		}
		if action == "execute" {
			action = "update"
		}
		t := p.Proposal
		return checker.AuthorizeRecoveryTarget(ctx, user, app, t.EnvironmentID, t.ConnectorID, t.Namespace, t.Deployment, t.UID, action)
	}
	s.registerReview(api)
	api.Register("GET /api/v1/investigations/{id}/action-plans", "viewer", func(w http.ResponseWriter, r *http.Request) {
		p, e := s.Plans(r.Context(), r.PathValue("id"))
		respond(w, map[string]any{"items": p}, e)
	})
	api.Register("POST /api/v1/investigations/{id}/action-plans", "operator", func(w http.ResponseWriter, r *http.Request) {
		var v struct {
			TargetRevision string `json:"target_revision"`
		}
		if !httpapi.Decode(w, r, &v) {
			return
		}
		if v.TargetRevision == "" {
			httpapi.Error(w, 422, "invalid_input", "target_revision is required")
			return
		}
		p, e := s.Create(r.Context(), r.PathValue("id"), v.TargetRevision, httpapi.CurrentUser(r).ID)
		respond(w, p, e)
	})
	api.Register("GET /api/v1/action-plans/{id}", "viewer", func(w http.ResponseWriter, r *http.Request) {
		p, e := s.Plan(r.Context(), r.PathValue("id"))
		respond(w, p, e)
	})
	api.Register("GET /api/v1/action-plans/{id}/execution", "viewer", func(w http.ResponseWriter, r *http.Request) {
		x, e := s.Repository.ExecutionForPlan(r.Context(), r.PathValue("id"))
		respond(w, x, e)
	})
	api.Register("POST /api/v1/action-plans/{id}/approve", "operator", func(w http.ResponseWriter, r *http.Request) {
		var v struct {
			Digest string `json:"digest"`
		}
		if !httpapi.Decode(w, r, &v) {
			return
		}
		p, e := s.Approve(r.Context(), r.PathValue("id"), v.Digest, httpapi.CurrentUser(r).ID)
		respond(w, p, e)
	})
	api.Register("POST /api/v1/action-plans/{id}/execute", "operator", func(w http.ResponseWriter, r *http.Request) {
		x, e := s.Enqueue(r.Context(), r.PathValue("id"), httpapi.CurrentUser(r).ID)
		respond(w, x, e)
	})
	api.Register("GET /api/v1/executions/{id}", "viewer", func(w http.ResponseWriter, r *http.Request) {
		x, e := s.Execution(r.Context(), r.PathValue("id"))
		respond(w, x, e)
	})
}
func respond(w http.ResponseWriter, v any, e error) {
	if e != nil {
		switch {
		case errors.Is(e, store.ErrForbidden):
			httpapi.Error(w, 403, "forbidden", "Application operation permission required")
		case errors.Is(e, ErrInvalid):
			httpapi.Error(w, 422, "invalid_input", "Required digest, reason, execution or summary is invalid.")
		case errors.Is(e, store.ErrConflict):
			httpapi.Error(w, 409, "conflict", "Exact plan approval is required; refresh and review the plan.")
		case errors.Is(e, pgx.ErrNoRows) || errors.Is(e, store.ErrNotFound):
			httpapi.Error(w, 404, "not_found", "Plan or investigation not found.")
		default:
			httpapi.Error(w, 503, "action_unavailable", "Action operation unavailable; inspect the registered connector or target revision.")
		}
		return
	}
	httpapi.WriteJSON(w, 200, v)
}
