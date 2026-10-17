package v3alertmanager

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

// SilenceWorkflowReadiness is supplied by the approval/executor owner. It
// must only report ready when an independent, durable approval gate and an
// executor for the requested operation are configured. A proposal producer
// alone is not a working product flow.
type SilenceWorkflowReadiness interface {
	CreateReady(context.Context, string) error
	CancelReady(context.Context, string) error
}

// SilenceProductProposal is a pending review receipt, never an upstream
// silence ID or evidence that an action was approved or executed.
type SilenceProductProposal struct {
	ProposalID   string
	Status       string
	ActionDigest string
}

// SilenceCreateProposalProducer must atomically create the immutable action,
// review content, idempotency identity and audit. It must lock the exact
// session token hash in that transaction alongside current human identity,
// catalog ownership and V3 grants. The existing v3proposals producer supplies
// the proposal logic; WAN-117's adapter adds the session lock and readiness.
type SilenceCreateProposalProducer interface {
	SubmitSilenceCreate(context.Context, core.User, string, string, SilenceCreateInput) (SilenceProductProposal, error)
}

type SilenceCancelProposalProducer interface {
	SubmitSilenceCancel(context.Context, core.User, string, string, string, string, string, string) (SilenceProductProposal, error)
}

type silenceProductMatcher struct {
	Name  string `json:"name"`
	Op    string `json:"op"`
	Value string `json:"value"`
}

type silenceProductCreateInput struct {
	InvestigationID string                  `json:"investigation_id"`
	Matchers        []silenceProductMatcher `json:"matchers"`
	DurationSeconds int64                   `json:"duration_seconds"`
	Comment         string                  `json:"comment"`
}

type silenceProductCancelInput struct {
	InvestigationID string `json:"investigation_id"`
	Reason          string `json:"reason"`
}

func (s *Service) CreateSilenceProduct(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	env, sessionHash, ok := s.silenceProductRequest(w, r, true)
	if !ok {
		return
	}
	var body silenceProductCreateInput
	if !httpapi.Decode(w, r, &body) {
		return
	}
	key, ok := silenceProductKey(w, r)
	if !ok {
		return
	}
	if !validSilenceProductText(body.InvestigationID, 128) ||
		!validSilenceProductText(body.Comment, 1000) ||
		body.DurationSeconds < int64((30*time.Minute)/time.Second) ||
		body.DurationSeconds > int64((24*time.Hour)/time.Second) ||
		len(body.Matchers) == 0 || len(body.Matchers) > 20 {
		httpapi.Error(w, http.StatusUnprocessableEntity, "invalid_silence_request", "Provide an open investigation, bounded matchers, duration and comment")
		return
	}
	matchers := make([]Matcher, 0, len(body.Matchers))
	applicationExact := false
	for _, item := range body.Matchers {
		if !alertLabelName.MatchString(item.Name) || item.Value == "" || len(item.Value) > 256 ||
			strings.ContainsAny(item.Value, "\x00\r\n") {
			httpapi.Error(w, http.StatusUnprocessableEntity, "invalid_silence_matcher", "Silence matcher is invalid")
			return
		}
		matcher := Matcher{Name: item.Name, Value: item.Value}
		switch item.Op {
		case "=":
			matcher.IsEqual = true
		case "!=":
		case "=~":
			matcher.IsEqual, matcher.IsRegex = true, true
		case "!~":
			matcher.IsRegex = true
		default:
			httpapi.Error(w, http.StatusUnprocessableEntity, "invalid_silence_matcher", "Silence matcher operator is invalid")
			return
		}
		if item.Name == "application" {
			if item.Op != "=" || applicationExact {
				httpapi.Error(w, http.StatusUnprocessableEntity, "invalid_silence_scope", "Exactly one bound application matcher is required")
				return
			}
			applicationExact = true
		}
		matchers = append(matchers, matcher)
	}
	if !applicationExact {
		httpapi.Error(w, http.StatusUnprocessableEntity, "invalid_silence_scope", "Exactly one bound application matcher is required")
		return
	}
	result, err := s.SilenceCreateProposals.SubmitSilenceCreate(r.Context(), httpapi.CurrentUser(r), sessionHash,
		body.InvestigationID, SilenceCreateInput{EnvironmentID: env, IdempotencyKey: key,
			Comment: body.Comment, Matchers: matchers, Duration: time.Duration(body.DurationSeconds) * time.Second})
	writeSilenceProductResult(w, result, err)
}

func (s *Service) CancelSilenceProduct(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	env, sessionHash, ok := s.silenceProductRequest(w, r, false)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if !validSilenceID(id) {
		httpapi.Error(w, http.StatusNotFound, "silence_not_found", "Silence is unavailable")
		return
	}
	var body silenceProductCancelInput
	if !httpapi.Decode(w, r, &body) {
		return
	}
	key, ok := silenceProductKey(w, r)
	if !ok {
		return
	}
	if !validSilenceProductText(body.InvestigationID, 128) || !validSilenceProductText(body.Reason, 1000) {
		httpapi.Error(w, http.StatusUnprocessableEntity, "invalid_silence_cancel", "Provide an open investigation and cancellation reason")
		return
	}
	result, err := s.SilenceCancelProposals.SubmitSilenceCancel(r.Context(), httpapi.CurrentUser(r), sessionHash,
		env, id, body.InvestigationID, key, body.Reason)
	writeSilenceProductResult(w, result, err)
}

func (s *Service) silenceProductRequest(w http.ResponseWriter, r *http.Request, create bool) (string, string, bool) {
	scope, ok := httpapi.V3ScopeFromContext(r.Context())
	if !ok || scope.All || len(scope.EnvironmentIDs) != 1 {
		httpapi.Error(w, http.StatusConflict, "single_environment_required", "Select one environment for a silence proposal")
		return "", "", false
	}
	env := scope.EnvironmentIDs[0]
	// Refuse a caller who may write no silence here before saying which
	// workflow or write binding this environment lacks (PA-108).
	writer, err := s.silenceWriterInEnvironment(r.Context(), httpapi.CurrentUser(r), env)
	if err != nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "silence_authorization_unavailable", "Silence authorization is unavailable")
		return "", "", false
	}
	if !writer {
		httpapi.Error(w, http.StatusForbidden, "forbidden", "Silence access is denied")
		return "", "", false
	}
	if s.SilenceWorkflow == nil || s.Source == nil ||
		create && s.SilenceCreateProposals == nil || !create && s.SilenceCancelProposals == nil {
		httpapi.Error(w, http.StatusNotImplemented, "not_connected", "Silence approval and execution workflow is not connected")
		return "", "", false
	}
	client, isClient := s.Source.(*Client)
	if !isClient || client == nil {
		httpapi.Error(w, http.StatusNotImplemented, "not_connected", "A trusted Alertmanager write binding is not connected")
		return "", "", false
	}
	if create {
		err = s.SilenceWorkflow.CreateReady(r.Context(), env)
	} else {
		err = s.SilenceWorkflow.CancelReady(r.Context(), env)
	}
	if err != nil {
		if errors.Is(err, ErrNotConfigured) {
			httpapi.Error(w, http.StatusNotImplemented, "not_connected", "Silence approval and execution workflow is not connected")
		} else {
			httpapi.Error(w, http.StatusServiceUnavailable, "silence_workflow_unavailable", "Silence workflow is unavailable")
		}
		return "", "", false
	}
	cookie, err := r.Cookie("aiops_session")
	if err != nil || cookie.Value == "" {
		httpapi.Error(w, http.StatusUnauthorized, "unauthorized", "Current session is required")
		return "", "", false
	}
	return env, store.Hash(cookie.Value), true
}

// silenceWriterInEnvironment reports whether user may propose a silence for
// some target in env: the environment itself, or a currently bound
// application or its owning team. It only admits the request; the proposal
// producer still authorizes the exact target.
func (s *Service) silenceWriterInEnvironment(ctx context.Context, user core.User, env string) (bool, error) {
	if s == nil || s.Catalog == nil || s.Access == nil {
		return false, errors.New("silence authorization unavailable")
	}
	scopes := []rbac.V3Decision{{EnvironmentID: env}}
	bound := make(map[string]bool, len(s.ApplicationBindings[env]))
	for _, applicationID := range s.ApplicationBindings[env] {
		if applicationID != "" {
			bound[applicationID] = true
		}
	}
	if len(bound) > 0 {
		apps, err := s.Catalog.Applications(ctx, []string{env})
		if err != nil {
			return false, err
		}
		for _, app := range apps {
			if app.EnvironmentID != env || !bound[app.ID] {
				continue
			}
			scopes = append(scopes, rbac.V3Decision{EnvironmentID: env, ApplicationID: app.ID})
			if app.OwnerTeamID != "" {
				scopes = append(scopes, rbac.V3Decision{EnvironmentID: env, TeamID: app.OwnerTeamID})
			}
		}
	}
	for _, scope := range scopes {
		writer := true
		for _, need := range [][2]string{{"silences", "manage"}, {"proposals", "change"}} {
			decision := scope
			decision.Module, decision.Action = need[0], need[1]
			allowed, err := silenceAllowed(ctx, s.Access, user, decision)
			if err != nil {
				return false, err
			}
			if !allowed {
				writer = false
				break
			}
		}
		if writer {
			return true, nil
		}
	}
	return false, nil
}

func silenceProductKey(w http.ResponseWriter, r *http.Request) (string, bool) {
	key := r.Header.Get("Idempotency-Key")
	// Bulk group proposals use an internal key namespace with mandatory
	// companion evidence. An ordinary product request cannot claim it.
	if len(r.Header.Values("Idempotency-Key")) != 1 || !silenceKeyPattern.MatchString(key) ||
		strings.HasPrefix(key, "bulk-") {
		httpapi.Error(w, http.StatusUnprocessableEntity, "idempotency_key_required", "Provide one bounded Idempotency-Key")
		return "", false
	}
	return key, true
}

func validSilenceProductText(value string, max int) bool {
	return value != "" && strings.TrimSpace(value) == value && len(value) <= max &&
		!strings.ContainsAny(value, "\x00\r\n")
}

func writeSilenceProductResult(w http.ResponseWriter, result SilenceProductProposal, err error) {
	if err != nil {
		switch {
		case errors.Is(err, ErrNotConfigured):
			httpapi.Error(w, http.StatusNotImplemented, "not_connected", "Silence proposal workflow is not connected")
		case errors.Is(err, store.ErrSessionRevoked):
			httpapi.Error(w, http.StatusUnauthorized, "unauthorized", "Session expired")
		case errors.Is(err, store.ErrForbidden):
			httpapi.Error(w, http.StatusForbidden, "forbidden", "Silence access is denied")
		case errors.Is(err, store.ErrNotFound):
			httpapi.Error(w, http.StatusNotFound, "silence_not_found", "Silence or investigation is unavailable")
		case errors.Is(err, store.ErrConflict):
			httpapi.Error(w, http.StatusConflict, "silence_proposal_conflict", "Silence proposal identity or source changed")
		case errors.Is(err, ErrSilenceCreateInvalid):
			httpapi.Error(w, http.StatusUnprocessableEntity, "invalid_silence_request", "Silence proposal is invalid")
		default:
			httpapi.Error(w, http.StatusServiceUnavailable, "silence_proposal_unavailable", "Silence proposal could not be confirmed")
		}
		return
	}
	if result.ProposalID == "" || len(result.ActionDigest) != 64 {
		httpapi.Error(w, http.StatusServiceUnavailable, "silence_proposal_unavailable", "Silence proposal could not be confirmed")
		return
	}
	if result.Status != "pending" && result.Status != "pending_review" {
		httpapi.Error(w, http.StatusConflict, "silence_proposal_decided", "The proposal is no longer pending review")
		return
	}
	httpapi.WriteJSON(w, http.StatusAccepted, map[string]any{
		"proposal_id": result.ProposalID, "status": "pending_review", "action_digest": result.ActionDigest,
		"effect": "no_upstream_write", "upstream_id": nil, "execution_status": nil,
	})
}
