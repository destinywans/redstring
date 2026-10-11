// Package v3changeview projects one selected, source-backed delivery run into
// the frozen ChangesView. An incomplete review or source scope fails closed.
package v3changeview

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
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
	AuthorizeWithTx(context.Context, pgx.Tx, core.User, rbac.V3Decision) error
}

// AlertCovered reports whether a reviewed operational source covers this exact
// application/environment and full requested interval. Without coverage, an
// empty alert band is unknown.
type AlertCovered func(context.Context, string, string, time.Time, time.Time) string

type Service struct {
	Store        *store.Store
	Access       Access
	Reviews      ReviewSource
	AlertCovered AlertCovered
	Now          func() time.Time
	// GitLab reads the GitLab changes of sources=gitlab (D11); nil leaves
	// that source not configured.
	GitLab GitLabChanges
}

func New(st *store.Store, access Access, reviews ReviewSource, covered AlertCovered) *Service {
	return &Service{Store: st, Access: access, Reviews: reviews, AlertCovered: covered, Now: time.Now}
}

func (s *Service) Register(api *httpapi.Server) {
	api.Register("GET /api/v1/changes", "viewer", s.view)
	api.MarkV3ObjectRoute("GET /api/v1/changes", "services", "view")
	api.Register("POST /api/v1/changes/{run_id}/review", "viewer", s.acceptReview)
	api.MarkV3ObjectRoute("POST /api/v1/changes/{run_id}/review", "services", "change")
	api.Register("GET /api/v1/changes/pipelines/{pipeline_id}", "viewer", s.gitLabPipeline)
	api.MarkV3ObjectRoute("GET /api/v1/changes/pipelines/{pipeline_id}", "services", "view")
}

type kpi struct {
	ID     string    `json:"id"`
	Label  string    `json:"label"`
	Value  string    `json:"value"`
	Unit   string    `json:"unit,omitempty"`
	Sub    string    `json:"sub,omitempty"`
	Tone   string    `json:"tone,omitempty"`
	Series []float64 `json:"series"`
}
type sourceCount struct {
	Brand string `json:"brand"`
	Name  string `json:"name"`
	Count int    `json:"count"`
	On    bool   `json:"on"`
}
type mark struct {
	At    float64 `json:"at"`
	Tone  string  `json:"tone"`
	Label string  `json:"label,omitempty"`
}
type lane struct {
	Service string       `json:"service"`
	Marks   []mark       `json:"marks"`
	Alerts  [][2]float64 `json:"alerts"`
}
type pipeline struct {
	ID       string   `json:"id"`
	Brand    string   `json:"brand"`
	Ref      string   `json:"ref"`
	Service  string   `json:"service"`
	Title    string   `json:"title"`
	Trigger  string   `json:"trigger"`
	Status   string   `json:"status"`
	Stages   []string `json:"stages"`
	Duration string   `json:"duration"`
	Time     string   `json:"time"`
}
type job struct {
	Name  string `json:"name"`
	State string `json:"state"`
	Brand string `json:"brand,omitempty"`
}
type stage struct {
	Name string `json:"name"`
	Jobs []job  `json:"jobs"`
}
type detail struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Meta  string `json:"meta"`
	// ReviewedAt is the review time in RFC 3339; Meta no longer repeats it
	// (PA-83).
	ReviewedAt *string `json:"reviewed_at"`
	Brand      string  `json:"brand"`
	Stages     []stage `json:"stages"`
	Diff       struct {
		Path  string `json:"path"`
		Minus string `json:"minus"`
		Plus  string `json:"plus"`
	} `json:"diff"`
	Risk struct {
		Title string `json:"title"`
		Meta  string `json:"meta"`
		Text  string `json:"text"`
	} `json:"risk"`
	Alert struct {
		Text     string `json:"text"`
		Incident string `json:"incident"`
	} `json:"alert"`
}
type viewResponse struct {
	KPIs    []kpi         `json:"kpis"`
	Sources []sourceCount `json:"sources"`
	Lanes   []lane        `json:"lanes"`
	// AxisTimes are the axis instants in RFC 3339 (PA-83; the
	// "Jan 02 15:04 UTC" axis text was removed).
	AxisTimes []string   `json:"axis_times"`
	Pipelines []pipeline `json:"pipelines"`
	Detail    detail     `json:"detail"`
}

var errIncomplete = errors.New("complete change evidence unavailable")
var errLimit = errors.New("change view exceeds bounded source limit")

func decodeServiceID(id string) (string, string, bool) {
	if id == "" || len(id) > 1024 {
		return "", "", false
	}
	raw, err := base64.RawURLEncoding.DecodeString(id)
	if err != nil || base64.RawURLEncoding.EncodeToString(raw) != id {
		return "", "", false
	}
	parts := strings.Split(string(raw), "\x00")
	returnPair := len(parts) == 2 && parts[0] != "" && parts[1] != ""
	if !returnPair {
		return "", "", false
	}
	return parts[0], parts[1], true
}

type requestFilter struct {
	ServiceID, RunID, Source string
	Window                   time.Duration
}

func parseRequest(raw string) (requestFilter, bool) {
	var out requestFilter
	q, err := url.ParseQuery(raw)
	if err != nil {
		return out, false
	}
	for key, values := range q {
		if len(values) != 1 || (key != "service_id" && key != "run_id" && key != "range" && key != "sources") ||
			len(values[0]) > 1024 || strings.TrimSpace(values[0]) != values[0] {
			return out, false
		}
	}
	out.ServiceID, out.RunID = q.Get("service_id"), q.Get("run_id")
	// The GitLab view covers a service and range; a native view one run.
	if q.Get("sources") == "gitlab" {
		if out.ServiceID == "" || q.Has("run_id") {
			return out, false
		}
	} else if out.ServiceID == "" || out.RunID == "" || len(out.RunID) > 128 || strings.ContainsAny(out.RunID, "\r\n\x00") {
		return out, false
	}
	if _, _, ok := decodeServiceID(out.ServiceID); !ok {
		return out, false
	}
	switch q.Get("range") {
	case "", "24h":
		out.Window = 24 * time.Hour
	case "7d":
		out.Window = 7 * 24 * time.Hour
	default:
		return out, false
	}
	switch q.Get("sources") {
	case "", "native":
		out.Source = "native"
	case "gitlab":
		out.Source = "gitlab"
	default:
		return out, false
	}
	return out, true
}

type serviceRow struct {
	AppID, EnvID, EnvKey, Key, Name, TeamID string
}

func (s *Service) canView(ctx context.Context, user core.User, row serviceRow, module string) (bool, error) {
	decision := rbac.V3Decision{Module: module, Action: "view", ApplicationID: row.AppID, EnvironmentID: row.EnvKey}
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
	decision.ApplicationID, decision.TeamID = "", row.TeamID
	err = s.Access.Authorize(ctx, user, decision)
	if errors.Is(err, store.ErrForbidden) {
		return false, nil
	}
	return err == nil, err
}

func (s *Service) scope(r *http.Request, app, env string) (serviceRow, error) {
	var zero serviceRow
	if s == nil || s.Store == nil || s.Store.DB == nil || s.Access == nil || s.Reviews == nil || s.AlertCovered == nil {
		return zero, errIncomplete
	}
	scope, ok := httpapi.V3ScopeFromContext(r.Context())
	if !ok || httpapi.CurrentUser(r).ID == "" {
		return zero, errIncomplete
	}
	selected := false
	for _, id := range scope.EnvironmentIDs {
		selected = selected || id == env
	}
	if !selected {
		return zero, pgx.ErrNoRows
	}
	return s.loadService(r.Context(), app, env)
}

func (s *Service) finalCheck(ctx context.Context, r *http.Request, before serviceRow) error {
	cookie, err := r.Cookie("aiops_session")
	if err != nil || cookie.Value == "" {
		return store.ErrForbidden
	}
	current, err := s.Store.Session(ctx, cookie.Value)
	if err != nil || current.ID != httpapi.CurrentUser(r).ID {
		return store.ErrForbidden
	}
	after, err := s.loadService(ctx, before.AppID, before.EnvKey)
	if err != nil || after != before {
		return store.ErrForbidden
	}
	for _, module := range []string{"services", "events"} {
		allowed, e := s.canView(ctx, current, before, module)
		if e != nil {
			return e
		}
		if !allowed {
			return store.ErrForbidden
		}
	}
	return nil
}

func writeReadError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrForbidden):
		httpapi.Error(w, 403, "forbidden", "Change view is outside your authorized scope")
	case errors.Is(err, pgx.ErrNoRows):
		httpapi.Error(w, 404, "not_found", "Change source not found")
	case errors.Is(err, errLimit):
		httpapi.Error(w, 413, "change_view_limit", "Change evidence exceeds the bounded query")
	case errors.Is(err, errIncomplete):
		httpapi.Error(w, 503, "change_view_incomplete", "Required change evidence is unavailable")
	default:
		httpapi.Error(w, 503, "change_view_unavailable", "Change source or authorization unavailable")
	}
}
