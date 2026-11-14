package v3canvas

import (
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"

	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/rbac"
	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5"
)

func (s *Service) createInvestigationTemplate(w http.ResponseWriter, r *http.Request) {
	if !s.ready(w) {
		return
	}
	envs, ok := allowedScope(w, r, true)
	if !ok {
		return
	}
	var in struct {
		ID            string `json:"id"`
		ApplicationID string `json:"application_id"`
		Name          string `json:"name"`
		Description   string `json:"description"`
		Branches      []struct {
			Name string `json:"name"`
			Step string `json:"step"`
		} `json:"branches"`
	}
	if !httpapi.Decode(w, r, &in) {
		return
	}
	if !strings.HasPrefix(in.ID, "team-") || len(in.ID) < 13 || in.ApplicationID == "" {
		httpapi.Error(w, 422, "invalid_template", "Supply a team template ID and application")
		return
	}
	ctx, user := r.Context(), httpapi.CurrentUser(r)
	cookie, err := r.Cookie("aiops_session")
	if err != nil || cookie.Value == "" {
		httpapi.Error(w, 401, "unauthorized", "Login required")
		return
	}
	tx, err := s.Store.DB.Begin(ctx)
	if err != nil {
		creationUnavailable().write(w)
		return
	}
	defer tx.Rollback(ctx)
	env := envs[0]
	service, failure := s.prepareCreation(ctx, tx, cookie.Value, user, env,
		createRequest{Kind: "investigation", Services: []string{in.ApplicationID}})
	if failure != nil {
		failure.write(w)
		return
	}
	var human bool
	err = tx.QueryRow(ctx, `SELECT v3_identity_kind='human' FROM users WHERE id=$1`, user.ID).Scan(&human)
	if err != nil {
		creationUnavailable().write(w)
		return
	}
	if !human || service.team == "" {
		httpapi.Error(w, 403, "forbidden", "An application owner team and a human author are required")
		return
	}
	template := investigationTemplateRecord{
		ID: in.ID, Owner: "team", EnvironmentID: env, ApplicationID: in.ApplicationID,
		TeamID: service.team, Name: strings.TrimSpace(in.Name), Description: strings.TrimSpace(in.Description),
		Version: 1, Maintainer: user.Name, Tools: []string{}, Runbooks: []string{},
		Triggers: investigationTemplateTriggers{Manual: true, Matchers: []investigationTemplateMatcher{}},
		Budget:   investigationTemplateBudget{Turns: 4, Tools: 0, Minutes: 3},
	}
	for _, branch := range in.Branches {
		template.Branches = append(template.Branches, investigationTemplateBranch{
			Name: strings.TrimSpace(branch.Name), Step: strings.TrimSpace(branch.Step), Tools: []string{},
		})
	}
	if err = validateInvestigationTemplate(template); err != nil {
		httpapi.Error(w, 422, "invalid_template", "Supply a name and one to eight distinct branches with instructions")
		return
	}
	policy := &rbac.V3Policy{Store: s.Store}
	visible, err := s.visibleInvestigationTemplate(ctx, tx, policy, user, template)
	if err != nil {
		creationUnavailable().write(w)
		return
	}
	if !visible {
		httpapi.Error(w, 403, "forbidden", "The application owner team and canvas view permission are required")
		return
	}
	branches, _ := json.Marshal(template.Branches)
	triggers, _ := json.Marshal(template.Triggers)
	budget, _ := json.Marshal(template.Budget)
	tag, err := tx.Exec(ctx, `INSERT INTO v3_investigation_templates
 (id,owner,environment_id,application_id,team_id,name,description,version,maintainer,branches,tools,runbooks,triggers,budget)
 VALUES($1,'team',$2,$3,$4,$5,$6,1,$7,$8::jsonb,'[]','[]',$9::jsonb,$10::jsonb)
 ON CONFLICT (id) DO NOTHING`, template.ID, env, template.ApplicationID, template.TeamID,
		template.Name, template.Description, template.Maintainer, branches, triggers, budget)
	if err != nil {
		creationUnavailable().write(w)
		return
	}
	status := http.StatusCreated
	if tag.RowsAffected() == 0 {
		existing, err := loadInvestigationTemplate(ctx, tx, template.ID, env, false)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			creationUnavailable().write(w)
			return
		}
		existing.Uses = 0
		if err != nil || !reflect.DeepEqual(existing, template) {
			httpapi.Error(w, 409, "template_id_conflict", "The template ID was already used")
			return
		}
		status = http.StatusOK
	} else if _, err = tx.Exec(ctx, `INSERT INTO management_audit(actor_id,actor_kind,application_id,action,target,outcome,changes)
 VALUES($1,'user',$2,'investigation_template.create',$3,'succeeded',jsonb_build_object('environment_id',$4::text,'revision',1))`,
		user.ID, template.ApplicationID, template.ID, env); err != nil {
		creationUnavailable().write(w)
		return
	}
	if err = store.LockActiveSession(ctx, tx, cookie.Value, user.ID); errors.Is(err, store.ErrSessionRevoked) {
		httpapi.Error(w, 401, "unauthorized", "Login required")
		return
	}
	if err != nil || tx.Commit(ctx) != nil {
		creationUnavailable().write(w)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	httpapi.WriteJSON(w, status, template.publicItem())
}
