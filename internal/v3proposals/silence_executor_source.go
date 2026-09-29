package v3proposals

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/store"
	"github.com/wans/aiops-canvas/internal/v3alertmanager"
)

// SilenceExecutionActionSource loads proposal-time evidence for the independent
// executor. It grants no approval or write authority. The caller must use the
// same transaction for this read, approval verification, and execution claim.
type SilenceExecutionActionSource struct {
	Proposals *Service
}

func (s *SilenceExecutionActionSource) Ready(ctx context.Context) error {
	if s == nil || s.Proposals == nil || s.Proposals.DB == nil {
		return v3alertmanager.ErrNotConfigured
	}
	if _, err := s.Proposals.DB.Exec(ctx, `SELECT 1 FROM v3_proposal_actions LIMIT 0`); err != nil {
		return v3alertmanager.ErrNotConfigured
	}
	return nil
}

func (s *SilenceExecutionActionSource) LoadReviewedActionWithTx(ctx context.Context, tx pgx.Tx,
	proposalID string) (v3alertmanager.SilenceReviewedAction, error) {
	var zero v3alertmanager.SilenceReviewedAction
	if s == nil || s.Proposals == nil || s.Proposals.DB == nil || tx == nil {
		return zero, v3alertmanager.ErrNotConfigured
	}
	if !bounded(proposalID, 128) {
		return zero, ErrInvalidProposal
	}
	var kind, reviewDigest string
	err := tx.QueryRow(ctx, `SELECT a.action_kind,a.review_content_digest
 FROM v3_proposal_actions a JOIN v3_proposals p ON p.id=a.proposal_id
 WHERE a.proposal_id=$1 FOR SHARE OF a,p`, proposalID).Scan(&kind, &reviewDigest)
	if errors.Is(err, pgx.ErrNoRows) {
		return zero, store.ErrNotFound
	}
	if err != nil {
		return zero, err
	}
	if !silenceProductDigest.MatchString(reviewDigest) {
		return zero, ErrInvalidProposal
	}
	switch kind {
	case v3alertmanager.ProposedSilenceCreateKind:
		action, result, err := LoadSilenceActionWithTx(ctx, tx, proposalID)
		if err != nil {
			return zero, err
		}
		if result.ProposalID != proposalID || !silenceProductDigest.MatchString(result.ActionDigest) ||
			action.InvestigationUpdatedAt.IsZero() || result.Status == "" {
			return zero, ErrInvalidProposal
		}
		return v3alertmanager.SilenceReviewedAction{
			ProposalID: proposalID, Kind: kind, Status: result.Status,
			ActionDigest: result.ActionDigest, ReviewContentDigest: reviewDigest,
			CreatorID: action.Silence.ActorID, EnvironmentID: action.Silence.EnvironmentID,
			ApplicationID: action.Silence.ApplicationID, InvestigationID: action.InvestigationID,
			InvestigationUpdatedAt: action.InvestigationUpdatedAt, Create: &action.Silence,
		}, nil
	case v3alertmanager.ProposedSilenceCancelKind:
		return loadReviewedSilenceCancel(ctx, tx, proposalID, reviewDigest)
	default:
		return zero, ErrInvalidProposal
	}
}

func loadReviewedSilenceCancel(ctx context.Context, tx pgx.Tx, proposalID, selectedReviewDigest string) (
	v3alertmanager.SilenceReviewedAction, error) {
	var zero v3alertmanager.SilenceReviewedAction
	var actionRaw, contentRaw, observedRaw, createPayloadRaw []byte
	var result v3alertmanager.SilenceReviewedAction
	var kind, sourceKind, requestHash, binding, key, creator, env, app, investigation string
	var parentCreator, parentKind, parentEnv, parentApp, parentInvestigation string
	var intentID, intentOperation, intentProposal, intentActor, intentEnv, intentApp, intentInvestigation string
	var intentUpstream, intentKey, intentRequestHash, intentObservedDigest, intentReason string
	var operationID, operationActor, operationEnv, operationEnvLabel, operationApp, operationAppLabel, operationTeam string
	var operationState, operationUpstream, operationPayloadHash string
	var version int
	var investigationUpdatedAt, intentObservedAt time.Time
	err := tx.QueryRow(ctx, `SELECT
 p.status,p.created_by,p.creator_kind,p.environment_id,p.application_id,p.investigation_id,p.content,
 a.action_kind,a.schema_version,a.action,a.action_digest,a.review_content_digest,a.request_hash,
 a.binding_fingerprint,a.creator_id,a.environment_id,a.application_id,a.investigation_id,
 a.investigation_updated_at,a.idempotency_key,a.source_kind,
 c.id,c.create_operation_id,c.proposal_id,c.actor_id,c.environment_id,c.application_id,
 c.investigation_id,c.upstream_id,c.idempotency_key,c.request_hash,c.observed_digest,
 c.observed_snapshot,c.observed_at,c.reason,
 o.id,o.actor_id,o.environment_id,o.environment_label,o.application_id,o.application_label,
 o.owner_team_id,o.state,o.upstream_id,o.payload_hash,o.payload
 FROM v3_proposals p
 JOIN v3_proposal_actions a ON a.proposal_id=p.id
 JOIN v3_silence_cancel_intents c ON c.proposal_id=p.id
 JOIN v3_silence_create_operations o ON o.id=c.create_operation_id
 WHERE p.id=$1 FOR SHARE OF p,a,c,o`, proposalID).Scan(
		&result.Status, &parentCreator, &parentKind, &parentEnv, &parentApp, &parentInvestigation, &contentRaw,
		&kind, &version, &actionRaw, &result.ActionDigest, &result.ReviewContentDigest, &requestHash,
		&binding, &creator, &env, &app, &investigation, &investigationUpdatedAt, &key, &sourceKind,
		&intentID, &intentOperation, &intentProposal, &intentActor, &intentEnv, &intentApp,
		&intentInvestigation, &intentUpstream, &intentKey, &intentRequestHash, &intentObservedDigest,
		&observedRaw, &intentObservedAt, &intentReason,
		&operationID, &operationActor, &operationEnv, &operationEnvLabel, &operationApp, &operationAppLabel,
		&operationTeam, &operationState, &operationUpstream, &operationPayloadHash, &createPayloadRaw)
	if errors.Is(err, pgx.ErrNoRows) {
		return zero, store.ErrNotFound
	}
	if err != nil {
		return zero, err
	}
	if result.Status == "" || kind != v3alertmanager.ProposedSilenceCancelKind ||
		version != v3alertmanager.ProposedSilenceCancelVersion || sourceKind != silenceProposalSource ||
		parentKind != "user" || result.ReviewContentDigest != selectedReviewDigest ||
		!silenceProductDigest.MatchString(result.ActionDigest) ||
		!silenceProductDigest.MatchString(result.ReviewContentDigest) ||
		!silenceProductDigest.MatchString(requestHash) || !silenceProductDigest.MatchString(binding) ||
		!silenceProductDigest.MatchString(intentObservedDigest) ||
		!silenceProductDigest.MatchString(operationPayloadHash) ||
		intentProposal != proposalID || intentOperation != operationID || intentActor != creator ||
		intentEnv != env || intentApp != app || intentInvestigation != investigation ||
		intentKey != key || intentRequestHash != requestHash ||
		parentCreator != creator || parentEnv != env || parentApp != app || parentInvestigation != investigation ||
		operationState != v3alertmanager.SilenceCreateConfirmed || operationUpstream != intentUpstream ||
		operationEnv != env || operationApp != app ||
		investigationUpdatedAt.IsZero() || intentObservedAt.IsZero() {
		return zero, ErrInvalidProposal
	}
	var action v3alertmanager.ProposedSilenceCancel
	if err = json.Unmarshal(actionRaw, &action); err != nil {
		return zero, ErrInvalidProposal
	}
	canonicalAction, err := canonicalProposalJSON(action)
	if err != nil {
		return zero, ErrInvalidProposal
	}
	storedAction, err := canonicalProposalJSON(json.RawMessage(actionRaw))
	if err != nil || !bytes.Equal(canonicalAction, storedAction) ||
		proposalDigest("aiops.v3.proposal.action.v1", canonicalAction) != result.ActionDigest ||
		action.Kind != kind || action.SchemaVersion != version ||
		action.IntentID != intentID || action.CreateOperationID != operationID ||
		action.CreatePayloadHash != operationPayloadHash || action.BindingFingerprint != binding ||
		action.ActorID != creator || action.EnvironmentID != env ||
		action.EnvironmentLabel != operationEnvLabel || action.ApplicationID != app ||
		action.ApplicationLabel != operationAppLabel || action.OwnerTeamID != operationTeam ||
		action.InvestigationID != investigation || !action.InvestigationUpdatedAt.Equal(investigationUpdatedAt) ||
		action.UpstreamID != intentUpstream || action.IdempotencyKey != key ||
		action.RequestHash != requestHash || action.ObservedDigest != intentObservedDigest ||
		!action.ObservedAt.Equal(intentObservedAt) || action.Reason != intentReason ||
		!silenceProposalKey.MatchString(key) || !validReviewedSilenceID(intentUpstream) {
		return zero, ErrInvalidProposal
	}
	computedRequestHash, err := reviewedSilenceCancelRequestHash(action)
	if err != nil || computedRequestHash != requestHash {
		return zero, ErrInvalidProposal
	}
	content := silenceCancelReviewContent(action)
	if !validContent(content) {
		return zero, ErrInvalidProposal
	}
	canonicalContent, err := canonicalProposalJSON(content)
	if err != nil {
		return zero, ErrInvalidProposal
	}
	storedContent, err := canonicalProposalJSON(json.RawMessage(contentRaw))
	if err != nil || !bytes.Equal(canonicalContent, storedContent) ||
		proposalDigest("aiops.v3.proposal.review.v1", canonicalContent) != result.ReviewContentDigest {
		return zero, ErrInvalidProposal
	}
	canonicalObserved, err := canonicalProposalJSON(json.RawMessage(observedRaw))
	if err != nil || len(canonicalObserved) > 64<<10 ||
		reviewedSilenceSHA256(canonicalObserved) != intentObservedDigest {
		return zero, ErrInvalidProposal
	}
	var observed v3alertmanager.Silence
	if json.Unmarshal(canonicalObserved, &observed) != nil || observed.ID != intentUpstream ||
		(observed.Status.State != "active" && observed.Status.State != "pending") ||
		observed.UpdatedAt.IsZero() || observed.EndsAt.IsZero() ||
		!observed.EndsAt.After(intentObservedAt) ||
		observed.Annotations["aiops_operation_id"] != operationID {
		return zero, ErrInvalidProposal
	}
	var createRequest v3alertmanager.CreateSilenceRequest
	if json.Unmarshal(createPayloadRaw, &createRequest) != nil {
		return zero, ErrInvalidProposal
	}
	canonicalCreate, err := canonicalProposalJSON(createRequest)
	if err != nil {
		return zero, ErrInvalidProposal
	}
	storedCreate, err := canonicalProposalJSON(json.RawMessage(createPayloadRaw))
	createWire, marshalErr := json.Marshal(createRequest)
	if err != nil || !bytes.Equal(canonicalCreate, storedCreate) ||
		marshalErr != nil || reviewedSilenceSHA256(createWire) != operationPayloadHash ||
		createRequest.CreatedBy != operationActor ||
		createRequest.Annotations["aiops_operation_id"] != operationID ||
		!reviewedSilenceSnapshotMatchesCreate(observed, createRequest) ||
		!reviewedSilenceExactMatcher(observed.Matchers, operationEnvLabel, env) ||
		!reviewedSilenceExactMatcher(observed.Matchers, "application", operationAppLabel) {
		return zero, ErrInvalidProposal
	}
	result.ProposalID, result.Kind = proposalID, kind
	result.CreatorID, result.EnvironmentID, result.ApplicationID = creator, env, app
	result.InvestigationID, result.InvestigationUpdatedAt = investigation, investigationUpdatedAt
	result.Cancel = &action
	result.CancelObservedSnapshot = canonicalObserved
	return result, nil
}

func reviewedSilenceCancelRequestHash(action v3alertmanager.ProposedSilenceCancel) (string, error) {
	raw, err := json.Marshal(struct {
		ActorID, EnvironmentID, UpstreamID, InvestigationID, IdempotencyKey, Reason string
	}{action.ActorID, action.EnvironmentID, action.UpstreamID, action.InvestigationID,
		action.IdempotencyKey, action.Reason})
	if err != nil {
		return "", err
	}
	return reviewedSilenceSHA256(raw), nil
}

func reviewedSilenceSHA256(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func validReviewedSilenceID(id string) bool {
	if len(id) != 36 {
		return false
	}
	for i, c := range id {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
		} else if !((c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F') || (c >= '0' && c <= '9')) {
			return false
		}
	}
	return true
}

func reviewedSilenceSnapshotMatchesCreate(observed v3alertmanager.Silence,
	request v3alertmanager.CreateSilenceRequest) bool {
	if observed.StartsAt.Before(request.StartsAt) ||
		observed.StartsAt.After(request.StartsAt.Add(30*time.Second)) ||
		!observed.StartsAt.Before(request.EndsAt) || !observed.EndsAt.Equal(request.EndsAt) ||
		observed.CreatedBy != request.CreatedBy || observed.Comment != request.Comment ||
		!reflect.DeepEqual(observed.Annotations, request.Annotations) ||
		len(observed.Matchers) != len(request.Matchers) {
		return false
	}
	a := append([]v3alertmanager.Matcher(nil), observed.Matchers...)
	b := append([]v3alertmanager.Matcher(nil), request.Matchers...)
	less := func(items []v3alertmanager.Matcher, i, j int) bool {
		if items[i].Name != items[j].Name {
			return items[i].Name < items[j].Name
		}
		if items[i].Value != items[j].Value {
			return items[i].Value < items[j].Value
		}
		if items[i].IsRegex != items[j].IsRegex {
			return !items[i].IsRegex
		}
		return !items[i].IsEqual && items[j].IsEqual
	}
	sort.Slice(a, func(i, j int) bool { return less(a, i, j) })
	sort.Slice(b, func(i, j int) bool { return less(b, i, j) })
	return reflect.DeepEqual(a, b)
}

func reviewedSilenceExactMatcher(matchers []v3alertmanager.Matcher, name, value string) bool {
	if name == "" || value == "" {
		return false
	}
	count := 0
	for _, matcher := range matchers {
		if matcher.Name == name {
			if !matcher.IsEqual || matcher.IsRegex || matcher.Value != value {
				return false
			}
			count++
		}
	}
	return count == 1
}
