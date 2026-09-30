package v3events

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/store"
	"github.com/wans/aiops-canvas/internal/v3alertmanager"
)

func insertBulkSilenceEvidence(ctx context.Context, tx pgx.Tx, proposalID, actionDigest string,
	e bulkSilenceEvidence) error {
	_, err := tx.Exec(ctx, `INSERT INTO v3_bulk_group_silence_evidence
 (proposal_id,action_digest,group_id,environment_id,environment_row_id,application_id,
  source_id,fingerprint,investigation_id,identity_label_name,identity_label_value,
  config_revision,attestation_digest,endpoint_binding_fingerprint,member_digest,
  attestation_expires_at)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)
 ON CONFLICT (proposal_id) DO NOTHING`, proposalID, actionDigest, e.GroupID, e.EnvironmentID,
		e.EnvironmentRowID, e.ApplicationID, e.SourceID, e.Fingerprint, e.InvestigationID,
		e.LabelName, e.LabelValue, e.ConfigRevision, e.AttestationDigest,
		e.EndpointBindingFingerprint, e.MemberDigest, e.AttestationExpiresAt)
	if err != nil {
		return store.ClassifyError(err)
	}
	stored, storedDigest, err := loadBulkSilenceEvidence(ctx, tx, proposalID, true)
	if err != nil {
		return err
	}
	if storedDigest != actionDigest || !equalBulkEvidence(stored, e) {
		return store.ErrConflict
	}
	return nil
}

func loadBulkSilenceEvidence(ctx context.Context, tx pgx.Tx, proposalID string,
	lock bool) (bulkSilenceEvidence, string, error) {
	var e bulkSilenceEvidence
	var digest string
	query := `SELECT action_digest,group_id,environment_id,environment_row_id,application_id,
 source_id,fingerprint,investigation_id,identity_label_name,identity_label_value,
 config_revision,attestation_digest,endpoint_binding_fingerprint,member_digest,
 attestation_expires_at FROM v3_bulk_group_silence_evidence WHERE proposal_id=$1`
	if lock {
		query += " FOR SHARE"
	}
	err := tx.QueryRow(ctx, query, proposalID).Scan(&digest, &e.GroupID, &e.EnvironmentID,
		&e.EnvironmentRowID, &e.ApplicationID, &e.SourceID, &e.Fingerprint, &e.InvestigationID,
		&e.LabelName, &e.LabelValue, &e.ConfigRevision, &e.AttestationDigest,
		&e.EndpointBindingFingerprint, &e.MemberDigest, &e.AttestationExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return e, "", store.ErrConflict
	}
	return e, digest, err
}

// BulkEvidenceReadyWithTx is the approval-side gate. Call it after the
// proposal/action has been locked and digest-verified, before offering a
// passkey challenge and again before recording a vote. It only reads immutable
// evidence, process configuration and a bounded current source snapshot:
// approval must not acquire group or catalog locks in the reverse order of
// evidence attachment. Migration 148 also rejects bulk votes and approval
// state changes if this hook is missed.
func (b *BulkService) BulkEvidenceReadyWithTx(ctx context.Context, tx pgx.Tx,
	action v3alertmanager.SilenceReviewedAction) error {
	return b.bulkEvidenceReadyWithTx(ctx, tx, action, true)
}

func (b *BulkService) bulkEvidenceReadyWithTx(ctx context.Context, tx pgx.Tx,
	action v3alertmanager.SilenceReviewedAction, checkCollision bool) error {
	if action.Kind != v3alertmanager.ProposedSilenceCreateKind {
		return nil
	}
	if action.Create == nil {
		return store.ErrConflict
	}
	if !strings.HasPrefix(action.Create.IdempotencyKey, "bulk-") {
		return nil
	}
	if b == nil || b.DB == nil || b.Silence == nil || tx == nil {
		return errBulkNotConnected
	}
	e, digest, err := loadBulkSilenceEvidence(ctx, tx, action.ProposalID, true)
	if err != nil {
		return err
	}
	if digest != action.ActionDigest || e.EnvironmentID != action.EnvironmentID ||
		e.ApplicationID != action.ApplicationID || e.InvestigationID != action.InvestigationID ||
		e.EndpointBindingFingerprint != action.Create.BindingFingerprint ||
		action.CreatorID != action.Create.ActorID ||
		!e.AttestationExpiresAt.After(time.Now().UTC().Add(time.Hour)) {
		return store.ErrConflict
	}
	envLabel := b.Silence.EnvironmentLabels[e.EnvironmentID]
	appLabel, err := b.applicationLabel(e.EnvironmentID, e.ApplicationID)
	if err != nil || envLabel == "" || b.Silence.Ledger.EnvironmentLabel(e.EnvironmentID) != envLabel {
		return errBulkNotConnected
	}
	binding, err := b.identityBinding(e.EnvironmentID, e.SourceID, e.ApplicationID, envLabel)
	if err != nil || binding.LabelName != e.LabelName ||
		binding.ConfigRevision != e.ConfigRevision ||
		binding.AttestationDigest != e.AttestationDigest ||
		binding.EndpointBindingFingerprint != e.EndpointBindingFingerprint ||
		!binding.ExpiresAt.Equal(e.AttestationExpiresAt) ||
		!exactPreparedBulkMatchers(*action.Create, envLabel, e.EnvironmentID,
			appLabel, e.LabelName, e.LabelValue) {
		return errBulkNotConnected
	}
	if checkCollision {
		return currentBulkIdentityCollision(ctx, tx, groupKey{
			EnvironmentID: e.EnvironmentRowID, ApplicationID: e.ApplicationID,
			SourceID: e.SourceID, Fingerprint: e.Fingerprint,
		}, e.LabelName, e.LabelValue)
	}
	return nil
}

// PrelockBulkSilenceWithTx is the worker's first group-domain step, before
// locking the catalog target, proposal or grant-source tables. The independent worker must
// call it in its execution-claim transaction for every silence-create action.
// Ordinary non-bulk proposals need no group evidence; a bulk key without a
// committed companion row fails closed.
func (b *BulkService) PrelockBulkSilenceWithTx(ctx context.Context, tx pgx.Tx,
	proposalID string) error {
	if tx == nil {
		return errBulkNotConnected
	}
	var key string
	err := tx.QueryRow(ctx, `SELECT idempotency_key FROM v3_proposal_actions
 WHERE proposal_id=$1`, proposalID).Scan(&key)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(key, "bulk-") {
		return nil
	}
	if b == nil || b.DB == nil || b.Silence == nil || tx == nil {
		return errBulkNotConnected
	}
	e, _, err := loadBulkSilenceEvidence(ctx, tx, proposalID, false)
	if err != nil {
		return err
	}
	group, err := lockBulkGroup(ctx, tx, e.EnvironmentID, e.GroupID, groupKey{
		EnvironmentID: e.EnvironmentRowID, ApplicationID: e.ApplicationID,
		SourceID: e.SourceID, Fingerprint: e.Fingerprint,
	})
	if err != nil {
		return err
	}
	if group.OverLimit {
		return errBulkGroupTooLarge
	}
	return nil
}

// RevalidateBulkSilenceWithTx must run after the worker loads the verified,
// approved action and current grant sources, but before its execution claim.
// It checks that the reviewed action and immutable group evidence still have
// the same endpoint, matchers, attestation and current member snapshot.
func (b *BulkService) RevalidateBulkSilenceWithTx(ctx context.Context, tx pgx.Tx,
	action v3alertmanager.SilenceReviewedAction) error {
	if action.Create == nil {
		return store.ErrConflict
	}
	if !strings.HasPrefix(action.Create.IdempotencyKey, "bulk-") {
		return nil
	}
	if b == nil || b.DB == nil || b.Silence == nil || tx == nil || action.Status != "approved" {
		return errBulkNotConnected
	}
	// prepareBulkSilence below performs the current collision check while
	// revalidating locked members, so avoid scanning the source twice.
	if err := b.bulkEvidenceReadyWithTx(ctx, tx, action, false); err != nil {
		return err
	}
	e, digest, err := loadBulkSilenceEvidence(ctx, tx, action.ProposalID, true)
	if err != nil {
		return err
	}
	if digest != action.ActionDigest || e.EnvironmentID != action.EnvironmentID ||
		e.ApplicationID != action.ApplicationID || e.InvestigationID != action.InvestigationID ||
		e.EndpointBindingFingerprint != action.Create.BindingFingerprint ||
		action.CreatorID != action.Create.ActorID {
		return store.ErrConflict
	}
	key := groupKey{EnvironmentID: e.EnvironmentRowID, ApplicationID: e.ApplicationID,
		SourceID: e.SourceID, Fingerprint: e.Fingerprint}
	group, err := lockBulkGroup(ctx, tx, e.EnvironmentID, e.GroupID, key)
	if err != nil {
		return err
	}
	if group.OverLimit {
		return errBulkGroupTooLarge
	}
	if b.Access == nil {
		return errBulkNotConnected
	}
	if err = authorizeBulkGroup(ctx, tx, b.Access, core.User{ID: action.CreatorID},
		key.ApplicationID, group.OwnerTeam, e.EnvironmentID); err != nil {
		return err
	}
	current, err := b.prepareBulkSilence(ctx, tx, core.User{ID: action.CreatorID},
		e.GroupID, e.EnvironmentID, key, group)
	if err != nil {
		return err
	}
	if !equalBulkEvidence(e, current.Evidence) ||
		!reflect.DeepEqual(*action.Create, current.Prepared) {
		return store.ErrConflict
	}
	return nil
}
