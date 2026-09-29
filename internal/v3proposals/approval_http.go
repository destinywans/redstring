package v3proposals

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/store"
	"github.com/wans/aiops-canvas/internal/v3passkey"
)

// ProposalPasskeys is a proposal-bound WebAuthn ceremony, never an account
// assertion receipt. The finish method verifies and consumes the challenge in
// the caller's approval transaction.
type ProposalPasskeys interface {
	CheckProposalWriteOrigin(*http.Request, string, bool) error
	BeginBoundProposalAssertion(context.Context, v3passkey.Actor, v3passkey.ProposalBinding) (v3passkey.Ceremony, error)
	VerifyProposalAssertionWithTx(context.Context, pgx.Tx, v3passkey.Actor,
		v3passkey.ProposalBinding, string, []byte) (v3passkey.ProposalProof, error)
}

// RegisterApprovals is called by WAN-117 only after the passkey service and
// approval migration are configured. The domain performs its own exact
// proposal authorization because the shared V3 matrix still deliberately
// denies generic approve decisions without a transaction-bound passkey.
func (s *Service) RegisterApprovals(api *httpapi.Server) {
	for _, route := range []struct {
		pattern string
		handler http.HandlerFunc
	}{
		{"POST /api/v1/proposals/{id}/approve/options", s.ApproveOptions},
		{"POST /api/v1/proposals/{id}/approve", s.Approve},
	} {
		handler := route.handler
		api.Register(route.pattern, "viewer", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			if s == nil || s.DB == nil || s.ApprovalPasskeys == nil {
				httpapi.Error(w, 503, "proposal_approval_unavailable", "Proposal approval is unavailable")
				return
			}
			if err := s.ApprovalPasskeys.CheckProposalWriteOrigin(r, api.AllowedOrigin, api.SecureCookies); err != nil {
				httpapi.Error(w, 403, "approval_origin_required", "Use the secure same-origin approval application")
				return
			}
			handler(w, r)
		})
		api.MarkV3ObjectRoute(route.pattern, "proposals", "approve")
	}
}

func approvalHTTPActor(w http.ResponseWriter, r *http.Request) (v3passkey.Actor, bool) {
	scope, ok := httpapi.V3ScopeFromContext(r.Context())
	if !ok || scope.All || len(scope.EnvironmentIDs) != 1 || scope.EnvironmentIDs[0] == "" {
		httpapi.Error(w, 409, "single_environment_required", "Select one environment to approve")
		return v3passkey.Actor{}, false
	}
	cookie, err := r.Cookie("aiops_session")
	if err != nil || cookie.Value == "" {
		httpapi.Error(w, 401, "unauthorized", "Current session is required")
		return v3passkey.Actor{}, false
	}
	return v3passkey.Actor{UserID: httpapi.CurrentUser(r).ID, SessionToken: cookie.Value,
		Tier: scope.Tier, EnvironmentID: scope.EnvironmentIDs[0]}, true
}

func (s *Service) ApproveOptions(w http.ResponseWriter, r *http.Request) {
	var in struct {
		// A JSON object is required so this route cannot be confused with the
		// account assertion options route by a stale client.
		Confirm bool `json:"confirm"`
	}
	if !httpapi.Decode(w, r, &in) {
		return
	}
	if !in.Confirm {
		httpapi.Error(w, 422, "approval_confirmation_required", "Confirm the reviewed proposal before starting passkey approval")
		return
	}
	actor, ok := approvalHTTPActor(w, r)
	if !ok {
		return
	}
	binding, err := s.ApprovalOptions(r.Context(), actor, r.PathValue("id"))
	if err != nil {
		approvalHTTPError(w, err)
		return
	}
	ceremony, err := s.ApprovalPasskeys.BeginBoundProposalAssertion(r.Context(), actor, binding)
	if err != nil {
		approvalHTTPError(w, err)
		return
	}
	httpapi.WriteJSON(w, 201, map[string]any{
		"challenge_id": ceremony.ID, "options": ceremony.Options, "expires_at": ceremony.ExpiresAt,
		"proposal_id": binding.ProposalID, "action_digest": binding.ActionDigest,
		"review_content_digest": binding.ReviewContentDigest,
	})
}

func (s *Service) Approve(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ChallengeID string          `json:"challenge_id"`
		Credential  json.RawMessage `json:"credential"`
		Comment     string          `json:"comment,omitempty"`
	}
	if !httpapi.Decode(w, r, &in) {
		return
	}
	if len(in.ChallengeID) != 32 || len(in.Credential) == 0 || len(in.Credential) > 128<<10 ||
		len(in.Comment) > 1000 || strings.TrimSpace(in.Comment) != in.Comment ||
		strings.ContainsAny(in.Comment, "\x00\r\n") {
		httpapi.Error(w, 422, "invalid_approval", "Provide one proposal passkey response and an optional short comment")
		return
	}
	actor, ok := approvalHTTPActor(w, r)
	if !ok {
		return
	}
	result, err := s.ApproveProposal(r.Context(), actor, r.PathValue("id"), in.ChallengeID,
		in.Credential, in.Comment)
	if err != nil {
		approvalHTTPError(w, err)
		return
	}
	status := http.StatusAccepted
	if result.Status == "approved" {
		status = http.StatusOK
	}
	httpapi.WriteJSON(w, status, result)
}

func approvalHTTPError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		httpapi.Error(w, 404, "proposal_not_found", "Proposal is unavailable")
	case errors.Is(err, store.ErrForbidden), errors.Is(err, v3passkey.ErrForbidden),
		errors.Is(err, v3passkey.ErrFreshLoginRequired):
		httpapi.Error(w, 403, "approval_forbidden", "Current human approval permission and session are required")
	case errors.Is(err, v3passkey.ErrChallengeInvalid),
		errors.Is(err, v3passkey.ErrProposalDigestUnavailable), errors.Is(err, store.ErrConflict):
		httpapi.Error(w, 409, "approval_conflict", "Proposal, challenge or approval state changed")
	case errors.Is(err, v3passkey.ErrInvalidResponse):
		httpapi.Error(w, 422, "approval_passkey_invalid", "Proposal passkey response was not verified")
	case errors.Is(err, v3passkey.ErrNoCredential):
		httpapi.Error(w, 409, "approval_passkey_required", "An active passkey is required")
	case errors.Is(err, v3passkey.ErrRateLimited):
		httpapi.Error(w, 429, "approval_rate_limited", "Try again later")
	case errors.Is(err, ErrInvalidProposal):
		httpapi.Error(w, 422, "invalid_approval", "Proposal cannot be approved")
	default:
		httpapi.Error(w, 503, "proposal_approval_unavailable", "Proposal approval is unavailable")
	}
}
