// Package v3home builds the home view from persisted, currently authorized
// records. An absent telemetry or briefing producer is never rendered as a
// healthy service, an empty alert stream, or an AI-generated summary.
package v3home

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

type Access interface {
	AuthorizeWithTx(context.Context, pgx.Tx, core.User, rbac.V3Decision) error
}

type Service struct {
	DB     *pgxpool.Pool
	Access Access
	Now    func() time.Time
}

func New(db *pgxpool.Pool, access Access) *Service {
	return &Service{DB: db, Access: access, Now: time.Now}
}

func (s *Service) Register(api *httpapi.Server) {
	api.Register("GET /api/v1/home", "viewer", s.Get)
	api.MarkV3ObjectRoute("GET /api/v1/home", "home", "view")
}

type Greeting struct {
	Name    string `json:"name"`
	Summary string `json:"summary"`
}

type KPI struct {
	ID     string    `json:"id"`
	Label  string    `json:"label"`
	Value  string    `json:"value"`
	Series []float64 `json:"series"`
	Tone   string    `json:"tone,omitempty"`
}

type Attention struct {
	ID       string  `json:"id"`
	Severity *string `json:"severity"`
	Service  string  `json:"service"`
	Env      string  `json:"env"`
	Symptom  string  `json:"symptom"`
	Meta     string  `json:"meta"`
	AI       *string `json:"ai"`
	Owner    string  `json:"owner,omitempty"`
	Href     string  `json:"href"`
}

type ServiceItem struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Env       string    `json:"env"`
	Tier      *string   `json:"tier"`
	Status    *string   `json:"status"`
	BudgetPct *float64  `json:"budget_pct"`
	P99       *string   `json:"p99"`
	ErrorRate *string   `json:"error_rate"`
	Series    []float64 `json:"series"`
}

type Activity struct {
	ID   string `json:"id"`
	Time string `json:"time"`
	Kind string `json:"kind"`
	Tone string `json:"tone"`
	Text string `json:"text"`
	Href string `json:"href"`
}

type Recent struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	Title string `json:"title"`
	Meta  string `json:"meta"`
	Href  string `json:"href"`
}

type AlertTrend struct {
	Labels  []string `json:"labels"`
	Counts  [][4]int `json:"counts"`
	Summary string   `json:"summary"`
}

type Summary struct {
	Greeting     Greeting          `json:"greeting"`
	Briefing     any               `json:"briefing"`
	Suggestions  []any             `json:"suggestions"`
	KPIs         []KPI             `json:"kpis"`
	Attention    []Attention       `json:"attention"`
	Todos        []any             `json:"todos"`
	Services     []ServiceItem     `json:"services"`
	AlertTrend   any               `json:"alert_trend"`
	Activity     []Activity        `json:"activity"`
	Recent       []Recent          `json:"recent"`
	Availability map[string]string `json:"availability"`
}

type target struct {
	AppID, Env, Name, TeamID string
	Tier                     *string
}

const maxTargets = 100
const maxIncidents = 1000
const maxProposals = 500

var errTooMany = errors.New("home view exceeds bounded result size")

// Get deliberately has no filter: the V3 environment gate already selected
// the only environments this authenticated request may inspect.
func (s *Service) Get(w http.ResponseWriter, r *http.Request) {
	if s == nil || s.DB == nil || s.Access == nil {
		httpapi.Error(w, 503, "home_unavailable", "Home storage or policy is unavailable")
		return
	}
	if r.URL.RawQuery != "" {
		httpapi.Error(w, 422, "invalid_query", "Home does not accept query parameters")
		return
	}
	scope, ok := httpapi.V3ScopeFromContext(r.Context())
	user := httpapi.CurrentUser(r)
	if !ok || len(scope.EnvironmentIDs) == 0 || user.ID == "" {
		httpapi.Error(w, 503, "environment_scope_unavailable", "Environment scope is unavailable")
		return
	}
	cookie, err := r.Cookie("aiops_session")
	if err != nil || cookie.Value == "" {
		httpapi.Error(w, 401, "unauthorized", "Login required")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		httpapi.Error(w, 503, "home_unavailable", "Home data is unavailable")
		return
	}
	defer tx.Rollback(ctx)
	if err = store.LockActiveSession(ctx, tx, cookie.Value, user.ID); err != nil {
		readError(w, err)
		return
	}
	name, err := currentName(ctx, tx, user.ID)
	if err != nil {
		readError(w, err)
		return
	}
	targets, err := s.targets(ctx, tx, user, scope.EnvironmentIDs)
	if err != nil {
		readError(w, err)
		return
	}
	now := time.Now().UTC()
	if s.Now != nil {
		now = s.Now().UTC()
	}
	out := Summary{
		Greeting: Greeting{Name: name}, Briefing: nil,
		Suggestions: []any{}, KPIs: []KPI{}, Attention: []Attention{}, Todos: []any{},
		Services: []ServiceItem{}, AlertTrend: nil, Activity: []Activity{}, Recent: []Recent{},
		Availability: map[string]string{
			"briefing": "not_generated", "oncall": "not_connected",
			"service_metrics": "not_connected", "alert_trend": "not_connected",
			"suggestions": "not_generated", "todo_actions": "not_connected",
		},
	}
	if err = s.populate(ctx, tx, user, targets, scope.EnvironmentIDs, now, &out); err != nil {
		readError(w, err)
		return
	}
	// A read snapshot and active session lock remain in force until Commit.
	// An expired session is not permitted to finish a long aggregation.
	if err = store.LockActiveSession(ctx, tx, cookie.Value, user.ID); err != nil {
		readError(w, err)
		return
	}
	if err = tx.Commit(ctx); err != nil {
		readError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpapi.WriteJSON(w, 200, out)
}

func currentName(ctx context.Context, tx pgx.Tx, id string) (string, error) {
	var name string
	err := tx.QueryRow(ctx, `SELECT name FROM users WHERE id=$1 AND NOT disabled AND deleted_at IS NULL`, id).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", store.ErrForbidden
	}
	return name, err
}

func (s *Service) allowed(ctx context.Context, tx pgx.Tx, user core.User, module string, row target) (bool, error) {
	err := s.Access.AuthorizeWithTx(ctx, tx, user, rbac.V3Decision{Module: module, Action: "view", EnvironmentID: row.Env, ApplicationID: row.AppID})
	if err == nil {
		return true, nil
	}
	if !errors.Is(err, store.ErrForbidden) {
		return false, err
	}
	if row.TeamID == "" {
		return false, nil
	}
	err = s.Access.AuthorizeWithTx(ctx, tx, user, rbac.V3Decision{Module: module, Action: "view", EnvironmentID: row.Env, TeamID: row.TeamID})
	if errors.Is(err, store.ErrForbidden) {
		return false, nil
	}
	return err == nil, err
}

func (s *Service) targets(ctx context.Context, tx pgx.Tx, user core.User, envs []string) ([]target, error) {
	rows, err := tx.Query(ctx, `SELECT a.id,ve.environment_id,a.name,COALESCE(a.owner_team_id,''),ep.tier
	 FROM applications a JOIN v3_application_environments ve ON ve.application_id=a.id
	 JOIN application_environments e ON e.application_id=a.id AND e.key=ve.environment_id AND NOT e.archived
	 LEFT JOIN v3_service_environment_profiles ep ON ep.application_id=a.id AND ep.environment_id=ve.environment_id
	 WHERE NOT a.archived AND ve.environment_id=ANY($1::text[])
	 ORDER BY a.id,ve.environment_id LIMIT 101`, envs)
	if err != nil {
		return nil, err
	}
	candidates := []target{}
	for rows.Next() {
		var row target
		if err = rows.Scan(&row.AppID, &row.Env, &row.Name, &row.TeamID, &row.Tier); err != nil {
			break
		}
		candidates = append(candidates, row)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(candidates) > maxTargets {
		return nil, errTooMany
	}
	// Every section has its own module grant. Filtering here by services.view
	// would incorrectly hide an event from a user with only events.view.
	return candidates, nil
}

func readError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrForbidden), errors.Is(err, store.ErrSessionRevoked):
		httpapi.Error(w, 403, "forbidden", "Home access denied")
	case errors.Is(err, errTooMany):
		httpapi.Error(w, 503, "home_result_limit", "Home has more records than the bounded view can summarize")
	case errors.Is(err, context.DeadlineExceeded):
		httpapi.Error(w, 503, "home_timeout", "Home data timed out")
	default:
		httpapi.Error(w, 503, "home_unavailable", "Home data is unavailable")
	}
}

func displaySummary(incidents, proposals int) string {
	return fmt.Sprintf("%d 个未解决事件，%d 个待复核提案。未接入的健康指标不计入。", incidents, proposals)
}

func cleanTitle(title string) string { return strings.TrimSpace(title) }
