package v3canvas

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/rbac"
	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5"
)

const maxInvestigationTemplateItems = 500

var investigationTemplateRef = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@-]*$`)

type investigationTemplateBranch struct {
	Name  string   `json:"name"`
	Step  string   `json:"step"`
	Tools []string `json:"tools"`
}

type investigationTemplateMatcher struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type investigationTemplateTriggers struct {
	Manual     bool                           `json:"manual"`
	AutoTriage bool                           `json:"auto_triage"`
	Matchers   []investigationTemplateMatcher `json:"matchers"`
}

type investigationTemplateBudget struct {
	Turns   int `json:"turns"`
	Tools   int `json:"tools"`
	Minutes int `json:"minutes"`
}

type investigationTemplateRecord struct {
	ID            string
	Owner         string
	EnvironmentID string
	ApplicationID string
	TeamID        string
	Name          string
	Description   string
	Version       int
	Maintainer    string
	Branches      []investigationTemplateBranch
	Tools         []string
	Runbooks      []string
	Triggers      investigationTemplateTriggers
	Budget        investigationTemplateBudget
	Uses          int64
}

// Validation is shared by list and start. A configured row that does not
// describe a bounded, manual, read-only outline must never be advertised.
func validateInvestigationTemplate(t investigationTemplateRecord) error {
	validText := func(s string, max int) bool { return s != "" && strings.TrimSpace(s) == s && len([]rune(s)) <= max }
	validRef := func(s string, max int) bool { return len(s) <= max && investigationTemplateRef.MatchString(s) }
	if !validRef(t.ID, 120) || (t.Owner != "official" && t.Owner != "team") ||
		!validRef(t.EnvironmentID, 120) || t.EnvironmentID == "all" ||
		(t.ApplicationID != "" && !validRef(t.ApplicationID, 120)) ||
		(t.TeamID != "" && !validRef(t.TeamID, 120)) ||
		(t.Owner == "team" && t.TeamID == "") ||
		!validText(t.Name, 160) || len([]rune(t.Description)) > 1000 ||
		len([]rune(t.Maintainer)) > 160 || t.Version < 1 || t.Uses < 0 {
		return errors.New("invalid investigation template metadata")
	}
	if !t.Triggers.Manual || t.Triggers.AutoTriage || t.Triggers.Matchers == nil || len(t.Triggers.Matchers) > 16 {
		return errors.New("unsupported investigation template trigger")
	}
	for _, m := range t.Triggers.Matchers {
		if !validText(m.Name, 80) || !validText(m.Value, 200) {
			return errors.New("invalid investigation template matcher")
		}
	}
	if t.Budget.Turns < 1 || t.Budget.Turns > 8 || t.Budget.Tools < 0 || t.Budget.Tools > 16 ||
		t.Budget.Minutes < 1 || t.Budget.Minutes > 5 {
		return errors.New("invalid investigation template budget")
	}
	if len(t.Branches) < 1 || len(t.Branches) > 8 || len(t.Tools) > 16 || len(t.Runbooks) > 16 {
		return errors.New("invalid investigation template size")
	}
	if t.ApplicationID == "" && (len(t.Tools) > 0 || len(t.Runbooks) > 0) {
		return errors.New("referenced resources require an application-bound template")
	}
	tools := make(map[string]bool, len(t.Tools))
	for _, tool := range t.Tools {
		if !validRef(tool, 200) || strings.Count(tool, ":") != 1 || tools[tool] {
			return errors.New("invalid investigation template tool")
		}
		parts := strings.SplitN(tool, ":", 2)
		if parts[0] == "" || parts[1] == "" {
			return errors.New("invalid investigation template tool")
		}
		tools[tool] = true
	}
	seen := map[string]bool{}
	for _, branch := range t.Branches {
		if !validText(branch.Name, 100) || !validText(branch.Step, 2000) || seen[branch.Name] || branch.Tools == nil || len(branch.Tools) > 16 {
			return errors.New("invalid investigation template branch")
		}
		seen[branch.Name] = true
		branchTools := map[string]bool{}
		for _, tool := range branch.Tools {
			if !tools[tool] || branchTools[tool] {
				return errors.New("invalid investigation template branch tool")
			}
			branchTools[tool] = true
		}
	}
	seen = map[string]bool{}
	for _, runbook := range t.Runbooks {
		if !validRef(runbook, 200) || seen[runbook] {
			return errors.New("invalid investigation template runbook")
		}
		seen[runbook] = true
	}
	return nil
}

func scanInvestigationTemplate(row pgx.Row) (investigationTemplateRecord, error) {
	var t investigationTemplateRecord
	var branches, tools, runbooks, triggers, budget []byte
	err := row.Scan(&t.ID, &t.Owner, &t.EnvironmentID, &t.ApplicationID, &t.TeamID,
		&t.Name, &t.Description, &t.Version, &t.Maintainer,
		&branches, &tools, &runbooks, &triggers, &budget, &t.Uses)
	if err != nil {
		return t, err
	}
	for _, item := range []struct {
		raw []byte
		out any
	}{
		{branches, &t.Branches}, {tools, &t.Tools}, {runbooks, &t.Runbooks},
		{triggers, &t.Triggers}, {budget, &t.Budget},
	} {
		if err = json.Unmarshal(item.raw, item.out); err != nil {
			return t, fmt.Errorf("invalid investigation template JSON: %w", err)
		}
	}
	if err = validateInvestigationTemplate(t); err != nil {
		return t, err
	}
	return t, nil
}

const investigationTemplateColumns = `id,owner,environment_id,COALESCE(application_id,''),COALESCE(team_id,''),
 name,description,version,maintainer,branches,tools,runbooks,triggers,budget,uses`

// FOR UPDATE serializes concurrent starts that increment the use count.
func loadInvestigationTemplate(ctx context.Context, tx pgx.Tx, id, env string, lock bool) (investigationTemplateRecord, error) {
	query := `SELECT ` + investigationTemplateColumns + ` FROM v3_investigation_templates
 WHERE id=$1 AND environment_id=$2 AND active`
	if lock {
		query += ` FOR UPDATE`
	}
	return scanInvestigationTemplate(tx.QueryRow(ctx, query, id, env))
}

type investigationTemplateItem struct {
	ID          string                        `json:"id"`
	Name        string                        `json:"name"`
	Owner       string                        `json:"owner"`
	Description string                        `json:"description"`
	Branches    []investigationTemplateBranch `json:"branches"`
	Tools       []string                      `json:"tools"`
	Uses        string                        `json:"uses"`
	Version     string                        `json:"version"`
	Maintainer  string                        `json:"maintainer"`
	Triggers    investigationTemplateTriggers `json:"triggers"`
	Runbooks    []string                      `json:"runbooks"`
	Budget      investigationTemplateBudget   `json:"budget"`
}

func (t investigationTemplateRecord) publicItem() investigationTemplateItem {
	return investigationTemplateItem{
		ID: t.ID, Name: t.Name, Owner: t.Owner, Description: t.Description,
		Branches: t.Branches, Tools: t.Tools, Uses: strconv.FormatInt(t.Uses, 10),
		Version: "v" + strconv.Itoa(t.Version), Maintainer: t.Maintainer,
		Triggers: t.Triggers, Runbooks: t.Runbooks, Budget: t.Budget,
	}
}

func (s *Service) listInvestigationTemplates(w http.ResponseWriter, r *http.Request) {
	if !s.ready(w) {
		return
	}
	envs, ok := allowedScope(w, r, false)
	if !ok {
		return
	}
	owner := r.URL.Query().Get("owner")
	if owner != "" && owner != "all" && owner != "official" && owner != "team" {
		httpapi.Error(w, 422, "invalid_owner", "Unknown template owner")
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if len([]rune(q)) > 200 {
		httpapi.Error(w, 422, "invalid_query", "Search is too long")
		return
	}
	ctx := r.Context()
	tx, err := s.Store.DB.Begin(ctx)
	if err != nil {
		httpapi.Error(w, 503, "investigation_templates_unavailable", "Investigation templates unavailable")
		return
	}
	defer tx.Rollback(ctx)
	if failure := s.canvasPrincipal(ctx, tx, r); failure != nil {
		failure.write(w)
		return
	}
	// Narrow candidates by current relational scope before the 500-row cap.
	// The policy and dependency checks below remain authoritative; this SQL
	// only prevents unrelated teams/apps from exhausting the bounded list.
	rows, err := tx.Query(ctx, `SELECT `+investigationTemplateColumns+` FROM v3_investigation_templates t
 WHERE t.active AND t.environment_id=ANY($1::text[])
 AND (t.owner='official' OR EXISTS (SELECT 1 FROM team_members m
   WHERE m.team_id=t.team_id AND m.user_id=$2))
 AND (
   (t.application_id IS NULL AND EXISTS (SELECT 1 FROM v3_effective_role_grants g
     WHERE g.user_id=$2 AND g.module='canvas' AND g.action='view'
     AND ((g.scope_kind='global' AND g.scope_id='*')
       OR (g.scope_kind='environment' AND g.scope_id=t.environment_id)
       OR (t.team_id IS NOT NULL AND g.scope_kind='team' AND g.scope_id=t.team_id))))
   OR (t.application_id IS NOT NULL AND EXISTS (
     SELECT 1 FROM applications a
     JOIN application_environments ae ON ae.application_id=a.id AND ae.key=t.environment_id AND NOT ae.archived
     JOIN v3_application_environments vae ON vae.application_id=a.id AND vae.environment_id=ae.key
     WHERE a.id=t.application_id AND NOT a.archived
       AND (t.owner<>'team' OR a.owner_team_id=t.team_id)
       AND EXISTS (SELECT 1 FROM v3_effective_role_grants g
         WHERE g.user_id=$2 AND g.module='canvas' AND g.action='view'
         AND ((g.scope_kind='global' AND g.scope_id='*')
           OR (g.scope_kind='environment' AND g.scope_id=t.environment_id)
           OR (g.scope_kind='application' AND g.scope_id=a.id)
           OR (a.owner_team_id IS NOT NULL AND g.scope_kind='team' AND g.scope_id=a.owner_team_id))))))
 ORDER BY t.name,t.id LIMIT $3 FOR SHARE OF t`, envs, httpapi.CurrentUser(r).ID, maxInvestigationTemplateItems+1)
	if err != nil {
		httpapi.Error(w, 503, "investigation_templates_unavailable", "Investigation templates unavailable")
		return
	}
	records := []investigationTemplateRecord{}
	for rows.Next() {
		t, scanErr := scanInvestigationTemplate(rows)
		if scanErr != nil {
			err = scanErr
			break
		}
		records = append(records, t)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil || len(records) > maxInvestigationTemplateItems {
		httpapi.Error(w, 503, "investigation_templates_unavailable", "Investigation templates unavailable")
		return
	}
	user := httpapi.CurrentUser(r)
	policy := &rbac.V3Policy{Store: s.Store}
	items := []investigationTemplateItem{}
	counts := map[string]int{"all": 0, "official": 0, "team": 0}
	needle := strings.ToLower(q)
	for _, t := range records {
		visible, checkErr := s.visibleInvestigationTemplate(ctx, tx, policy, user, t)
		if checkErr != nil {
			httpapi.Error(w, 503, "investigation_templates_unavailable", "Investigation templates unavailable")
			return
		}
		if !visible {
			continue
		}
		counts["all"]++
		counts[t.Owner]++
		if needle != "" && !strings.Contains(strings.ToLower(t.Name+" "+t.Description), needle) {
			continue
		}
		if owner == "" || owner == "all" || owner == t.Owner {
			items = append(items, t.publicItem())
		}
	}
	if failure := s.canvasSession(ctx, tx, r); failure != nil {
		failure.write(w)
		return
	}
	if err = tx.Commit(ctx); err != nil {
		httpapi.Error(w, 503, "investigation_templates_unavailable", "Investigation templates unavailable")
		return
	}
	httpapi.WriteJSON(w, 200, map[string]any{"items": items, "counts": counts})
}

func (s *Service) visibleInvestigationTemplate(ctx context.Context, tx pgx.Tx, policy *rbac.V3Policy, user core.User, t investigationTemplateRecord) (bool, error) {
	decision := rbac.V3Decision{Module: "canvas", Action: "view", EnvironmentID: t.EnvironmentID}
	if t.Owner == "team" {
		var member string
		err := tx.QueryRow(ctx, `SELECT user_id FROM team_members WHERE team_id=$1 AND user_id=$2 FOR SHARE`, t.TeamID, user.ID).Scan(&member)
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		decision.TeamID = t.TeamID
	}
	if t.ApplicationID != "" {
		var ownerTeam string
		err := tx.QueryRow(ctx, `SELECT COALESCE(a.owner_team_id,'') FROM applications a
 JOIN application_environments ae ON ae.application_id=a.id AND ae.key=$2 AND NOT ae.archived
 JOIN v3_application_environments vae ON vae.application_id=a.id AND vae.environment_id=ae.key
 WHERE a.id=$1 AND NOT a.archived FOR SHARE OF a,ae,vae`, t.ApplicationID, t.EnvironmentID).Scan(&ownerTeam)
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if t.Owner == "team" && t.TeamID != ownerTeam {
			// An application may move teams after the template was configured.
			// Hide the stale row without taking down unrelated catalog entries.
			return false, nil
		}
		decision.ApplicationID, decision.TeamID = t.ApplicationID, ""
		err = policy.AuthorizeWithTx(ctx, tx, user, decision)
		if errors.Is(err, store.ErrForbidden) && ownerTeam != "" {
			decision.ApplicationID, decision.TeamID = "", ownerTeam
			err = policy.AuthorizeWithTx(ctx, tx, user, decision)
		}
		if err != nil {
			return false, nonForbidden(err)
		}
		if len(t.Tools) > 0 || len(t.Runbooks) > 0 {
			if failure := s.checkTemplateDependencies(ctx, tx, user, t.EnvironmentID, t.ApplicationID, ownerTeam, t); failure != nil {
				if failure.status == 403 || failure.status == 409 {
					return false, nil
				}
				return false, errors.New("template dependency check unavailable")
			}
		}
		return true, nil
	}
	err := policy.AuthorizeWithTx(ctx, tx, user, decision)
	return !errors.Is(err, store.ErrForbidden) && err == nil, nonForbidden(err)
}

func nonForbidden(err error) error {
	if errors.Is(err, store.ErrForbidden) {
		return nil
	}
	return err
}
