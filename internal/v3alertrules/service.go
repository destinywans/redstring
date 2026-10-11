// Package v3alertrules exposes only alerting rules observed from a configured,
// environment-bound provider. It never treats an alert event as a rule definition.
package v3alertrules

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
	"github.com/wans/aiops-canvas/internal/v3rulemodel"
)

type Access interface {
	Authorize(context.Context, core.User, rbac.V3Decision) error
}

// Catalog returns persisted, active application/environment mappings. A rule's
// provider labels alone cannot establish an application's existence or access.
type Catalog interface {
	Applications(context.Context, []string) ([]Application, error)
}

type Application struct {
	ID, EnvironmentID, OwnerTeamID string
}

type DatabaseCatalog struct{ DB *pgxpool.Pool }

func (c DatabaseCatalog) Applications(ctx context.Context, environments []string) ([]Application, error) {
	if c.DB == nil {
		return nil, errors.New("application catalog unavailable")
	}
	rows, err := c.DB.Query(ctx, `SELECT a.id,ve.environment_id,COALESCE(a.owner_team_id,'') FROM applications a
 JOIN v3_application_environments ve ON ve.application_id=a.id
 JOIN application_environments e ON e.application_id=a.id AND e.key=ve.environment_id AND NOT e.archived
 WHERE NOT a.archived AND ve.environment_id=ANY($1::text[])
 ORDER BY ve.environment_id,a.id LIMIT 501`, environments)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	apps := []Application{}
	for rows.Next() {
		if len(apps) == 500 {
			return nil, errors.New("application catalog exceeds 500-row bound")
		}
		var app Application
		if err := rows.Scan(&app.ID, &app.EnvironmentID, &app.OwnerTeamID); err != nil {
			return nil, err
		}
		apps = append(apps, app)
	}
	return apps, rows.Err()
}

// Aliases preserve the public read API while keeping shared observation types
// below both the alert-rule and configuration-promotion packages.
type Source = v3rulemodel.Source
type ObservedRule = v3rulemodel.ObservedRule

type Summary struct {
	ID            string `json:"id"`
	Folder        string `json:"folder"`
	Name          string `json:"name"`
	State         string `json:"state"`
	EnvironmentID string `json:"environment_id"`
	ApplicationID string `json:"application_id"`
}

// Detail intentionally contains provider-backed fields only. The mock's
// preview, route, AI triage and threshold are unavailable from Prometheus.
type Detail struct {
	Summary
	Datasource         string  `json:"datasource"`
	Expr               string  `json:"expr"`
	ForSeconds         float64 `json:"for_seconds"`
	evaluationInterval time.Duration
	Labels             map[string]string `json:"labels"`
	Annotations        map[string]string `json:"annotations"`
	LastEvaluation     string            `json:"last_evaluation,omitempty"`
	EvaluationMS       float64           `json:"evaluation_ms"`
	Partial            bool              `json:"partial"`
	Unavailable        []string          `json:"unavailable"`
}

type Service struct {
	Catalog  Catalog
	Access   Access
	Source   Source
	Now      func() time.Time
	bindings map[string]string // environment + provider group identity -> application ID
	// D32: environment + application ID -> application label value, and
	// environment + label value -> application ID.
	appLabels, labelApps map[string]string
}

type RuleGroupBinding = v3rulemodel.RuleGroupBinding

func New(catalog Catalog, access Access, source Source, bindings []RuleGroupBinding) (*Service, error) {
	index := make(map[string]string, len(bindings))
	appLabels, labelApps := make(map[string]string), make(map[string]string)
	for _, b := range bindings {
		if b.EnvironmentID == "" || b.EnvironmentID == "all" || b.ApplicationID == "" || b.File == "" || b.Group == "" ||
			strings.ContainsRune(b.EnvironmentID, 0) || strings.ContainsRune(b.ApplicationID, 0) || strings.ContainsRune(b.File, 0) || strings.ContainsRune(b.Group, 0) {
			return nil, errors.New("invalid alert rule group binding")
		}
		key := bindingKey(b.EnvironmentID, b.File+"\x00"+b.Group)
		if _, exists := index[key]; exists {
			return nil, errors.New("duplicate alert rule group binding")
		}
		index[key] = b.ApplicationID
		label := b.ApplicationLabel
		if label == "" {
			label = b.ApplicationID
		}
		if len(label) > 128 || strings.ContainsAny(label, "\x00\r\n") {
			return nil, errors.New("invalid alert rule application label")
		}
		// One application has one label value in an environment, and one
		// label value names one application, so an alert maps back.
		appKey, labelKey := bindingKey(b.EnvironmentID, b.ApplicationID), bindingKey(b.EnvironmentID, label)
		if prior, ok := appLabels[appKey]; ok && prior != label {
			return nil, errors.New("an application has two alert rule application labels")
		}
		if prior, ok := labelApps[labelKey]; ok && prior != b.ApplicationID {
			return nil, errors.New("an alert rule application label names two applications")
		}
		appLabels[appKey], labelApps[labelKey] = label, b.ApplicationID
	}
	return &Service{Catalog: catalog, Access: access, Source: source, bindings: index, appLabels: appLabels, labelApps: labelApps}, nil
}

// ApplicationLabel is the "application" label value of app's rules in env
// (D32): the bound key, or the application ID when none is configured.
func (s *Service) ApplicationLabel(env, app string) string {
	if label, ok := s.appLabels[bindingKey(env, app)]; ok {
		return label
	}
	return app
}

// applicationOfLabel maps a rule's "application" label back to the bound
// application in env.
func (s *Service) applicationOfLabel(env, label string) (string, bool) {
	app, ok := s.labelApps[bindingKey(env, label)]
	return app, ok
}

func bindingKey(env, identity string) string {
	return env + "\x00" + identity
}

func (s *Service) Register(api *httpapi.Server) {
	api.Register("GET /api/v1/alert-rules", "viewer", s.MainList)
	api.MarkV3ObjectRoute("GET /api/v1/alert-rules", "alert_rules", "view")
	api.Register("GET /api/v1/alert-rules/{id}", "viewer", s.MainDetail)
	api.MarkV3ObjectRoute("GET /api/v1/alert-rules/{id}", "alert_rules", "view")
	api.Register("GET /api/v1/alert-rules/observed", "viewer", s.List)
	api.MarkV3ObjectRoute("GET /api/v1/alert-rules/observed", "alert_rules", "view")
	api.Register("GET /api/v1/alert-rules/observed/{id}", "viewer", s.Detail)
	api.MarkV3ObjectRoute("GET /api/v1/alert-rules/observed/{id}", "alert_rules", "view")
	api.Register("POST /api/v1/alert-rules/{id}/dry-run", "viewer", s.DryRun)
	api.MarkV3ObjectRoute("POST /api/v1/alert-rules/{id}/dry-run", "alert_rules", "view")
}

// MainList serves the complete V3 rule-list contract. Unlike the auxiliary
// observed route, it never publishes partial counts when one authorized
// environment's provider is unavailable. Prometheus rules have no cron
// schedule, so the scheduled count is truthfully zero.
// listState answers a list request whose visible scope is empty or whose
// source is not configured. Both are states, not failures: an empty visible
// scope is a known empty list (PA-09) and an absent source leaves the list
// unknown. It reports false for any other error.
func listState(w http.ResponseWriter, err error) bool {
	switch {
	case errors.Is(err, errNoVisibleApplication):
		httpapi.WriteJSON(w, http.StatusOK, map[string]any{"items": []any{}, "counts": map[string]int{"all": 0, "firing": 0, "scheduled": 0},
			"empty_reason": "no_authorized_target", "unavailable": map[string]string{}})
		return true
	case errors.Is(err, errRuleSourceNotConfigured):
		httpapi.WriteJSON(w, http.StatusOK, map[string]any{"items": nil, "counts": nil, "empty_reason": nil,
			"unavailable": map[string]string{"items": "not_configured", "counts": "not_configured"}})
		return true
	}
	return false
}

func (s *Service) MainList(w http.ResponseWriter, r *http.Request) {
	items, unavailable, status, err := s.visible(r)
	if listState(w, err) {
		return
	}
	if err != nil {
		httpapi.Error(w, status, errorCode(status), "Alert rule source or authorization is unavailable")
		return
	}
	if len(unavailable) != 0 {
		httpapi.Error(w, http.StatusServiceUnavailable, "alert_rules_unavailable", "An authorized alert rule source is unavailable")
		return
	}
	type mainSummary struct {
		ID     string `json:"id"`
		Folder string `json:"folder"`
		Name   string `json:"name"`
		State  string `json:"state"`
	}
	summaries := make([]mainSummary, 0, len(items))
	counts := map[string]int{"all": 0, "firing": 0, "scheduled": 0}
	seen := make(map[string]bool, len(items))
	for _, item := range items {
		if seen[item.ID] {
			httpapi.Error(w, http.StatusServiceUnavailable, "alert_rule_identity_conflict", "Observed rule identity is ambiguous")
			return
		}
		seen[item.ID] = true
		summaries = append(summaries, mainSummary{ID: item.ID, Folder: item.Folder, Name: item.Name, State: item.State})
		counts["all"]++
		if item.State == "firing" {
			counts["firing"]++
		}
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"items": summaries, "counts": counts, "empty_reason": nil, "unavailable": map[string]string{}})
}

var (
	errNoVisibleApplication    = errors.New("no application visible for alert rules")
	errRuleSourceNotConfigured = errors.New("alert rule provider or group bindings not configured")
)

func (s *Service) visible(r *http.Request) ([]Detail, []string, int, error) {
	scope, ok := httpapi.V3ScopeFromContext(r.Context())
	if !ok || len(scope.EnvironmentIDs) == 0 || s.Catalog == nil || s.Access == nil {
		return nil, nil, http.StatusServiceUnavailable, errors.New("environment scope, catalog or policy unavailable")
	}
	user := httpapi.CurrentUser(r)
	allowed, status, err := s.authorizedApplications(r.Context(), user, scope.EnvironmentIDs)
	if err != nil {
		// Nothing visible before any provider read: lists report an empty
		// scope; detail lookups keep the forbidden status.
		if status == http.StatusForbidden {
			return nil, nil, status, fmt.Errorf("%w: %w", errNoVisibleApplication, err)
		}
		return nil, nil, status, err
	}
	if s.Source == nil || len(s.bindings) == 0 {
		return nil, nil, http.StatusServiceUnavailable, errRuleSourceNotConfigured
	}
	items := []Detail{}
	unavailable := []string{}
	for _, env := range scope.EnvironmentIDs {
		if len(allowed[env]) == 0 {
			continue
		}
		rules, err := s.Source.List(r.Context(), env)
		if err != nil {
			unavailable = append(unavailable, env)
			continue
		}
		if len(rules) > 500 || len(items)+len(rules) > 500 {
			return nil, nil, http.StatusServiceUnavailable, errors.New("alert rule result exceeds 500-row bound")
		}
		for _, rule := range rules {
			app, bound := s.bindings[bindingKey(env, rule.GroupIdentity)]
			if _, authorized := allowed[env][app]; !bound || rule.Labels["environment"] != env || rule.Labels["application"] != s.ApplicationLabel(env, app) || !authorized || rule.Name == "" || rule.Group == "" {
				continue
			}
			items = append(items, toDetail(env, app, rule))
		}
	}
	// A provider read can wait long enough for a grant, catalog mapping or team
	// owner to change. Refuse to publish a list built from a different scope.
	current, status, err := s.authorizedApplications(r.Context(), user, scope.EnvironmentIDs)
	if err != nil {
		return nil, nil, status, err
	}
	if !reflect.DeepEqual(allowed, current) {
		return nil, nil, http.StatusServiceUnavailable, errors.New("alert rule scope changed while reading provider")
	}
	if len(unavailable) > 0 && len(unavailable) == len(allowed) {
		return nil, nil, http.StatusServiceUnavailable, errors.New("all authorized rule providers unavailable")
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items, unavailable, http.StatusOK, nil
}

func (s *Service) authorizedApplications(ctx context.Context, user core.User, environments []string) (map[string]map[string]Application, int, error) {
	apps, err := s.Catalog.Applications(ctx, environments)
	if err != nil {
		return nil, http.StatusServiceUnavailable, err
	}
	allowed := map[string]map[string]Application{}
	for _, app := range apps {
		if !contains(environments, app.EnvironmentID) || app.ID == "" {
			continue
		}
		decision := rbac.V3Decision{Module: "alert_rules", Action: "view", EnvironmentID: app.EnvironmentID, ApplicationID: app.ID}
		err := s.Access.Authorize(ctx, user, decision)
		if errors.Is(err, store.ErrForbidden) && app.OwnerTeamID != "" {
			decision.ApplicationID = ""
			decision.TeamID = app.OwnerTeamID
			err = s.Access.Authorize(ctx, user, decision)
		}
		if errors.Is(err, store.ErrForbidden) {
			continue
		}
		if err != nil {
			return nil, http.StatusServiceUnavailable, err
		}
		if allowed[app.EnvironmentID] == nil {
			allowed[app.EnvironmentID] = map[string]Application{}
		}
		allowed[app.EnvironmentID][app.ID] = app
	}
	if len(allowed) == 0 {
		return nil, http.StatusForbidden, store.ErrForbidden
	}
	return allowed, http.StatusOK, nil
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func toDetail(env, app string, rule ObservedRule) Detail {
	idBytes := sha256.Sum256([]byte(env + "\x00" + app + "\x00" + rule.GroupIdentity + "\x00" + rule.Name))
	state := "ok"
	if rule.State == "firing" || rule.State == "pending" {
		state = rule.State
	}
	last := ""
	if !rule.LastEvaluation.IsZero() {
		last = rule.LastEvaluation.UTC().Format(time.RFC3339Nano)
	}
	return Detail{
		Summary:    Summary{ID: "prom-" + hex.EncodeToString(idBytes[:12]), Folder: rule.Group, Name: rule.Name, State: state, EnvironmentID: env, ApplicationID: app},
		Datasource: "prometheus", Expr: rule.Expression, ForSeconds: rule.Duration,
		evaluationInterval: rule.EvaluationInterval,
		Labels:             rule.Labels, Annotations: rule.Annotations, LastEvaluation: last,
		EvaluationMS: rule.EvaluationTime * 1000, Partial: true,
		Unavailable: []string{"preview", "condition", "evaluation_schedule", "route_path", "ai_triage", "runbook"},
	}
}

func (s *Service) List(w http.ResponseWriter, r *http.Request) {
	items, unavailable, status, err := s.visible(r)
	if listState(w, err) {
		return
	}
	if err != nil {
		httpapi.Error(w, status, errorCode(status), "Alert rule source or authorization is unavailable")
		return
	}
	summaries := make([]Summary, 0, len(items))
	counts := map[string]int{"all": 0, "firing": 0, "scheduled": 0}
	for _, item := range items {
		summaries = append(summaries, item.Summary)
		counts["all"]++
		if item.State == "firing" {
			counts["firing"]++
		}
	}
	if len(unavailable) > 0 {
		status = http.StatusPartialContent
	}
	httpapi.WriteJSON(w, status, map[string]any{"items": summaries, "counts": counts, "partial": len(unavailable) > 0, "unavailable_environments": unavailable})
}

func (s *Service) Detail(w http.ResponseWriter, r *http.Request) {
	item, lookupError := s.findRule(r, r.PathValue("id"))
	if lookupError != nil {
		httpapi.Error(w, lookupError.status, lookupError.code, lookupError.message)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, item)
}

type ruleLookupError struct {
	status  int
	code    string
	message string
}

func (s *Service) findRule(r *http.Request, rawID string) (Detail, *ruleLookupError) {
	id := strings.TrimSpace(rawID)
	if len(id) > 100 || id == "" {
		return Detail{}, &ruleLookupError{http.StatusNotFound, "not_found", "Alert rule not found"}
	}
	items, unavailable, status, err := s.visible(r)
	if err != nil {
		return Detail{}, &ruleLookupError{status, errorCode(status), "Alert rule source or authorization is unavailable"}
	}
	var match *Detail
	for i := range items {
		item := &items[i]
		if item.ID == id {
			if match != nil {
				return Detail{}, &ruleLookupError{http.StatusServiceUnavailable, "alert_rule_identity_conflict", "Observed rule identity is ambiguous"}
			}
			match = item
		}
	}
	if match != nil {
		return *match, nil
	}
	if len(unavailable) > 0 {
		return Detail{}, &ruleLookupError{http.StatusServiceUnavailable, "rule_source_partial", "A rule provider is unavailable; the rule cannot be resolved"}
	}
	return Detail{}, &ruleLookupError{http.StatusNotFound, "not_found", "Alert rule not found"}
}

func errorCode(status int) string {
	if status == http.StatusForbidden {
		return "forbidden"
	}
	return "alert_rules_unavailable"
}
