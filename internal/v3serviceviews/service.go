// Package v3serviceviews projects the V3 service pages from registered
// applications and current, scoped operational evidence. A value without
// trustworthy evidence is null with a reason in the item's unavailable map;
// it never becomes zero, healthy or "not on call". Configured metadata is not
// live health.
package v3serviceviews

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

type Access interface {
	Authorize(context.Context, core.User, rbac.V3Decision) error
}

// AlertCovered returns the exact reviewed Operations source ID for a covered
// target. An empty ID means unknown coverage. Counts must use only that source.
type AlertCovered func(context.Context, string, string) string

type Service struct {
	Store        *store.Store
	Access       Access
	MetricReader MetricReader
	AlertCovered AlertCovered
	Now          func() time.Time
}

func New(st *store.Store, access Access, metrics MetricReader, alerts AlertCovered) *Service {
	return &Service{Store: st, Access: access, MetricReader: metrics, AlertCovered: alerts, Now: time.Now}
}

// Register owns only the two service page GET routes. The V3 composition root
// must omit those routes from catalog registration before calling this method;
// http.ServeMux rejects duplicate patterns. Catalog writes and mappings remain
// with v3catalog.
func (s *Service) Register(api *httpapi.Server) {
	s.registerSLO(api)
	api.Register("GET /api/v1/services", "viewer", s.list)
	api.MarkV3ObjectRoute("GET /api/v1/services", "services", "view")
	api.Register("GET /api/v1/services/{id}", "viewer", s.detail)
	api.MarkV3ObjectRoute("GET /api/v1/services/{id}", "services", "view")
}

type serviceRow struct {
	AppID, Key, Name, Description, Env, TeamID, Team, Tier, Oncall, Target string
}

// Reasons for a null field. The vocabulary is shared with GET /home and /me.
const (
	reasonNotConnected  = "not_connected"
	reasonNotConfigured = "not_configured"
	reasonNoAuthorized  = "no_authorized_target"
	reasonNotGenerated  = "not_generated"
	reasonUnavailable   = "unavailable"
	reasonStale         = "stale"
	// The service's CMDB relations cannot all be drawn as dependency edges
	// between services the reader can see. It is a property of the recorded
	// relations, not a failed read, so retrying does not change it (PA-191).
	reasonUnmappedRelation = "unmapped_relation"
)

var reasonOrder = []string{reasonNotConnected, reasonNotConfigured, reasonNoAuthorized,
	reasonNotGenerated, reasonUnavailable, reasonStale, reasonUnmappedRelation}

// unknownError marks evidence that is absent or untrustworthy for one group
// of fields. Handlers report it as null plus reason instead of failing the
// request. It matches errIncomplete so the watched-services status keeps its
// unavailable semantics.
type unknownError struct{ reason string }

func (e *unknownError) Error() string        { return "service evidence unknown: " + e.reason }
func (e *unknownError) Is(target error) bool { return target == errIncomplete }

func unknown(reason string) error { return &unknownError{reason: reason} }

// unknownReason separates a field-level unknown from a request failure.
func unknownReason(err error) (string, bool) {
	var u *unknownError
	if errors.As(err, &u) {
		return u.reason, true
	}
	return "", false
}

type release struct {
	Brand   string    `json:"brand"`
	Version string    `json:"version"`
	When    string    `json:"when"`
	At      time.Time `json:"-"`
}

type sloSummary struct {
	Target     *string  `json:"target"`
	BudgetLeft *float64 `json:"budget_left"`
}

type summary struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Desc        string            `json:"desc"`
	Tier        *string           `json:"tier"`
	Team        *string           `json:"team"`
	Oncall      *string           `json:"oncall"`
	Status      *string           `json:"status"`
	SLO         sloSummary        `json:"slo"`
	Release     *release          `json:"release"`
	Alerts      *int              `json:"alerts"`
	Connectors  []string          `json:"connectors"`
	Unavailable map[string]string `json:"unavailable"`
	Env         string            `json:"-"`
	obs         observation
	row         serviceRow
}

// evidence lists the separately authorized sources a reply relies on. The
// final recheck repeats exactly these grants and source checks, so a shown
// value cannot outlive its grant, alert source or metric configuration. An
// unknown field relied on nothing and needs no recheck.
type evidence struct {
	metrics     bool // connectors.view and the sealed Prometheus target
	seal        metricSourceSeal
	alertSource string // events.view and this reviewed alert source
	events      bool   // events.view for incident records
	resources   bool
	canvas      bool
}

func (e *evidence) merge(o evidence) {
	if o.metrics {
		e.metrics, e.seal = true, o.seal
	}
	if o.alertSource != "" {
		e.alertSource = o.alertSource
	}
	e.events = e.events || o.events
	e.resources = e.resources || o.resources
	e.canvas = e.canvas || o.canvas
}

type checked struct {
	row  serviceRow
	used evidence
}

type facetItem struct {
	Label string `json:"label"`
	Count int    `json:"count"`
	On    bool   `json:"on"`
}
type facet struct {
	Key   string      `json:"key"`
	Title string      `json:"title"`
	Items []facetItem `json:"items"`
}

var errLimit = errors.New("authorized service view exceeds limit")

// errIncomplete is the watched-services "status unavailable" signal.
var errIncomplete = errors.New("required service evidence is unavailable")

// errSourceChanged means evidence already read changed or was withdrawn
// before the reply. That reply cannot be served; a retry reads afresh.
var errSourceChanged = errors.New("service evidence source changed during the read")

func encodeID(app, env string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(app + "\x00" + env))
}

func decodeID(id string) (string, string, bool) {
	if id == "" || len(id) > 1024 {
		return "", "", false
	}
	b, err := base64.RawURLEncoding.DecodeString(id)
	if err != nil {
		return "", "", false
	}
	parts := strings.Split(string(b), "\x00")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" || encodeID(parts[0], parts[1]) != id {
		return "", "", false
	}
	return parts[0], parts[1], true
}

func (s *Service) scope(w http.ResponseWriter, r *http.Request) (httpapi.V3EnvironmentScope, bool) {
	if s == nil || s.Store == nil || s.Store.DB == nil || s.Access == nil {
		httpapi.Error(w, 503, "service_view_unavailable", "Service view storage or policy is unavailable")
		return httpapi.V3EnvironmentScope{}, false
	}
	scope, ok := httpapi.V3ScopeFromContext(r.Context())
	if !ok || len(scope.EnvironmentIDs) == 0 || httpapi.CurrentUser(r).ID == "" {
		httpapi.Error(w, 503, "environment_scope_unavailable", "Environment scope is unavailable")
		return httpapi.V3EnvironmentScope{}, false
	}
	return scope, true
}

type filters struct{ q, tier, team, status, tab, sort string }

func parseFilters(raw string) (filters, bool) {
	var out filters
	q, err := url.ParseQuery(raw)
	if err != nil {
		return out, false
	}
	for key, values := range q {
		if len(values) != 1 || (key != "q" && key != "tier" && key != "team" && key != "status" && key != "tab" && key != "sort") {
			return out, false
		}
		if len(values[0]) > 200 || strings.TrimSpace(values[0]) != values[0] {
			return out, false
		}
	}
	out = filters{q: q.Get("q"), tier: q.Get("tier"), team: q.Get("team"), status: q.Get("status"), tab: q.Get("tab"), sort: q.Get("sort")}
	if out.tier != "" && !validTier(out.tier) {
		return out, false
	}
	if out.status != "" && out.status != "ok" && out.status != "warn" && out.status != "danger" {
		return out, false
	}
	if out.tab != "" && out.tab != "all" && out.tab != "mine" && out.tab != "watched" {
		return out, false
	}
	if out.sort != "" && out.sort != "status" && out.sort != "budget" && out.sort != "alerts" && out.sort != "name" {
		return out, false
	}
	return out, true
}

const pageSize = 128
const viewLimit = 50 // each visible row requires a separate live scoped read

const rowsSQL = `SELECT a.id,a.key,a.name,a.description,ve.environment_id,
 COALESCE(a.owner_team_id,''),COALESCE(t.name,''),COALESCE(ep.tier,''),
 COALESCE(u.name,''),COALESCE(ep.slo_target_percent::text,'')
 FROM applications a
 JOIN v3_application_environments ve ON ve.application_id=a.id
 JOIN application_environments e ON e.application_id=a.id AND e.key=ve.environment_id AND NOT e.archived
 LEFT JOIN teams t ON t.id=a.owner_team_id
 LEFT JOIN v3_service_environment_profiles ep ON ep.application_id=a.id AND ep.environment_id=ve.environment_id
 LEFT JOIN users u ON u.id=ep.oncall_user_id AND NOT u.disabled AND u.deleted_at IS NULL
 WHERE NOT a.archived AND ve.environment_id=ANY($1::text[])
 AND ($2='' OR a.id=$2) AND ($3='' OR ve.environment_id=$3)
 AND ($4='' OR position(lower($4) in lower(a.key))>0 OR position(lower($4) in lower(a.name))>0)
 AND ($5='' OR ep.tier=$5)
 AND ($6='' OR a.owner_team_id=$6 OR t.name=$6)
 AND ($10::text IN ('','all') OR ($10='mine' AND (ep.oncall_user_id=$11 OR EXISTS(SELECT 1 FROM team_members m WHERE m.user_id=$11 AND m.team_id=a.owner_team_id))) OR ($10='watched' AND EXISTS(SELECT 1 FROM v3_user_watched_services w WHERE w.user_id=$11 AND w.application_id=a.id AND w.environment_id=ve.environment_id)))
 AND (a.key,ve.environment_id,a.id)>($7,$8,$9)
 ORDER BY a.key,ve.environment_id,a.id LIMIT 128`

func (s *Service) canView(ctx context.Context, user core.User, row serviceRow, module string) (bool, error) {
	decision := rbac.V3Decision{Module: module, Action: "view", EnvironmentID: row.Env, ApplicationID: row.AppID}
	err := s.Access.Authorize(ctx, user, decision)
	if err == nil {
		return true, nil
	}
	if !errors.Is(err, store.ErrForbidden) {
		return false, err
	}
	if row.TeamID == "" {
		return false, nil
	}
	err = s.Access.Authorize(ctx, user, rbac.V3Decision{Module: module, Action: "view", EnvironmentID: row.Env, TeamID: row.TeamID})
	if errors.Is(err, store.ErrForbidden) {
		return false, nil
	}
	return err == nil, err
}

func (s *Service) rows(ctx context.Context, user core.User, envs []string, app, env string, f filters) ([]serviceRow, error) {
	result := []serviceRow{}
	var lastKey, lastEnv, lastID string
	for {
		rows, err := s.Store.DB.Query(ctx, rowsSQL, envs, app, env, f.q, f.tier, f.team, lastKey, lastEnv, lastID, f.tab, user.ID)
		if err != nil {
			return nil, err
		}
		batch := make([]serviceRow, 0, pageSize)
		for rows.Next() {
			var row serviceRow
			if err = rows.Scan(&row.AppID, &row.Key, &row.Name, &row.Description, &row.Env, &row.TeamID, &row.Team, &row.Tier, &row.Oncall, &row.Target); err != nil {
				break
			}
			batch = append(batch, row)
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			return nil, err
		}
		for _, row := range batch {
			visible, e := s.canView(ctx, user, row, "services")
			if e != nil {
				return nil, e
			}
			if !visible {
				continue
			}
			if len(result) == viewLimit {
				return nil, errLimit
			}
			result = append(result, row)
		}
		if len(batch) < pageSize {
			return result, nil
		}
		last := batch[len(batch)-1]
		lastKey, lastEnv, lastID = last.Key, last.Env, last.AppID
	}
}

// Registration allows T3 (migration 042), so the contract does too.
func validTier(tier string) bool {
	return tier == "T0" || tier == "T1" || tier == "T2" || tier == "T3"
}

// identity projects configured metadata. Missing configuration is null with
// not_configured; an unexpected stored value is not guessed.
func identity(row serviceRow, unavailable map[string]string) (tier, team, oncall *string) {
	switch {
	case row.Tier == "":
		unavailable["tier"] = reasonNotConfigured
	case !validTier(row.Tier):
		unavailable["tier"] = reasonUnavailable
	default:
		tier = &row.Tier
	}
	if row.Team == "" {
		unavailable["team"] = reasonNotConfigured
	} else {
		team = &row.Team
	}
	if row.Oncall == "" {
		unavailable["oncall"] = reasonNotConfigured
	} else {
		oncall = &row.Oncall
	}
	return tier, team, oncall
}

// sloTarget is the configured request-based target; it is not a measurement.
func sloTarget(row serviceRow) (float64, bool) {
	n, err := strconv.ParseFloat(row.Target, 64)
	return n, err == nil && n > 0 && n < 100
}

func formatTarget(target float64) string { return fmt.Sprintf("%g%%", target) }

// finalCheck runs after every remote and local read. It reconfirms the
// session, each shown service's identity and ownership, and exactly the
// grants and sources its shown values relied on.
func (s *Service) finalCheck(ctx context.Context, r *http.Request, items []checked) error {
	cookie, err := r.Cookie("aiops_session")
	if err != nil || cookie.Value == "" {
		return store.ErrForbidden
	}
	current, err := s.Store.Session(ctx, cookie.Value)
	if err != nil || current.ID != httpapi.CurrentUser(r).ID {
		return store.ErrForbidden
	}
	for _, item := range items {
		row, used := item.row, item.used
		if used.alertSource != "" && (s.AlertCovered == nil || s.AlertCovered(ctx, row.AppID, row.Env) != used.alertSource) {
			return errSourceChanged
		}
		// Team-scoped policy accepts a caller-supplied team ID. Resolve the
		// persisted owner again after remote reads so a reassigned application
		// cannot be exposed with its former team's still-valid grant.
		currentRows, e := s.rows(ctx, current, []string{row.Env}, row.AppID, row.Env, filters{})
		if e != nil {
			return e
		}
		if len(currentRows) != 1 || currentRows[0] != row {
			return store.ErrForbidden
		}
		modules := []string{"services"}
		if used.metrics {
			modules = append(modules, "connectors")
		}
		if used.alertSource != "" || used.events {
			modules = append(modules, "events")
		}
		if used.resources {
			modules = append(modules, "resources")
		}
		if used.canvas {
			modules = append(modules, "canvas")
		}
		for _, module := range modules {
			allowed, e := s.canView(ctx, current, row, module)
			if e != nil {
				return e
			}
			if !allowed {
				return store.ErrForbidden
			}
		}
		if used.metrics {
			seal, e := s.sourceSeal(ctx, row)
			if _, isUnknown := unknownReason(e); isUnknown || (e == nil && seal != used.seal) {
				return errSourceChanged
			}
			if e != nil {
				return e
			}
		}
	}
	return nil
}

func writeReadError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errLimit):
		httpapi.Error(w, 422, "result_limit", "Narrow the service filter")
	case errors.Is(err, errSourceChanged):
		httpapi.Error(w, 503, "service_source_changed", "Service evidence changed during the read; retry")
	case errors.Is(err, store.ErrForbidden):
		httpapi.Error(w, 403, "forbidden", "Service view is outside your authorized scope")
	case errors.Is(err, pgx.ErrNoRows):
		httpapi.Error(w, 404, "not_found", "Service not found")
	default:
		httpapi.Error(w, 503, "service_view_unavailable", "Service view source or policy is unavailable")
	}
}
