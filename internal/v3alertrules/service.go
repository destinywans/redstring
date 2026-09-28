// Package v3alertrules exposes only alerting rules observed from a configured,
// environment-bound provider. It never treats an alert event as a rule definition.
package v3alertrules

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
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
	ID, EnvironmentID string
}

type DatabaseCatalog struct{ DB *pgxpool.Pool }

func (c DatabaseCatalog) Applications(ctx context.Context, environments []string) ([]Application, error) {
	if c.DB == nil {
		return nil, errors.New("application catalog unavailable")
	}
	rows, err := c.DB.Query(ctx, `SELECT a.id,ve.environment_id FROM applications a
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
		if err := rows.Scan(&app.ID, &app.EnvironmentID); err != nil {
			return nil, err
		}
		apps = append(apps, app)
	}
	return apps, rows.Err()
}

// Source is trusted process configuration. List must read a real provider and
// return only its observed rules. The service still enforces labels and RBAC.
type Source interface {
	List(context.Context, string) ([]ObservedRule, error)
}

type ObservedRule struct {
	GroupIdentity  string // exact provider file + NUL + group name
	Group          string
	Name           string
	Expression     string
	State          string
	Duration       float64
	Labels         map[string]string
	Annotations    map[string]string
	LastEvaluation time.Time
	EvaluationTime float64
}

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
	Datasource     string            `json:"datasource"`
	Expr           string            `json:"expr"`
	ForSeconds     float64           `json:"for_seconds"`
	Labels         map[string]string `json:"labels"`
	Annotations    map[string]string `json:"annotations"`
	LastEvaluation string            `json:"last_evaluation,omitempty"`
	EvaluationMS   float64           `json:"evaluation_ms"`
	Partial        bool              `json:"partial"`
	Unavailable    []string          `json:"unavailable"`
}

type Service struct {
	Catalog  Catalog
	Access   Access
	Source   Source
	bindings map[string]string // environment + provider group identity -> application ID
}

// RuleGroupBinding is trusted process configuration, reviewed alongside the
// provider rule file. Labels from Prometheus cannot create or change ownership.
type RuleGroupBinding struct {
	EnvironmentID string
	ApplicationID string
	File          string
	Group         string
}

func New(catalog Catalog, access Access, source Source, bindings []RuleGroupBinding) (*Service, error) {
	index := make(map[string]string, len(bindings))
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
	}
	return &Service{Catalog: catalog, Access: access, Source: source, bindings: index}, nil
}

func bindingKey(env, identity string) string {
	return env + "\x00" + identity
}

func (s *Service) Register(api *httpapi.Server) {
	api.Register("GET /api/v1/alert-rules", "viewer", s.MainList)
	api.MarkV3ObjectRoute("GET /api/v1/alert-rules", "alert_rules", "view")
	api.Register("GET /api/v1/alert-rules/observed", "viewer", s.List)
	api.MarkV3ObjectRoute("GET /api/v1/alert-rules/observed", "alert_rules", "view")
	api.Register("GET /api/v1/alert-rules/observed/{id}", "viewer", s.Detail)
	api.MarkV3ObjectRoute("GET /api/v1/alert-rules/observed/{id}", "alert_rules", "view")
}

// MainList serves the complete V3 rule-list contract. Unlike the auxiliary
// observed route, it never publishes partial counts when one authorized
// environment's provider is unavailable. Prometheus rules have no cron
// schedule, so the scheduled count is truthfully zero.
func (s *Service) MainList(w http.ResponseWriter, r *http.Request) {
	items, unavailable, status, err := s.visible(r)
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
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"items": summaries, "counts": counts})
}

func (s *Service) visible(r *http.Request) ([]Detail, []string, int, error) {
	scope, ok := httpapi.V3ScopeFromContext(r.Context())
	if !ok || len(scope.EnvironmentIDs) == 0 || s.Catalog == nil || s.Access == nil {
		return nil, nil, http.StatusServiceUnavailable, errors.New("environment scope, catalog or policy unavailable")
	}
	apps, err := s.Catalog.Applications(r.Context(), scope.EnvironmentIDs)
	if err != nil {
		return nil, nil, http.StatusServiceUnavailable, err
	}
	allowed := map[string]map[string]bool{}
	for _, app := range apps {
		if !contains(scope.EnvironmentIDs, app.EnvironmentID) || app.ID == "" {
			continue
		}
		err := s.Access.Authorize(r.Context(), httpapi.CurrentUser(r), rbac.V3Decision{Module: "alert_rules", Action: "view", EnvironmentID: app.EnvironmentID, ApplicationID: app.ID})
		if errors.Is(err, store.ErrForbidden) {
			continue
		}
		if err != nil {
			return nil, nil, http.StatusServiceUnavailable, err
		}
		if allowed[app.EnvironmentID] == nil {
			allowed[app.EnvironmentID] = map[string]bool{}
		}
		allowed[app.EnvironmentID][app.ID] = true
	}
	if len(allowed) == 0 {
		return nil, nil, http.StatusForbidden, store.ErrForbidden
	}
	if s.Source == nil || len(s.bindings) == 0 {
		return nil, nil, http.StatusServiceUnavailable, errors.New("alert rule provider or group bindings not configured")
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
			if !bound || rule.Labels["environment"] != env || rule.Labels["application"] != app || !allowed[env][app] || rule.Name == "" || rule.Group == "" {
				continue
			}
			items = append(items, toDetail(env, app, rule))
		}
	}
	if len(unavailable) > 0 && len(unavailable) == len(allowed) {
		return nil, nil, http.StatusServiceUnavailable, errors.New("all authorized rule providers unavailable")
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items, unavailable, http.StatusOK, nil
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
		Labels: rule.Labels, Annotations: rule.Annotations, LastEvaluation: last,
		EvaluationMS: rule.EvaluationTime * 1000, Partial: true,
		Unavailable: []string{"preview", "condition", "evaluation_schedule", "route_path", "ai_triage", "runbook"},
	}
}

func (s *Service) List(w http.ResponseWriter, r *http.Request) {
	items, unavailable, status, err := s.visible(r)
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
	id := strings.TrimSpace(r.PathValue("id"))
	if len(id) > 100 || id == "" {
		httpapi.Error(w, 404, "not_found", "Alert rule not found")
		return
	}
	items, unavailable, status, err := s.visible(r)
	if err != nil {
		httpapi.Error(w, status, errorCode(status), "Alert rule source or authorization is unavailable")
		return
	}
	var match *Detail
	for i := range items {
		item := &items[i]
		if item.ID == id {
			if match != nil {
				httpapi.Error(w, http.StatusServiceUnavailable, "alert_rule_identity_conflict", "Observed rule identity is ambiguous")
				return
			}
			match = item
		}
	}
	if match != nil {
		httpapi.WriteJSON(w, http.StatusOK, match)
		return
	}
	if len(unavailable) > 0 {
		httpapi.Error(w, 503, "rule_source_partial", "A rule provider is unavailable; the rule cannot be resolved")
		return
	}
	httpapi.Error(w, 404, "not_found", "Alert rule not found")
}

func (s *Service) DryRun(w http.ResponseWriter, r *http.Request) {
	// No reviewed expression/threshold and historical evaluator is available.
	// In particular, accepting a request body here would permit arbitrary PromQL.
	httpapi.Error(w, 503, "dry_run_unavailable", "Historical alert rule evaluation is not configured")
}

func errorCode(status int) string {
	if status == http.StatusForbidden {
		return "forbidden"
	}
	return "alert_rules_unavailable"
}
