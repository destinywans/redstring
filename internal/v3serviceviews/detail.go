package v3serviceviews

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
)

type link struct {
	Brand string `json:"brand"`
	Name  string `json:"name"`
	URL   string `json:"url"`
}
type aiNarrative struct {
	Text          string `json:"text"`
	Investigation string `json:"investigation"`
	Proposal      string `json:"proposal,omitempty"`
	Meta          string `json:"meta"`
}
type dependency struct {
	Brand  string `json:"brand,omitempty"`
	Name   string `json:"name"`
	Status string `json:"status"`
	Meta   string `json:"meta"`
	row    serviceRow
	seal   metricSourceSeal
}
type dependencySides struct {
	Upstream   []dependency `json:"upstream"`
	Downstream []dependency `json:"downstream"`
}
type detail struct {
	ID        string      `json:"id"`
	Name      string      `json:"name"`
	Tier      string      `json:"tier"`
	Team      string      `json:"team"`
	Env       string      `json:"env"`
	Status    string      `json:"status"`
	Desc      string      `json:"desc"`
	Deeplinks []link      `json:"deeplinks"`
	AI        aiNarrative `json:"ai"`
	Signals   []KPI       `json:"signals"`
	SLO       struct {
		Availability string  `json:"availability"`
		Target       string  `json:"target"`
		BudgetLeft   float64 `json:"budget_left"`
		Burn         []Burn  `json:"burn"`
		Note         string  `json:"note"`
	} `json:"slo"`
	Changes []change        `json:"changes"`
	Deps    dependencySides `json:"deps"`
	Tabs    map[string]int  `json:"tabs"`
}

func (s *Service) detail(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.scope(w, r)
	if !ok {
		return
	}
	if r.URL.RawQuery != "" {
		httpapi.Error(w, 422, "invalid_filter", "Service detail does not accept filters")
		return
	}
	app, env, valid := decodeID(r.PathValue("id"))
	if !valid {
		httpapi.Error(w, 404, "not_found", "Service not found")
		return
	}
	selected := false
	for _, v := range scope.EnvironmentIDs {
		selected = selected || v == env
	}
	if !selected {
		httpapi.Error(w, 404, "not_found", "Service not found")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 35*time.Second)
	defer cancel()
	user := httpapi.CurrentUser(r)
	rows, err := s.rows(ctx, user, scope.EnvironmentIDs, app, env, filters{})
	if err != nil {
		writeReadError(w, err)
		return
	}
	if len(rows) != 1 {
		httpapi.Error(w, 404, "not_found", "Service not found")
		return
	}
	row := rows[0]
	item, err := s.project(ctx, scope, user, row, nowOf(s))
	if err != nil {
		writeReadError(w, err)
		return
	}
	ai, err := s.narrative(ctx, user, row)
	if err != nil {
		writeReadError(w, err)
		return
	}
	changes, err := s.changes(ctx, row)
	if err != nil {
		writeReadError(w, err)
		return
	}
	deps, dependencyRows, err := s.dependencies(ctx, scope, user, row)
	if err != nil {
		writeReadError(w, err)
		return
	}
	incidentCount, err := s.incidentCount(ctx, row)
	if err != nil {
		writeReadError(w, err)
		return
	}
	var changeCount int
	err = s.Store.DB.QueryRow(ctx, `SELECT count(*)::int FROM deployment_events de
 JOIN deployment_executions x ON x.id=de.execution_id AND x.status='succeeded'
 JOIN deployment_plans p ON p.id=x.plan_id
 JOIN application_environments e ON e.id=p.environment_id AND e.application_id=p.application_id AND NOT e.archived
 WHERE p.application_id=$1 AND e.key=$2 AND de.type='succeeded'`, row.AppID, row.Env).Scan(&changeCount)
	if err != nil {
		writeReadError(w, err)
		return
	}
	if ctx.Err() != nil {
		writeReadError(w, ctx.Err())
		return
	}
	if err = s.finalCheck(ctx, r, rows, true, true); err != nil {
		writeReadError(w, err)
		return
	}
	if err = s.finalCheck(ctx, r, dependencyRows, false, true); err != nil {
		writeReadError(w, err)
		return
	}
	seal, err := s.sourceSeal(ctx, row)
	if err != nil || seal != item.seal {
		writeReadError(w, errIncomplete)
		return
	}
	for _, side := range [][]dependency{deps.Upstream, deps.Downstream} {
		for _, dep := range side {
			current, e := s.sourceSeal(ctx, dep.row)
			if e != nil || current != dep.seal {
				writeReadError(w, errIncomplete)
				return
			}
		}
	}
	out := detail{ID: item.ID, Name: item.Name, Tier: item.Tier, Team: item.Team, Env: item.Env, Status: item.Status, Desc: item.Desc,
		Deeplinks: []link{}, AI: ai, Signals: item.metrics.Signals, Changes: changes, Deps: deps,
		Tabs: map[string]int{"changes": changeCount, "deps": len(deps.Upstream) + len(deps.Downstream), "incidents": incidentCount}}
	if len(out.Changes) > 10 {
		out.Changes = out.Changes[:10]
	}
	out.SLO.Availability = item.metrics.Availability
	out.SLO.Target = item.metrics.Target
	out.SLO.BudgetLeft = item.metrics.BudgetLeft
	out.SLO.Burn = item.metrics.Burn
	out.SLO.Note = "Observed monthly availability and error-budget burn"
	w.Header().Set("Cache-Control", "no-store")
	httpapi.WriteJSON(w, 200, out)
}

// The AI panel has no honest empty-state field in the frozen DTO. It is shown
// only when a real, currently authorized investigation has a persisted model
// reply. The exact reply is labeled with its model and time, not summarized by
// this read path. The linked investigation belongs to this app/environment.
func (s *Service) narrative(ctx context.Context, user core.User, row serviceRow) (aiNarrative, error) {
	allowed, err := s.canView(ctx, user, row, "canvas")
	if err != nil {
		return aiNarrative{}, err
	}
	if !allowed {
		return aiNarrative{}, errIncomplete
	}
	var inv, content, model string
	var at time.Time
	now := nowOf(s)
	err = s.Store.DB.QueryRow(ctx, `SELECT i.id,m.value->>'content',m.value->>'model_id',
 NULLIF(m.value->>'created_at','')::timestamptz
 FROM investigations i
 JOIN application_environments e ON e.id=i.environment_id AND e.application_id=i.application_id AND NOT e.archived
 JOIN branches b ON b.investigation_id=i.id
 JOIN context_revisions cr ON cr.branch_id=b.id AND cr.revision=b.revision
 JOIN LATERAL (SELECT id,status,model_id,created_at FROM runs r
   WHERE r.branch_id=b.id ORDER BY r.created_at DESC,r.id DESC LIMIT 1) latest_run ON true
 JOIN LATERAL jsonb_array_elements(cr.messages) WITH ORDINALITY AS m(value,ord) ON true
 WHERE i.application_id=$1 AND e.key=$2 AND i.status='open'
 AND latest_run.status='completed'
 AND m.value->>'run_id'=latest_run.id
 AND NULLIF(m.value->>'created_at','')::timestamptz BETWEEN $3 AND $4
 AND latest_run.created_at<=NULLIF(m.value->>'created_at','')::timestamptz
 AND NULLIF(m.value->>'created_at','')::timestamptz<=cr.created_at
 AND latest_run.model_id=m.value->>'model_id'
 AND m.value->>'role'='assistant'
 AND length(btrim(COALESCE(m.value->>'content',''))) BETWEEN 1 AND 2000
 AND COALESCE(m.value->>'model_id','')<>''
 ORDER BY NULLIF(m.value->>'created_at','')::timestamptz DESC,m.ord DESC LIMIT 1`, row.AppID, row.Env, now.Add(-time.Hour), now).Scan(&inv, &content, &model, &at)
	if errors.Is(err, pgx.ErrNoRows) {
		return aiNarrative{}, errIncomplete
	}
	if err != nil {
		return aiNarrative{}, err
	}
	out := aiNarrative{Text: "Investigation note as of " + at.UTC().Format(time.RFC3339) + ": " + content,
		Investigation: inv, Meta: "Model " + model + " · " + at.UTC().Format(time.RFC3339)}
	// Proposal links expose no content, but still require a current proposal
	// grant and a pending proposal bound to the selected investigation.
	proposalAllowed, e := s.canView(ctx, user, row, "proposals")
	if e != nil {
		return aiNarrative{}, e
	}
	if proposalAllowed {
		e = s.Store.DB.QueryRow(ctx, `SELECT id FROM v3_proposals WHERE investigation_id=$1
 AND application_id=$2 AND environment_id=$3 AND status='pending'
 ORDER BY created_at DESC,id DESC LIMIT 1`, inv, row.AppID, row.Env).Scan(&out.Proposal)
		if e != nil && !errors.Is(e, pgx.ErrNoRows) {
			return aiNarrative{}, e
		}
	}
	return out, nil
}

func (s *Service) incidentCount(ctx context.Context, row serviceRow) (int, error) {
	var n int
	err := s.Store.DB.QueryRow(ctx, `SELECT count(*)::int FROM ops_incidents i
 JOIN application_environments e ON e.id=i.environment_id AND e.application_id=i.application_id AND NOT e.archived
 WHERE i.application_id=$1 AND e.key=$2 AND NOT i.suspected`, row.AppID, row.Env).Scan(&n)
	return n, err
}

func statusOnly(ctx context.Context, s *Service, scope httpapi.V3EnvironmentScope, user core.User, row serviceRow, now time.Time) (string, metricSourceSeal, error) {
	var zero metricSourceSeal
	target, ok := validRow(row)
	if !ok {
		return "", zero, errIncomplete
	}
	allowed, err := s.canView(ctx, user, row, "events")
	if err != nil {
		return "", zero, err
	}
	if !allowed {
		return "", zero, errIncomplete
	}
	before, err := s.sourceSeal(ctx, row)
	if err != nil {
		return "", zero, err
	}
	metricScope := scope
	metricScope.All = false
	metricScope.EnvironmentIDs = []string{row.Env}
	m, err := ReadMetrics(ctx, s.MetricReader, metricScope, user, row.Key, row.AppID, row.Env, target, now)
	if err != nil {
		return "", zero, errIncomplete
	}
	if m.BindingID != before.BindingID || m.ConnectorID != before.ConnectorID || m.ResourceID != before.ResourceID {
		return "", zero, errIncomplete
	}
	after, err := s.sourceSeal(ctx, row)
	if err != nil || after != before {
		return "", zero, errIncomplete
	}
	alerts, critical, err := s.alerts(ctx, row)
	if err != nil {
		return "", zero, err
	}
	if critical {
		return "danger", before, nil
	}
	if alerts > 0 && m.Status == "ok" {
		return "warn", before, nil
	}
	return m.Status, before, nil
}
