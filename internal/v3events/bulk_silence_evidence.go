package v3events

import (
	"context"
	"errors"
	"reflect"
	"strings"

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
		return err
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

// PrelockBulkSilenceWithTx is the worker's first group-domain step, before
// locking the proposal or grant-source tables. The independent worker must
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
