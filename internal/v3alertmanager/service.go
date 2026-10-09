package v3alertmanager

import (
	"context"
	"errors"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
	"github.com/wans/aiops-canvas/internal/v3configsync"
)

// Source is a trusted, environment-bound Alertmanager client. The service
// still requires an exact environment matcher on each returned silence, so a
// shared Alertmanager cannot leak another environment's broad silence.
type Source interface {
	ListSilences(context.Context, string) ([]Silence, error)
	ListAlerts(context.Context, string) ([]Alert, error)
}

// RouteRepository exports a versioned route draft to a reviewed GitLab MR.
// It never installs or activates Alertmanager configuration.
type RouteRepository interface {
	ProjectID() int64
	BranchPrefix() string
	TargetBranch() string
	Export(context.Context, string, string, []byte, string) (v3configsync.MR, error)
}

type Service struct {
	Source                 Source
	SilenceWorkflow        SilenceWorkflowReadiness
	SilenceCreateProposals SilenceCreateProposalProducer
	SilenceCancelProposals SilenceCancelProposalProducer
	RouteRepository        RouteRepository
	InhibitionRepository   RouteRepository
	InhibitionHistory      InhibitionHistorySource
	// ConfigPromotionBranchPrefix is the active config-sync import namespace.
	// Route draft MRs must use a disjoint branch namespace so config-sync never
	// mistakes their JSON proposals for importable promotions.
	ConfigPromotionBranchPrefix string
	// The route draft uses this existing GitOps project with a distinct target.
	ConfigPromotionProjectID int64
	// ConfigPromotionTargetBranch is the target branch scanned by config-sync.
	// Route draft MRs use a separate target to avoid filling its global MR list.
	ConfigPromotionTargetBranch string
	Catalog                     Catalog
	Access                      Access
	// EnvironmentLabels maps a V3 environment to the Alertmanager label used
	// for its exact scope. Values come from trusted server configuration.
	EnvironmentLabels map[string]string
	// ApplicationBindings maps environment -> exact Alertmanager application
	// label value -> persisted application ID. A catalog key is mutable and
	// cannot establish ownership of a silence by itself. Only explicit,
	// server-owned bindings can attribute an exact application silence.
	ApplicationBindings map[string]map[string]string
	Now                 func() time.Time
}

type silenceView struct {
	ID       string              `json:"id"`
	Matchers []map[string]string `json:"matchers"`
	State    string              `json:"state"`
	// Window is display text in the silence's own offset; StartsAt/EndsAt
	// are RFC 3339 so clients can show the viewer's time zone (PA-37).
	Window    string         `json:"window"`
	StartsAt  string         `json:"starts_at"`
	EndsAt    string         `json:"ends_at"`
	Remaining *remainingView `json:"remaining,omitempty"`
	Affects   int            `json:"affects"`
	// Creator is the platform user's display name when Alertmanager's
	// createdBy is a known user ID, otherwise the upstream value.
	Creator string `json:"creator"`
	Reason  string `json:"reason"`
}

// remainingView.Pct is the share of the window still remaining (0..100).
type remainingView struct {
	Pct  int    `json:"pct"`
	Text string `json:"text"`
}

// creatorNameCatalog resolves platform user IDs to display names for the
// silence creator column. It is optional; unknown IDs keep the raw value.
type creatorNameCatalog interface {
	UserNames(context.Context, []string) (map[string]string, error)
}

func (s *Service) Register(api *httpapi.Server) {
	api.Register("GET /api/v1/silences", "viewer", s.List)
	api.MarkV3ObjectRoute("GET /api/v1/silences", "silences", "view")
	api.Register("POST /api/v1/silences", "operator", s.CreateSilenceProduct)
	api.MarkV3ObjectRoute("POST /api/v1/silences", "silences", "manage")
	api.Register("DELETE /api/v1/silences/{id}", "operator", s.CancelSilenceProduct)
	api.MarkV3ObjectRoute("DELETE /api/v1/silences/{id}", "silences", "manage")
	api.Register("POST /api/v1/silences/preview", "viewer", s.Preview)
	api.MarkV3ObjectRoute("POST /api/v1/silences/preview", "silences", "view")
	api.Register("GET /api/v1/inhibitions", "viewer", s.Inhibitions)
	api.MarkV3ObjectRoute("GET /api/v1/inhibitions", "silences", "view")
	api.Register("POST /api/v1/inhibitions/preview", "viewer", s.PreviewInhibition)
	api.MarkV3Route("POST /api/v1/inhibitions/preview", "silences", "view")
	api.Register("POST /api/v1/inhibitions", "operator", s.CreateInhibition)
	api.MarkV3Route("POST /api/v1/inhibitions", "silences", "manage")
	api.Register("POST /api/v1/notification-routes/test", "viewer", s.TestNotificationRoute)
	api.MarkV3Route("POST /api/v1/notification-routes/test", "alert_rules", "manage")
	api.Register("GET /api/v1/notification-routes", "viewer", s.NotificationRoutes)
	api.MarkV3Route("GET /api/v1/notification-routes", "alert_rules", "view")
	api.Register("POST /api/v1/notification-routes", "operator", s.CreateNotificationRoute)
	api.MarkV3Route("POST /api/v1/notification-routes", "alert_rules", "manage")
}

func (s *Service) List(w http.ResponseWriter, r *http.Request) {
	filter := "active"
	if values, present := r.URL.Query()["state"]; present {
		if len(values) != 1 || (values[0] != "active" && values[0] != "planned" && values[0] != "expired" && values[0] != "all") {
			httpapi.Error(w, http.StatusUnprocessableEntity, "invalid_state", "Select one known silence state")
			return
		}
		filter = values[0]
	}
	scope, ok := httpapi.V3ScopeFromContext(r.Context())
	if !ok || len(scope.EnvironmentIDs) == 0 {
		httpapi.Error(w, http.StatusServiceUnavailable, "environment_scope_unavailable", "Environment scope is unavailable")
		return
	}
	if s == nil || s.Source == nil {
		// No Alertmanager is a deployment state: the list is unknown.
		httpapi.WriteJSON(w, http.StatusOK, map[string]any{"items": nil, "counts": nil, "partial": false,
			"unavailable_environments": []string{}, "unscoped_present": false, "empty_reason": nil,
			"unavailable": map[string]string{"items": "not_configured", "counts": "not_configured"}})
		return
	}
	if s.Catalog == nil || s.Access == nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "silence_scope_unavailable", "Silence authorization is unavailable")
		return
	}
	apps, err := s.Catalog.Applications(r.Context(), scope.EnvironmentIDs)
	if err != nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "silence_catalog_unavailable", "Application catalog is unavailable")
		return
	}
	user := httpapi.CurrentUser(r)
	currentApps := make(map[string]map[string]Application)
	for _, app := range apps {
		if app.ID == "" || !containsSilenceEnvironment(scope.EnvironmentIDs, app.EnvironmentID) {
			httpapi.Error(w, http.StatusServiceUnavailable, "silence_catalog_unavailable", "Application catalog contains invalid ownership")
			return
		}
		if currentApps[app.EnvironmentID] == nil {
			currentApps[app.EnvironmentID] = make(map[string]Application)
		}
		if _, duplicate := currentApps[app.EnvironmentID][app.ID]; duplicate {
			httpapi.Error(w, http.StatusServiceUnavailable, "silence_catalog_unavailable", "Application identity is not unique in its environment")
			return
		}
		currentApps[app.EnvironmentID][app.ID] = app
	}
	allowedApps := make(map[string]map[string]bool)
	for _, env := range scope.EnvironmentIDs {
		allowedApps[env] = make(map[string]bool)
		for labelValue, applicationID := range s.ApplicationBindings[env] {
			if labelValue == "" || applicationID == "" {
				continue
			}
			app, active := currentApps[env][applicationID]
			if !active {
				// An old binding must not transfer to a newly created app
				// just because the provider label has been reused.
				continue
			}
			decision := rbac.V3Decision{Module: "silences", Action: "view", EnvironmentID: app.EnvironmentID, ApplicationID: app.ID}
			allowed, authErr := silenceAllowed(r.Context(), s.Access, user, decision)
			if authErr != nil {
				httpapi.Error(w, http.StatusServiceUnavailable, "silence_authorization_unavailable", "Silence authorization is unavailable")
				return
			}
			if !allowed && app.OwnerTeamID != "" {
				decision.ApplicationID = ""
				decision.TeamID = app.OwnerTeamID
				allowed, authErr = silenceAllowed(r.Context(), s.Access, user, decision)
				if authErr != nil {
					httpapi.Error(w, http.StatusServiceUnavailable, "silence_authorization_unavailable", "Silence authorization is unavailable")
					return
				}
			}
			allowedApps[env][labelValue] = allowed
		}
	}
	admin := make(map[string]bool)
	readable := make(map[string]bool)
	for _, env := range scope.EnvironmentIDs {
		broadAllowed, authErr := silenceAllowed(r.Context(), s.Access, user,
			rbac.V3Decision{Module: "silences", Action: "view", EnvironmentID: env})
		if authErr != nil {
			httpapi.Error(w, http.StatusServiceUnavailable, "silence_authorization_unavailable", "Silence authorization is unavailable")
			return
		}
		grant, catalogErr := s.Catalog.EnvironmentAdmin(r.Context(), user, env)
		if catalogErr != nil {
			httpapi.Error(w, http.StatusServiceUnavailable, "silence_catalog_unavailable", "Silence grants are unavailable")
			return
		}
		admin[env] = broadAllowed && grant
		readable[env] = admin[env]
		for _, allowed := range allowedApps[env] {
			readable[env] = readable[env] || allowed
		}
	}
	anyReadable := false
	for _, canRead := range readable {
		anyReadable = anyReadable || canRead
	}
	if !anyReadable {
		// Nothing readable before any upstream read: a known empty scope.
		httpapi.WriteJSON(w, http.StatusOK, map[string]any{"items": []silenceView{},
			"counts": map[string]int{"active": 0, "planned": 0, "expired": 0}, "partial": false,
			"unavailable_environments": []string{}, "unscoped_present": false,
			"empty_reason": "no_authorized_target", "unavailable": map[string]string{}})
		return
	}
	now := time.Now()
	if s.Now != nil {
		now = s.Now()
	}
	all := []silenceView{}
	seenVisibleIDs := make(map[string]bool)
	unscopedPresent := false
	for _, env := range scope.EnvironmentIDs {
		if !readable[env] {
			continue
		}
		label := s.EnvironmentLabels[env]
		if label == "" {
			httpapi.Error(w, http.StatusServiceUnavailable, "alertmanager_unavailable", "An authorized Alertmanager source is unavailable")
			return
		}
		silences, err := s.Source.ListSilences(r.Context(), env)
		if err != nil {
			httpapi.Error(w, http.StatusServiceUnavailable, "alertmanager_unavailable", "An authorized Alertmanager source is unavailable")
			return
		}
		type visibleSilence struct {
			record         Silence
			applicationKey string
		}
		scoped := make([]visibleSilence, 0, len(silences))
		for _, silence := range silences {
			switch classifySilence(silence, label, env, s.EnvironmentLabels) {
			case scopeOwn:
				appKey, exact, ambiguous := exactSilenceApplication(silence)
				if exact && allowedApps[env][appKey] {
					scoped = append(scoped, visibleSilence{silence, appKey})
				} else if !exact && !ambiguous && admin[env] {
					scoped = append(scoped, visibleSilence{record: silence})
				}
			case scopeBroad:
				if admin[env] {
					unscopedPresent = true
				}
			}
		}
		alerts := []Alert{}
		if len(all)+len(scoped) > maxRecords {
			httpapi.Error(w, http.StatusServiceUnavailable, "silence_limit", "Silence result exceeds limit")
			return
		}
		visibleKeys := make(map[string]string, len(scoped))
		for _, visible := range scoped {
			if seenVisibleIDs[visible.record.ID] {
				httpapi.Error(w, http.StatusServiceUnavailable, "alertmanager_invalid", "Alertmanager returned a duplicate visible silence")
				return
			}
			seenVisibleIDs[visible.record.ID] = true
			visibleKeys[visible.record.ID] = visible.applicationKey
		}
		if len(scoped) > 0 {
			alerts, err = s.Source.ListAlerts(r.Context(), env)
			if err != nil {
				httpapi.Error(w, http.StatusServiceUnavailable, "alertmanager_unavailable", "An authorized Alertmanager source is unavailable")
				return
			}
		}
		affects := countVisibleSilencedAlerts(alerts, label, env, visibleKeys)
		for _, visible := range scoped {
			all = append(all, projectSilence(visible.record, affects[visible.record.ID], now, r.Header.Get("Accept-Language")))
		}
	}
	counts := map[string]int{"active": 0, "planned": 0, "expired": 0}
	items := []silenceView{}
	for _, item := range all {
		counts[item.State]++
		if filter == "all" || filter == item.State {
			items = append(items, item)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	if names, ok := s.Catalog.(creatorNameCatalog); ok && len(items) > 0 {
		ids := make([]string, 0, len(items))
		for _, item := range items {
			ids = append(ids, item.Creator)
		}
		if resolved, nameErr := names.UserNames(r.Context(), ids); nameErr == nil {
			for i := range items {
				if name := resolved[items[i].Creator]; name != "" {
					items[i].Creator = name
				}
			}
		}
	}
	status := http.StatusOK
	if unscopedPresent {
		status = http.StatusPartialContent
	}
	// A foreign exact-environment record never changes the response status or
	// coverage flag. Truly broad records only set a boolean, never a count.
	httpapi.WriteJSON(w, status, map[string]any{"items": items, "counts": counts, "partial": status == http.StatusPartialContent,
		"unavailable_environments": []string{}, "unscoped_present": unscopedPresent, "empty_reason": nil,
		"unavailable": map[string]string{}})
}

func containsSilenceEnvironment(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func silenceAllowed(ctx context.Context, access Access, user core.User, decision rbac.V3Decision) (bool, error) {
	err := access.Authorize(ctx, user, decision)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, store.ErrForbidden) {
		return false, nil
	}
	return false, err
}

// A unique, positive equality matcher is the only trusted application link.
// Once an exact application is present, any other matcher on that label makes
// ownership ambiguous, including a regex or negative matcher.
func exactSilenceApplication(s Silence) (id string, exact bool, ambiguous bool) {
	otherConstraint := false
	for _, matcher := range s.Matchers {
		if matcher.Name != "application" {
			continue
		}
		if !matcher.IsEqual || matcher.IsRegex || matcher.Value == "" {
			otherConstraint = true
			continue
		}
		if id != "" && id != matcher.Value {
			return "", false, true
		}
		id = matcher.Value
	}
	if id != "" && otherConstraint {
		return "", false, true
	}
	return id, id != "", false
}

type recordScope uint8

const (
	scopeBroad recordScope = iota
	scopeOwn
	scopeOther
)

func classifySilence(s Silence, label, env string, labels map[string]string) recordScope {
	own := false
	other := false
	mixed := false
	for _, matcher := range s.Matchers {
		if matcher.Name == label {
			switch {
			case !matcher.IsEqual || matcher.IsRegex:
				mixed = true
			case matcher.Value == env:
				own = true
			default:
				other = true
			}
		}
		for otherEnv, otherLabel := range labels {
			if otherEnv != env && matcher.Name == otherLabel {
				if matcher.IsEqual && !matcher.IsRegex && matcher.Value == otherEnv {
					other = true
				} else if otherLabel != label {
					mixed = true
				}
			}
		}
	}
	if other || (own && mixed) {
		return scopeOther
	}
	if own {
		return scopeOwn
	}
	return scopeBroad
}

// Scan the shared upstream once. A visible app-owned silence counts only
// alerts with its exact application key; environment-wide admin records count
// all alerts in the selected environment. Hidden silence IDs never enter the
// aggregation map.
func countVisibleSilencedAlerts(alerts []Alert, label, env string, visibleKeys map[string]string) map[string]int {
	counts := make(map[string]int)
	for _, alert := range alerts {
		if alert.Labels[label] != env {
			continue
		}
		seen := make(map[string]bool, len(alert.Status.SilencedBy))
		for _, id := range alert.Status.SilencedBy {
			key, visible := visibleKeys[id]
			if !visible || seen[id] || (key != "" && alert.Labels["application"] != key) {
				continue
			}
			counts[id]++
			seen[id] = true
		}
	}
	return counts
}

func projectSilence(s Silence, affects int, now time.Time, language string) silenceView {
	state := s.Status.State
	if state == "pending" {
		state = "planned"
	}
	matchers := make([]map[string]string, 0, len(s.Matchers))
	for _, matcher := range s.Matchers {
		value := matcher.Value
		if matcher.IsRegex {
			value = "~ " + value
		} else if !matcher.IsEqual {
			value = "! " + value
		}
		matchers = append(matchers, map[string]string{"k": matcher.Name, "v": value})
	}
	view := silenceView{ID: s.ID, Matchers: matchers, State: state,
		Window:   s.StartsAt.Format("2006-01-02 15:04 -07:00") + " – " + s.EndsAt.Format("2006-01-02 15:04 -07:00"),
		StartsAt: s.StartsAt.UTC().Format(time.RFC3339), EndsAt: s.EndsAt.UTC().Format(time.RFC3339),
		Creator: s.CreatedBy, Reason: s.Comment}
	if state != "active" {
		return view
	}
	view.Affects = affects
	duration := s.EndsAt.Sub(s.StartsAt)
	if duration > 0 {
		pct := int(math.Round(100 * s.EndsAt.Sub(now).Seconds() / duration.Seconds()))
		if pct < 0 {
			pct = 0
		} else if pct > 100 {
			pct = 100
		}
		left := s.EndsAt.Sub(now)
		if left < 0 {
			left = 0
		}
		en := strings.HasPrefix(strings.ToLower(language), "en")
		text := "不到 1 分钟"
		if en {
			text = "< 1 m"
		}
		minutes := int(left.Minutes())
		if minutes >= 60 {
			if en {
				text = fmtDurationEN(minutes)
			} else {
				text = fmtDurationZH(minutes)
			}
		} else if minutes > 0 {
			if en {
				text = fmtInt(minutes) + " m"
			} else {
				text = fmtInt(minutes) + " 分"
			}
		}
		view.Remaining = &remainingView{Pct: pct, Text: text}
	}
	return view
}

func fmtDurationEN(minutes int) string { return fmtInt(minutes/60) + " h " + fmtInt(minutes%60) + " m" }
func fmtDurationZH(minutes int) string {
	return fmtInt(minutes/60) + " 小时 " + fmtInt(minutes%60) + " 分"
}
func fmtInt(n int) string { return strconv.Itoa(n) }
