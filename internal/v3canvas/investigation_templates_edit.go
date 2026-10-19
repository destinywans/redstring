package v3canvas

import (
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"net/http"
	"strings"

	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/rbac"
	"github.com/destinywans/redstring/internal/store"
	"github.com/destinywans/redstring/internal/v3interactionaudit"
)

func (s *Service) RegisterPageInteractions(api *httpapi.Server) {
	pattern := "PUT /api/v1/investigation-templates/{id}"
	v3interactionaudit.Mark(api, s.Store.DB, pattern, "investigation_template.update")
	api.Register(pattern, "viewer", s.editInvestigationTemplate)
	api.MarkV3ObjectRoute(pattern, "canvas", "change")
}

func (s *Service) editInvestigationTemplate(w http.ResponseWriter, r *http.Request) {
	if !s.ready(w) {
		return
	}
	envs, ok := allowedScope(w, r, true)
	if !ok {
		return
	}
	var in struct {
		ExpectedRevision int    `json:"expected_revision"`
		Name             string `json:"name"`
		Budget           struct {
			Tools   int `json:"tools"`
			Minutes int `json:"minutes"`
		} `json:"budget"`
	}
	if !httpapi.Decode(w, r, &in) {
		return
	}
	if in.ExpectedRevision < 1 || in.Name == "" || strings.TrimSpace(in.Name) != in.Name || len([]rune(in.Name)) > 160 || in.Budget.Tools < 0 || in.Budget.Tools > 16 || in.Budget.Minutes < 1 || in.Budget.Minutes > 5 {
		httpapi.Error(w, 422, "invalid_template", "Supply a revision, name and bounded budget")
		return
	}
	tx, err := s.Store.DB.Begin(r.Context())
	if err != nil {
		creationUnavailable().write(w)
		return
	}
	defer tx.Rollback(r.Context())
	cookie, err := r.Cookie("aiops_session")
	if err != nil {
		httpapi.Error(w, 401, "unauthorized", "Login required")
		return
	}
	user := httpapi.CurrentUser(r)
	if err = store.LockActiveSession(r.Context(), tx, cookie.Value, user.ID); err != nil {
		httpapi.Error(w, 401, "unauthorized", "Session expired")
		return
	}
	if err = lockCanvasGrantSources(r.Context(), tx, user.ID); err != nil {
		creationUnavailable().write(w)
		return
	}
	t, err := loadInvestigationTemplate(r.Context(), tx, r.PathValue("id"), envs[0], true)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) || errors.Is(err, pgx.ErrNoRows) {
			httpapi.Error(w, 404, "not_found", "Template not found")
		} else {
			creationUnavailable().write(w)
		}
		return
	}
	policy := &rbac.V3Policy{Store: s.Store}
	visible, err := s.visibleInvestigationTemplate(r.Context(), tx, policy, user, t)
	if err != nil {
		creationUnavailable().write(w)
		return
	}
	if !visible {
		httpapi.Error(w, 404, "not_found", "Template not found")
		return
	}
	var human bool
	if err = tx.QueryRow(r.Context(), `SELECT v3_identity_kind='human' FROM users WHERE id=$1`, user.ID).Scan(&human); err != nil {
		creationUnavailable().write(w)
		return
	}
	if !human {
		httpapi.Error(w, 403, "forbidden", "Human administrator required")
		return
	}
	d := rbac.V3Decision{Module: "canvas", Action: "change", EnvironmentID: envs[0], ApplicationID: t.ApplicationID}
	if t.Owner == "team" {
		d.ApplicationID = ""
		d.TeamID = t.TeamID
	}
	if err = policy.AuthorizeWithTx(r.Context(), tx, user, d); err != nil {
		if errors.Is(err, store.ErrForbidden) {
			httpapi.Error(w, 403, "forbidden", "Template management permission required")
		} else {
			creationUnavailable().write(w)
		}
		return
	}
	if t.Version != in.ExpectedRevision {
		httpapi.Error(w, 409, "template_revision_changed", "Reload the current template")
		return
	}
	t.Name = in.Name
	t.Budget.Tools = in.Budget.Tools
	t.Budget.Minutes = in.Budget.Minutes
	t.Version++
	if err = validateInvestigationTemplate(t); err != nil {
		httpapi.Error(w, 422, "invalid_template", "Budget cannot invalidate the retained template")
		return
	}
	budget, _ := json.Marshal(t.Budget)
	if _, err = tx.Exec(r.Context(), `UPDATE v3_investigation_templates SET name=$2,budget=$3,version=$4 WHERE id=$1`, t.ID, t.Name, budget, t.Version); err != nil {
		creationUnavailable().write(w)
		return
	}
	if _, err = tx.Exec(r.Context(), `INSERT INTO management_audit(actor_id,actor_kind,application_id,action,target,outcome,changes) VALUES($1,'user',NULLIF($2,''),'investigation_template.update',$3,'succeeded',jsonb_build_object('environment_id',$4::text,'revision',$5::int))`, user.ID, t.ApplicationID, t.ID, envs[0], t.Version); err != nil {
		creationUnavailable().write(w)
		return
	}
	if err = store.LockActiveSession(r.Context(), tx, cookie.Value, user.ID); err != nil {
		httpapi.Error(w, 401, "unauthorized", "Session expired")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		creationUnavailable().write(w)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpapi.WriteJSON(w, 200, map[string]any{"revision": t.Version, "template": t.publicItem()})
}
