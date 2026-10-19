package v3alertmanager

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/store"
)

var (
	routeDraftKeyRE        = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{7,127}$`)
	routeDraftRevisionRE   = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
	routeDraftReceiverRE   = regexp.MustCompile(`^receiver-[1-9][0-9]{0,3}$`)
	routeDurationPartRE    = regexp.MustCompile(`([0-9]+)(ms|s|m|h|d|w|y)`)
	errRouteDraftBusy      = errors.New("route draft export is in progress")
	errRouteDraftUntrusted = errors.New("route draft MR identity is untrusted")
)

type routeDraftInput struct {
	ApplicationID  string              `json:"application_id"`
	Name           string              `json:"name"`
	Matchers       []routeDraftMatcher `json:"matchers"`
	ReceiverRef    string              `json:"receiver_ref"`
	GroupBy        []string            `json:"group_by"`
	GroupWait      string              `json:"group_wait"`
	GroupInterval  string              `json:"group_interval"`
	RepeatInterval string              `json:"repeat_interval"`
	Continue       bool                `json:"continue"`
}

type routeDraftMatcher struct {
	K  string `json:"k"`
	Op string `json:"op"`
	V  string `json:"v"`
}

type routeDraftManifest struct {
	Schema             string              `json:"schema"`
	DraftID            string              `json:"draft_id"`
	EnvironmentID      string              `json:"environment_id"`
	ApplicationID      string              `json:"application_id"`
	ApplicationLabel   string              `json:"application_label"`
	SourceRevision     string              `json:"source_revision"`
	Status             string              `json:"status"`
	ParentRef          string              `json:"parent_ref"`
	Placement          string              `json:"placement"`
	ReceiverResolution string              `json:"receiver_resolution"`
	EffectiveMatchers  []routeDraftMatcher `json:"effective_matchers"`
	Route              routeDraftInput     `json:"route"`
}

type routeDraftRecord struct {
	ID, Branch, TargetBranch, Path, State, URL, SourceRevision string
	Payload                                                    []byte
	MR, ProjectID, Attempt                                     int64
}

type routeDraftResponse struct {
	ID             string `json:"id"`
	Status         string `json:"status"`
	StatusBasis    string `json:"status_basis"`
	Effect         string `json:"effect"`
	CurrentMRState any    `json:"current_mr_state"`
	RuntimeState   any    `json:"runtime_state"`
	SourceRevision string `json:"source_revision"`
	Review         struct {
		ProjectID       int64  `json:"project_id"`
		MergeRequestIID int64  `json:"merge_request_iid"`
		URL             string `json:"url"`
	} `json:"review"`
}

// CreateNotificationRoute saves a versioned draft in a reviewed GitLab MR.
// The JSON is a proposal for a separate config owner; it is never imported,
// merged, installed, reloaded, or considered active by this endpoint.
func (s *Service) CreateNotificationRoute(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	scope, ok := httpapi.V3ScopeFromContext(r.Context())
	if !ok || scope.All || len(scope.EnvironmentIDs) != 1 {
		httpapi.Error(w, http.StatusConflict, "single_environment_required", "Select one environment to save routing")
		return
	}
	if s == nil || s.Access == nil || s.Catalog == nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "route_scope_unavailable", "Routing authorization is unavailable")
		return
	}
	env := scope.EnvironmentIDs[0]
	if status, err := s.routeTestManager(r.Context(), httpapi.CurrentUser(r), env); err != nil {
		httpapi.Error(w, status, routeTestErrorCode(status), "Routing access is unavailable or denied")
		return
	}
	db, txAccess := routeTestAuthority(s.Catalog, s.Access)
	if db == nil || txAccess == nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "route_scope_unavailable", "Routing authorization snapshot is unavailable")
		return
	}
	if !s.routeRepositoryReady() {
		httpapi.Error(w, http.StatusNotImplemented, "not_connected", "A reviewed GitOps route repository is not connected")
		return
	}
	if !s.routeSourceReady(env) {
		httpapi.Error(w, http.StatusNotImplemented, "not_connected", "An exact Alertmanager route source is not connected")
		return
	}
	envLabel := s.EnvironmentLabels[env]
	source := s.Source.(routeTreeSource)
	var input routeDraftInput
	if !httpapi.Decode(w, r, &input) {
		return
	}
	key := r.Header.Get("Idempotency-Key")
	revision := strings.TrimSpace(r.Header.Get("If-Match"))
	if len(r.Header.Values("Idempotency-Key")) != 1 || len(r.Header.Values("If-Match")) != 1 ||
		!routeDraftKeyRE.MatchString(key) || len(revision) < 2 || revision[0] != '"' || revision[len(revision)-1] != '"' {
		httpapi.Error(w, http.StatusUnprocessableEntity, "route_precondition_required", "Provide Idempotency-Key and the quoted route tree ETag")
		return
	}
	revision = revision[1 : len(revision)-1]
	if !routeDraftRevisionRE.MatchString(revision) || !validRouteDraftInput(input, envLabel) {
		httpapi.Error(w, http.StatusUnprocessableEntity, "invalid_route_draft", "Provide a bounded application route, existing receiver reference and valid timing")
		return
	}
	providerApp, app, status, err := s.routeDraftApplication(r.Context(), env, input.ApplicationID)
	if err != nil {
		httpapi.Error(w, status, routeTestErrorCode(status), "Bound application is unavailable")
		return
	}
	requestHash := routeDraftRequestHash(env, revision, input)
	// Existing identities can be reconciled with their original immutable
	// payload even when the live source changed after reservation.
	record, found, err := s.claimRouteDraft(r, db, txAccess, env, app, providerApp, key, requestHash, revision, nil, "")
	if err != nil {
		writeRouteDraftError(w, err)
		return
	}
	if found {
		s.exportRouteDraft(w, r, record, db, txAccess, env, app)
		return
	}
	observation, err := source.ObserveRouteTree(r.Context(), env, envLabel)
	if err != nil {
		if errors.Is(err, ErrNotConfigured) {
			httpapi.Error(w, http.StatusNotImplemented, "not_connected", "An exact Alertmanager route source is not connected")
		} else {
			httpapi.Error(w, http.StatusServiceUnavailable, "route_source_unavailable", "Current route tree cannot be scoped exactly")
		}
		return
	}
	if "sha256:"+observation.ConfigSHA256 != revision {
		httpapi.Error(w, http.StatusConflict, "route_version_conflict", "Route configuration changed; refresh the tree")
		return
	}
	if !routeTreeHasReceiver(observation.Root, input.ReceiverRef) {
		httpapi.Error(w, http.StatusUnprocessableEntity, "receiver_not_found", "Select a receiver reference from the current route tree")
		return
	}
	if s.EnvironmentLabels[env] != envLabel || s.ApplicationBindings[env][providerApp] != app.ID {
		httpapi.Error(w, http.StatusServiceUnavailable, "route_scope_changed", "Routing scope changed during source read")
		return
	}
	id := store.ID()
	manifest := routeDraftManifest{Schema: "aiops.notification-route-draft/v1", DraftID: id,
		EnvironmentID: env, ApplicationID: app.ID, ApplicationLabel: providerApp,
		SourceRevision: revision, Status: "pending_review", ParentRef: observation.Root.ID,
		Placement:          "manual_order_review_required",
		ReceiverResolution: "environment_tree_unique_preorder_v1",
		EffectiveMatchers:  append([]routeDraftMatcher{{K: envLabel, Op: "=", V: env}, {K: "application", Op: "=", V: providerApp}}, input.Matchers...),
		Route:              input}
	payload, err := json.Marshal(manifest)
	if err != nil || len(payload) > 64<<10 {
		httpapi.Error(w, http.StatusUnprocessableEntity, "invalid_route_draft", "Route draft is too large")
		return
	}
	record, _, err = s.claimRouteDraft(r, db, txAccess, env, app, providerApp, key, requestHash, revision, payload, id)
	if err != nil {
		writeRouteDraftError(w, err)
		return
	}
	s.exportRouteDraft(w, r, record, db, txAccess, env, app)
}

func routeDraftRequestHash(env, revision string, input routeDraftInput) string {
	requestBytes, _ := json.Marshal(struct {
		Environment string          `json:"environment"`
		Revision    string          `json:"revision"`
		Input       routeDraftInput `json:"input"`
	}{env, revision, input})
	requestDigest := sha256.Sum256(requestBytes)
	return hex.EncodeToString(requestDigest[:])
}

func validRouteDraftInput(input routeDraftInput, envLabel string) bool {
	if input.ApplicationID == "" || len(input.ApplicationID) > 128 ||
		strings.TrimSpace(input.Name) != input.Name || input.Name == "" || len(input.Name) > 120 ||
		strings.ContainsAny(input.Name, "\x00\r\n") || input.Matchers == nil || len(input.Matchers) > 20 ||
		!routeDraftReceiverRE.MatchString(input.ReceiverRef) ||
		len(input.GroupBy) == 0 || len(input.GroupBy) > 20 {
		return false
	}
	groupBySeen := make(map[string]bool)
	for _, field := range input.GroupBy {
		if field != "..." && !policyLabelName.MatchString(field) || groupBySeen[field] || field == "..." && len(input.GroupBy) != 1 {
			return false
		}
		groupBySeen[field] = true
	}
	matcherSeen := make(map[string]bool)
	for _, matcher := range input.Matchers {
		if matcher.K == envLabel || matcher.K == "application" || len(matcher.V) > 256 ||
			strings.ContainsAny(matcher.V, "\x00\r\n") {
			return false
		}
		if _, err := newPolicyMatcher(matcher.K, matcher.Op, matcher.V); err != nil {
			return false
		}
		if matcherSeen[matcher.K] {
			return false
		}
		matcherSeen[matcher.K] = true
	}
	for index, value := range []string{input.GroupWait, input.GroupInterval, input.RepeatInterval} {
		if len(value) == 0 || len(value) > 32 {
			return false
		}
		duration, ok := routeDraftDuration(value)
		if !ok || index != 0 && duration == 0 {
			return false
		}
	}
	return true
}

func routeDraftDuration(value string) (time.Duration, bool) {
	if !policyDuration.MatchString(value) {
		return 0, false
	}
	if value == "0" {
		return 0, true
	}
	const limit = 30 * 24 * time.Hour
	units := map[string]time.Duration{
		"ms": time.Millisecond, "s": time.Second, "m": time.Minute,
		"h": time.Hour, "d": 24 * time.Hour, "w": 7 * 24 * time.Hour,
		"y": 365 * 24 * time.Hour,
	}
	var total time.Duration
	for _, part := range routeDurationPartRE.FindAllStringSubmatch(value, -1) {
		amount, err := strconv.ParseInt(part[1], 10, 64)
		unit := units[part[2]]
		if err != nil || amount > int64((limit-total)/unit) {
			return 0, false
		}
		total += time.Duration(amount) * unit
	}
	return total, true
}

func routeTreeHasReceiver(node RouteTreeNode, ref string) bool {
	for _, candidate := range node.ReceiverRefs {
		if candidate == ref {
			return true
		}
	}
	for _, child := range node.Children {
		if routeTreeHasReceiver(child, ref) {
			return true
		}
	}
	return false
}

func (s *Service) routeDraftApplication(ctx context.Context, env, appID string) (string, Application, int, error) {
	providerApp := ""
	for label, boundID := range s.ApplicationBindings[env] {
		if boundID != appID {
			continue
		}
		if providerApp != "" || label == "" {
			return "", Application{}, http.StatusServiceUnavailable, errors.New("ambiguous application binding")
		}
		providerApp = label
	}
	if providerApp == "" {
		return "", Application{}, http.StatusNotFound, store.ErrNotFound
	}
	app, status, err := s.routeTestApplication(ctx, env, providerApp)
	return providerApp, app, status, err
}

// routeRepositoryReady says whether a reviewed GitOps route repository is configured,
// separate from the configuration-promotion branches, the first condition
// CreateNotificationRoute needs.
func (s *Service) routeRepositoryReady() bool {
	r := s.RouteRepository
	gitLabRepository, isGitLabRepository := r.(*RouteGitLabRepository)
	return r != nil && !(isGitLabRepository && gitLabRepository == nil) &&
		r.ProjectID() > 0 && r.ProjectID() == s.ConfigPromotionProjectID &&
		r.BranchPrefix() != "" && r.TargetBranch() != "" &&
		s.ConfigPromotionBranchPrefix != "" && s.ConfigPromotionTargetBranch != "" &&
		r.TargetBranch() != s.ConfigPromotionTargetBranch &&
		!strings.HasPrefix(r.BranchPrefix(), s.ConfigPromotionBranchPrefix) &&
		!strings.HasPrefix(s.ConfigPromotionBranchPrefix, r.BranchPrefix())
}

// routeSourceReady says whether env has an exact Alertmanager route source and
// environment label, the second condition CreateNotificationRoute needs.
func (s *Service) routeSourceReady(env string) bool {
	envLabel := s.EnvironmentLabels[env]
	source, ok := s.Source.(routeTreeSource)
	client, isClient := s.Source.(*Client)
	return envLabel != "" && envLabel != "application" && alertLabelName.MatchString(envLabel) &&
		ok && source != nil && !(isClient && client == nil)
}

// routeDraftsReady says whether CreateNotificationRoute can export a draft for env
// rather than answer 501 not_connected; GET /notification-routes names it in
// unavailable as route_drafts otherwise so the page can hold the entry (PA-141).
func (s *Service) routeDraftsReady(env string) bool {
	return s.routeRepositoryReady() && s.routeSourceReady(env)
}
