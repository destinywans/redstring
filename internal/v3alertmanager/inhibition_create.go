package v3alertmanager

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/store"
)

var (
	errInhibitionDraftBusy      = errors.New("inhibition draft export is in progress")
	errInhibitionDraftUntrusted = errors.New("inhibition draft MR identity is untrusted")
)

type inhibitionDraftManifest struct {
	Schema           string               `json:"schema"`
	DraftID          string               `json:"draft_id"`
	EnvironmentID    string               `json:"environment_id"`
	ApplicationID    string               `json:"application_id"`
	ApplicationLabel string               `json:"application_label"`
	SourceRevision   string               `json:"source_revision"`
	Status           string               `json:"status"`
	Promotion        string               `json:"promotion"`
	EffectiveSource  []routeDraftMatcher  `json:"effective_source"`
	EffectiveTarget  []routeDraftMatcher  `json:"effective_target"`
	Input            inhibitionDraftInput `json:"input"`
}

type inhibitionDraftRecord struct {
	ID, Branch, TargetBranch, Path, State, URL, SourceRevision string
	Payload                                                    []byte
	MR, ProjectID, Attempt                                     int64
}

type inhibitionDraftResponse struct {
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

// CreateInhibition persists an immutable, versioned proposal, then opens a
// review MR. It never writes Alertmanager, merges, reloads, or sends notices.
func (s *Service) CreateInhibition(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	scope, ok := httpapi.V3ScopeFromContext(r.Context())
	if !ok || scope.All || len(scope.EnvironmentIDs) != 1 {
		httpapi.Error(w, http.StatusConflict, "single_environment_required", "Select one environment to save an inhibition draft")
		return
	}
	env := scope.EnvironmentIDs[0]
	if status, err := s.inhibitionManager(r, env); err != nil {
		httpapi.Error(w, status, "inhibition_scope_unavailable", "Inhibition access is unavailable or denied")
		return
	}
	db, txAccess := routeTestAuthority(s.Catalog, s.Access)
	if db == nil || txAccess == nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "inhibition_scope_unavailable", "Inhibition authorization snapshot is unavailable")
		return
	}
	if !s.inhibitionRepositoryReady() {
		httpapi.Error(w, http.StatusNotImplemented, "not_connected", "A reviewed GitOps inhibition repository is not connected")
		return
	}
	source, connected := s.Source.(inhibitionObservationSource)
	client, isClient := s.Source.(*Client)
	envLabel := s.EnvironmentLabels[env]
	if !connected || source == nil || isClient && client == nil ||
		envLabel == "" || envLabel == "application" || !policyLabelName.MatchString(envLabel) {
		httpapi.Error(w, http.StatusNotImplemented, "not_connected", "An exact Alertmanager inhibition source is not connected")
		return
	}
	var input inhibitionDraftInput
	if !httpapi.Decode(w, r, &input) {
		return
	}
	key := r.Header.Get("Idempotency-Key")
	revision := strings.TrimSpace(r.Header.Get("If-Match"))
	if len(r.Header.Values("Idempotency-Key")) != 1 || len(r.Header.Values("If-Match")) != 1 ||
		!routeDraftKeyRE.MatchString(key) || len(revision) < 2 || revision[0] != '"' || revision[len(revision)-1] != '"' {
		httpapi.Error(w, http.StatusUnprocessableEntity, "inhibition_precondition_required", "Provide Idempotency-Key and quoted inhibition config ETag")
		return
	}
	revision = revision[1 : len(revision)-1]
	if !routeDraftRevisionRE.MatchString(revision) {
		httpapi.Error(w, http.StatusUnprocessableEntity, "invalid_inhibition_revision", "Provide the current inhibition config revision")
		return
	}
	appLabel, app, status, err := s.routeDraftApplication(r.Context(), env, input.ApplicationID)
	if err != nil {
		httpapi.Error(w, status, "application_not_found", "Bound application is unavailable")
		return
	}
	compiled, err := compileInhibitionDraft(input, envLabel, env, appLabel)
	if err != nil {
		httpapi.Error(w, http.StatusUnprocessableEntity, "invalid_inhibition_draft", "Provide bounded source, non-P1 target, and equal matchers")
		return
	}
	requestHash := inhibitionDraftRequestHash(env, revision, input)
	record, found, err := s.claimInhibitionDraft(r, db, txAccess, env, app, appLabel, key, requestHash, revision, nil, "")
	if err != nil {
		writeInhibitionDraftError(w, err)
		return
	}
	if found {
		s.exportInhibitionDraft(w, r, record, db, txAccess, env, app)
		return
	}
	observation, err := source.ObserveInhibitions(r.Context(), env)
	if err != nil {
		writeInhibitionSourceError(w, err)
		return
	}
	if "sha256:"+observation.ConfigSHA256 != revision || observation.ObservedAt.IsZero() {
		httpapi.Error(w, http.StatusConflict, "inhibition_version_conflict", "Inhibition configuration changed; refresh the rule list")
		return
	}
	if s.EnvironmentLabels[env] != envLabel || s.ApplicationBindings[env][appLabel] != app.ID {
		httpapi.Error(w, http.StatusServiceUnavailable, "inhibition_scope_changed", "Inhibition scope changed during configuration read")
		return
	}
	id := store.ID()
	manifest := inhibitionDraftManifest{
		Schema: "aiops.inhibition-draft/v1", DraftID: id, EnvironmentID: env, ApplicationID: app.ID,
		ApplicationLabel: appLabel, SourceRevision: revision, Status: "pending_review",
		Promotion: "manual_review_and_separate_activation_required", EffectiveSource: compiled.effectiveSource,
		EffectiveTarget: compiled.effectiveTarget, Input: input,
	}
	payload, err := json.Marshal(manifest)
	if err != nil || len(payload) > 64<<10 {
		httpapi.Error(w, http.StatusUnprocessableEntity, "invalid_inhibition_draft", "Inhibition draft is too large")
		return
	}
	record, _, err = s.claimInhibitionDraft(r, db, txAccess, env, app, appLabel, key, requestHash, revision, payload, id)
	if err != nil {
		writeInhibitionDraftError(w, err)
		return
	}
	s.exportInhibitionDraft(w, r, record, db, txAccess, env, app)
}

func (s *Service) inhibitionRepositoryReady() bool {
	r := s.InhibitionRepository
	gitLabRepository, isGitLabRepository := r.(*RouteGitLabRepository)
	if r == nil || isGitLabRepository && gitLabRepository == nil ||
		r.ProjectID() <= 0 || r.ProjectID() != s.ConfigPromotionProjectID ||
		r.BranchPrefix() == "" || r.TargetBranch() == "" ||
		s.ConfigPromotionBranchPrefix == "" || s.ConfigPromotionTargetBranch == "" ||
		r.TargetBranch() == s.ConfigPromotionTargetBranch ||
		strings.HasPrefix(r.BranchPrefix(), s.ConfigPromotionBranchPrefix) ||
		strings.HasPrefix(s.ConfigPromotionBranchPrefix, r.BranchPrefix()) {
		return false
	}
	if route := s.RouteRepository; route != nil {
		if concrete, isGitLab := route.(*RouteGitLabRepository); isGitLab && concrete == nil {
			return false
		}
		if r.TargetBranch() == route.TargetBranch() ||
			strings.HasPrefix(r.BranchPrefix(), route.BranchPrefix()) ||
			strings.HasPrefix(route.BranchPrefix(), r.BranchPrefix()) {
			return false
		}
	}
	return true
}

func inhibitionDraftRequestHash(env, revision string, input inhibitionDraftInput) string {
	data, _ := json.Marshal(struct {
		Environment string               `json:"environment"`
		Revision    string               `json:"revision"`
		Input       inhibitionDraftInput `json:"input"`
	}{env, revision, input})
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}
