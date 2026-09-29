package v3events

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/store"
	"github.com/wans/aiops-canvas/internal/v3alertmanager"
	"github.com/wans/aiops-canvas/internal/v3proposals"
)

const bulkIdentityAttestation = "upstream-equality-unique-stable-v1"

var bulkIdentityLabelName = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)
var bulkEvidenceDigest = regexp.MustCompile(`^[0-9a-f]{64}$`)
var bulkConfigRevision = regexp.MustCompile(`^[a-zA-Z0-9._/-]{1,128}$`)

type bulkSilenceEvidence struct {
	GroupID, EnvironmentID, EnvironmentRowID, ApplicationID, SourceID, Fingerprint string
	InvestigationID, LabelName, LabelValue, ConfigRevision, AttestationDigest      string
	EndpointBindingFingerprint, MemberDigest                                       string
	AttestationExpiresAt                                                           time.Time
}

type bulkSilencePlan struct {
	Input    v3alertmanager.SilenceCreateInput
	Prepared v3alertmanager.ProposedSilenceCreate
	Evidence bulkSilenceEvidence
}

func (b *BulkService) applicationLabel(env, app string) (string, error) {
	if b.Silence == nil || b.Silence.Proposals == nil || b.Silence.Ledger == nil ||
		b.Silence.Client == nil || b.Silence.Proposals.DB != b.DB || b.Silence.Ledger.DB != b.DB {
		return "", errBulkNotConnected
	}
	label := ""
	for candidate, boundApp := range b.Silence.ApplicationBindings[env] {
		if boundApp != app {
			continue
		}
		if label != "" {
			return "", errBulkNotConnected
		}
		label = candidate
	}
	if label == "" {
		return "", errBulkNotConnected
	}
	return label, nil
}

func (b *BulkService) identityBinding(env, sourceID, appID, envLabel string) (BulkGroupIdentityBinding, error) {
	var zero BulkGroupIdentityBinding
	if b.Silence == nil {
		return zero, errBulkNotConnected
	}
	binding, ok := b.Silence.GroupIdentityBindings[env][sourceID][appID]
	// PostgreSQL timestamptz stores microseconds. Pin the reviewed expiry to
	// that precision before persisting and comparing companion evidence.
	binding.ExpiresAt = binding.ExpiresAt.UTC().Truncate(time.Microsecond)
	if !ok || sourceID == "" || binding.Attestation != bulkIdentityAttestation ||
		len(binding.LabelName) > 128 || !bulkIdentityLabelName.MatchString(binding.LabelName) ||
		strings.HasPrefix(binding.LabelName, "__") ||
		binding.LabelName == "application" || binding.LabelName == envLabel ||
		binding.LabelName == "alertname" || !bulkConfigRevision.MatchString(binding.ConfigRevision) ||
		!bulkEvidenceDigest.MatchString(binding.AttestationDigest) ||
		!bulkEvidenceDigest.MatchString(binding.EndpointBindingFingerprint) ||
		!binding.ExpiresAt.After(time.Now().UTC().Add(time.Hour)) {
		return zero, errBulkNotConnected
	}
	return binding, nil
}

// Source coverage, authenticated ingress mapping and the dedicated label
// attestation are independent evidence. None is inferred from the envelope
// fingerprint or the application/alertname labels alone.
func (b *BulkService) attestedSilenceSource(ctx context.Context, env string,
	key groupKey, group lockedBulkGroup, appLabel, envLabel, identityName, identityValue string,
	oldest time.Time) error {
	if b.Silence.AlertRangeCovered == nil || oldest.IsZero() {
		return errBulkNotConnected
	}
	coverageCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	sourceID := b.Silence.AlertRangeCovered(coverageCtx, key.ApplicationID, env, oldest, time.Now().UTC())
	if coverageCtx.Err() != nil || sourceID == "" || sourceID == "ai-patrol" || sourceID != key.SourceID {
		return errBulkNotConnected
	}
	sources, selected := 0, 0
	for _, source := range b.Silence.IngressSources {
		if source.ID != sourceID {
			continue
		}
		sources++
		if source.TokenFile == "" {
			return errBulkNotConnected
		}
		for _, mapping := range source.Mappings {
			if mapping.ApplicationID != key.ApplicationID || mapping.EnvironmentID != group.EnvironmentID {
				continue
			}
			selected++
			if mapping.Labels["application"] != appLabel || mapping.Labels[envLabel] != env {
				return errBulkNotConnected
			}
			if fixed := mapping.Labels[identityName]; fixed != "" && fixed != identityValue {
				return errBulkNotConnected
			}
		}
	}
	if sources != 1 || selected != 1 {
		return errBulkNotConnected
	}
	return nil
}

func bulkMembersDigest(members []bulkMember) (string, error) {
	raw, err := json.Marshal(members) // Group rows are sorted by immutable ID.
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(append([]byte("aiops.v3.bulk.group.members.v1\x00"), raw...))
	return hex.EncodeToString(sum[:]), nil
}

func exactPreparedBulkMatchers(prepared v3alertmanager.ProposedSilenceCreate,
	envLabel, env, appLabel, identityName, identityValue string) bool {
	if len(prepared.Matchers) != 3 {
		return false
	}
	expected := map[string]string{envLabel: env, "application": appLabel, identityName: identityValue}
	if len(expected) != 3 {
		return false
	}
	for _, matcher := range prepared.Matchers {
		if matcher.IsRegex || !matcher.IsEqual || expected[matcher.Name] != matcher.Value {
			return false
		}
		delete(expected, matcher.Name)
	}
	return len(expected) == 0
}

// prepareBulkSilence reads only trusted process configuration and locked,
// persisted incident labels. The attestation must promise that its dedicated
// label is stable, propagated unchanged to this Alertmanager endpoint and the
// authenticated webhook, globally unique to the full local group tuple on
// that endpoint (including future alerts), and not reused during a silence.
func (b *BulkService) prepareBulkSilence(ctx context.Context, tx pgx.Tx, actor core.User,
	id, env string, key groupKey, group lockedBulkGroup) (bulkSilencePlan, error) {
	var zero bulkSilencePlan
	appLabel, err := b.applicationLabel(env, key.ApplicationID)
	if err != nil {
		return zero, err
	}
	envLabel := b.Silence.EnvironmentLabels[env]
	if envLabel == "" || envLabel == "application" ||
		b.Silence.Ledger.EnvironmentLabel(env) != envLabel ||
		group.EnvironmentID != key.EnvironmentID {
		return zero, errBulkNotConnected
	}
	binding, err := b.identityBinding(env, key.SourceID, key.ApplicationID, envLabel)
	if err != nil {
		return zero, err
	}
	active, linked := 0, ""
	oldest := time.Time{}
	identityValue := ""
	for _, member := range group.Members {
		value := member.Labels[binding.LabelName]
		if len(member.Labels) == 0 || value == "" || len(value) > 256 ||
			member.Labels["application"] != appLabel || member.Labels[envLabel] != env ||
			member.SourceID != key.SourceID || member.Fingerprint != key.Fingerprint ||
			member.Suspected || member.StartsAt.IsZero() ||
			(identityValue != "" && identityValue != value) {
			return zero, errBulkNotConnected
		}
		identityValue = value
		if !activeClaimMember(member) {
			continue
		}
		active++
		if oldest.IsZero() || member.StartsAt.Before(oldest) {
			oldest = member.StartsAt
		}
		if member.InvestigationID == "" {
			return zero, errInvestigationRequired
		}
		if linked != "" && linked != member.InvestigationID {
			return zero, store.ErrConflict
		}
		linked = member.InvestigationID
	}
	if active == 0 {
		return zero, store.ErrConflict
	}
	if err = b.attestedSilenceSource(ctx, env, key, group, appLabel, envLabel,
		binding.LabelName, identityValue, oldest); err != nil {
		return zero, err
	}
	var status string
	err = tx.QueryRow(ctx, `SELECT status FROM investigations
 WHERE id=$1 AND application_id=$2 AND environment_id=$3 FOR SHARE`,
		linked, key.ApplicationID, group.EnvironmentID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && status != "open" {
		return zero, errInvestigationRequired
	}
	if err != nil {
		return zero, err
	}
	memberDigest, err := bulkMembersDigest(group.Members)
	if err != nil {
		return zero, err
	}
	keyHash := store.Hash("v3.bulk.silence.v2\x00" + actor.ID + "\x00" + id + "\x00" +
		linked + "\x00" + binding.AttestationDigest + "\x00" + binding.ConfigRevision +
		"\x00" + binding.LabelName + "\x00" + identityValue + "\x00" + memberDigest)
	comment := fmt.Sprintf("AIOps Canvas alert group %s; scope config %s/%s; member evidence %s; one-hour silence pending approval",
		id, binding.ConfigRevision, binding.AttestationDigest, memberDigest)
	input := v3alertmanager.SilenceCreateInput{
		EnvironmentID: env, IdempotencyKey: "bulk-" + keyHash, Comment: comment,
		Matchers: []v3alertmanager.Matcher{
			{Name: "application", Value: appLabel, IsEqual: true},
			{Name: binding.LabelName, Value: identityValue, IsEqual: true},
		},
		Duration: time.Hour,
	}
	if len(comment) > 1000 {
		return zero, errBulkNotConnected
	}
	prepared, err := b.Silence.Ledger.PrepareCreateProposalWithTx(ctx, tx, actor, input, b.Silence.Client)
	if errors.Is(err, v3alertmanager.ErrSilenceCreateInvalid) {
		return zero, errBulkNotConnected
	}
	if err != nil {
		return zero, err
	}
	if prepared.ApplicationID != key.ApplicationID || prepared.EnvironmentID != env ||
		prepared.EnvironmentLabel != envLabel ||
		prepared.BindingFingerprint != binding.EndpointBindingFingerprint ||
		!exactPreparedBulkMatchers(prepared, envLabel, env, appLabel, binding.LabelName, identityValue) {
		return zero, errBulkNotConnected
	}
	return bulkSilencePlan{Input: input, Prepared: prepared, Evidence: bulkSilenceEvidence{
		GroupID: id, EnvironmentID: env, EnvironmentRowID: group.EnvironmentID,
		ApplicationID: key.ApplicationID, SourceID: key.SourceID, Fingerprint: key.Fingerprint,
		InvestigationID: linked, LabelName: binding.LabelName, LabelValue: identityValue,
		ConfigRevision: binding.ConfigRevision, AttestationDigest: binding.AttestationDigest,
		EndpointBindingFingerprint: binding.EndpointBindingFingerprint,
		MemberDigest:               memberDigest, AttestationExpiresAt: binding.ExpiresAt,
	}}, nil
}

func equalBulkEvidence(a, b bulkSilenceEvidence) bool {
	return a.GroupID == b.GroupID && a.EnvironmentID == b.EnvironmentID &&
		a.EnvironmentRowID == b.EnvironmentRowID && a.ApplicationID == b.ApplicationID &&
		a.SourceID == b.SourceID && a.Fingerprint == b.Fingerprint &&
		a.InvestigationID == b.InvestigationID && a.LabelName == b.LabelName &&
		a.LabelValue == b.LabelValue && a.ConfigRevision == b.ConfigRevision &&
		a.AttestationDigest == b.AttestationDigest &&
		a.EndpointBindingFingerprint == b.EndpointBindingFingerprint &&
		a.MemberDigest == b.MemberDigest && a.AttestationExpiresAt.Equal(b.AttestationExpiresAt)
}

func equalBulkPlan(a, b bulkSilencePlan) bool {
	return reflect.DeepEqual(a.Input, b.Input) && reflect.DeepEqual(a.Prepared, b.Prepared) &&
		equalBulkEvidence(a.Evidence, b.Evidence)
}

func (b *BulkService) createBulkSilenceProposal(r *http.Request,
	id, env string, key groupKey, matched int, plan bulkSilencePlan) bulkResult {
	actor := httpapi.CurrentUser(r)
	result, err := b.Silence.Proposals.CreateSilenceProposalForSession(r.Context(), actor,
		store.Hash(bulkCookie(r)), v3proposals.SilenceProposalRequest{
			InvestigationID: plan.Evidence.InvestigationID, Silence: plan.Input,
		}, b.Silence.Ledger, b.Silence.Client)
	if err != nil {
		if errors.Is(err, v3alertmanager.ErrSilenceCreateInvalid) ||
			errors.Is(err, v3proposals.ErrInvalidProposal) {
			return bulkFailed(id, "not_connected", matched)
		}
		return safeBulkError(id, matched, err)
	}
	if result.Status != "pending" || result.ProposalID == "" || !bulkEvidenceDigest.MatchString(result.ActionDigest) {
		failed := bulkFailed(id, "conflict", matched)
		failed.ProposalID = &result.ProposalID
		return failed
	}
	if err = b.attachBulkSilenceEvidence(r, env, id, key, plan, result); err != nil {
		return safeBulkError(id, matched, err)
	}
	// No upstream alert or incident state changed.
	out := bulkSucceeded(id, "pending_approval", matched, 0)
	out.ProposalID = &result.ProposalID
	out.InvestigationID = &plan.Evidence.InvestigationID
	return out
}

// The generic producer commits separately. This second transaction verifies
// that the group, grants, session, connector and attestation still match, then
// links immutable evidence. An orphan proposal must fail the worker's bulk
// evidence gate; it is never reported as a successful bulk action.
func (b *BulkService) attachBulkSilenceEvidence(r *http.Request, env, id string, key groupKey,
	initial bulkSilencePlan, result v3proposals.SilenceProposalResult) error {
	tx, err := b.DB.Begin(r.Context())
	if err != nil {
		return err
	}
	defer tx.Rollback(r.Context())
	group, err := lockBulkGroup(r.Context(), tx, env, id, key)
	if err != nil {
		return err
	}
	// The producer prelocks an existing proposal before taking grant-source
	// locks. Preserve that order on idempotent evidence attachment.
	var digest, proposalKey, binding, kind, creator, status string
	err = tx.QueryRow(r.Context(), `SELECT a.action_digest,a.idempotency_key,a.binding_fingerprint,
 a.action_kind,a.creator_id,p.status FROM v3_proposal_actions a
 JOIN v3_proposals p ON p.id=a.proposal_id WHERE a.proposal_id=$1 FOR SHARE OF a,p`,
		result.ProposalID).Scan(&digest, &proposalKey, &binding, &kind, &creator, &status)
	if err != nil {
		return err
	}
	if status != "pending" || digest != result.ActionDigest ||
		proposalKey != initial.Input.IdempotencyKey ||
		binding != initial.Evidence.EndpointBindingFingerprint ||
		kind != v3alertmanager.ProposedSilenceCreateKind || creator != httpapi.CurrentUser(r).ID {
		return store.ErrConflict
	}
	token, err := b.authorizeLockedGroup(r, tx, key, group, env, "silence")
	if err != nil {
		return err
	}
	if group.OverLimit {
		return errBulkGroupTooLarge
	}
	current, err := b.prepareBulkSilence(r.Context(), tx, httpapi.CurrentUser(r), id, env, key, group)
	if err != nil {
		return err
	}
	if !equalBulkPlan(initial, current) {
		return store.ErrConflict
	}
	if digest != result.ActionDigest || proposalKey != current.Input.IdempotencyKey ||
		binding != current.Evidence.EndpointBindingFingerprint ||
		kind != v3alertmanager.ProposedSilenceCreateKind || creator != httpapi.CurrentUser(r).ID {
		return store.ErrConflict
	}
	if err = insertBulkSilenceEvidence(r.Context(), tx, result.ProposalID, digest, current.Evidence); err != nil {
		return err
	}
	if err = bulkSessionStillActive(r.Context(), tx, token, httpapi.CurrentUser(r).ID); err != nil {
		return err
	}
	return tx.Commit(r.Context())
}
