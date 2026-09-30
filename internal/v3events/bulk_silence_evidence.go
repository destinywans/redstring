package v3events

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/store"
	"github.com/wans/aiops-canvas/internal/v3alertmanager"
)

// A reservation is separate from immutable evidence. A terminal, never-sent
// proposal can release its current group and matcher scope without erasing the
// old proposal or its evidence. The caller already holds the current group
// rows and authorization; lock the old proposal before updating its slot so
// approval and rejection cannot race the release decision.
func releaseTerminalBulkReservation(ctx context.Context, tx pgx.Tx, actor core.User,
	key groupKey) error {
	var proposalID string
	err := tx.QueryRow(ctx, `SELECT proposal_id FROM v3_bulk_group_silence_reservations
 WHERE environment_row_id=$1 AND application_id=$2 AND source_id=$3 AND fingerprint=$4
   AND released_at IS NULL`, key.EnvironmentID, key.ApplicationID, key.SourceID,
		key.Fingerprint).Scan(&proposalID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var status string
	var expired bool
	err = tx.QueryRow(ctx, `SELECT p.status,e.attestation_expires_at<=clock_timestamp()
 FROM v3_proposals p JOIN v3_bulk_group_silence_evidence e ON e.proposal_id=p.id
 WHERE p.id=$1 FOR UPDATE OF p`, proposalID).Scan(&status, &expired)
	if err != nil {
		return err
	}
	reason := ""
	switch {
	case status == "rejected":
		reason = "rejected"
	case status == "pending" && expired:
		reason = "expired"
	default:
		return nil // The availability check below refuses a different live proposal.
	}
	var releasedID string
	err = tx.QueryRow(ctx, `UPDATE v3_bulk_group_silence_reservations
 SET released_at=clock_timestamp(),release_reason=$2,released_by=$3
 WHERE proposal_id=$1 AND released_at IS NULL RETURNING proposal_id`,
		proposalID, reason, actor.ID).Scan(&releasedID)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.ErrConflict
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23514" {
		return store.ErrConflict
	}
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO management_audit
 (actor_id,actor_kind,application_id,action,target,outcome,changes)
 VALUES($1,'user',$2,'alert.group.silence_reservation_released',$3,'succeeded',
 jsonb_build_object('reason',$4::text,'environment_row_id',$5::text,
                    'source_id',$6::text,'fingerprint',$7::text))`,
		actor.ID, key.ApplicationID, releasedID, reason, key.EnvironmentID, key.SourceID, key.Fingerprint)
	return err
}

// The last released proposal is the stable idempotency generation. It changes
// only after a verified release, so retries of the new pending proposal keep
// their key while a rejected or expired predecessor can be proposed again.
func bulkReservationGeneration(ctx context.Context, tx pgx.Tx, key groupKey) (string, error) {
	var proposalID string
	err := tx.QueryRow(ctx, `SELECT proposal_id FROM v3_bulk_group_silence_reservations
 WHERE environment_row_id=$1 AND application_id=$2 AND source_id=$3 AND fingerprint=$4
   AND released_at IS NOT NULL ORDER BY reservation_seq DESC LIMIT 1`,
		key.EnvironmentID, key.ApplicationID, key.SourceID, key.Fingerprint).Scan(&proposalID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return proposalID, err
}

func bulkReservationAvailable(ctx context.Context, tx pgx.Tx, actor core.User,
	e bulkSilenceEvidence, idempotencyKey, expectedProposalID string) error {
	rows, err := tx.Query(ctx, `SELECT r.proposal_id,a.creator_id,a.idempotency_key,p.status,
 r.environment_row_id,r.application_id,r.source_id,r.fingerprint,
 r.endpoint_binding_fingerprint,r.identity_label_name,r.identity_label_value
 FROM v3_bulk_group_silence_reservations r
 JOIN v3_proposal_actions a ON a.proposal_id=r.proposal_id
 JOIN v3_proposals p ON p.id=r.proposal_id
 WHERE r.released_at IS NULL AND (
  (r.environment_row_id=$1 AND r.application_id=$2 AND r.source_id=$3 AND r.fingerprint=$4)
  OR (r.endpoint_binding_fingerprint=$5 AND r.identity_label_name=$6 AND r.identity_label_value=$7))`,
		e.EnvironmentRowID, e.ApplicationID, e.SourceID, e.Fingerprint,
		e.EndpointBindingFingerprint, e.LabelName, e.LabelValue)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var proposalID, creator, key, status string
		var environmentRowID, applicationID, sourceID, fingerprint string
		var endpoint, labelName, labelValue string
		if err = rows.Scan(&proposalID, &creator, &key, &status,
			&environmentRowID, &applicationID, &sourceID, &fingerprint,
			&endpoint, &labelName, &labelValue); err != nil {
			return err
		}
		if status != "pending" || creator != actor.ID || key != idempotencyKey ||
			(expectedProposalID != "" && proposalID != expectedProposalID) ||
			environmentRowID != e.EnvironmentRowID || applicationID != e.ApplicationID ||
			sourceID != e.SourceID || fingerprint != e.Fingerprint ||
			endpoint != e.EndpointBindingFingerprint || labelName != e.LabelName ||
			labelValue != e.LabelValue {
			return store.ErrConflict
		}
	}
	return rows.Err()
}

func bulkReservationActive(ctx context.Context, tx pgx.Tx, proposalID string,
	e bulkSilenceEvidence) error {
	var one int
	err := tx.QueryRow(ctx, `SELECT 1 FROM v3_bulk_group_silence_reservations
 WHERE proposal_id=$1 AND released_at IS NULL AND environment_row_id=$2
   AND application_id=$3 AND source_id=$4 AND fingerprint=$5
   AND endpoint_binding_fingerprint=$6 AND identity_label_name=$7 AND identity_label_value=$8
 FOR SHARE`, proposalID, e.EnvironmentRowID, e.ApplicationID, e.SourceID, e.Fingerprint,
		e.EndpointBindingFingerprint, e.LabelName, e.LabelValue).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.ErrConflict
	}
	return err
}

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
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return store.ErrConflict
		}
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
	if err = bulkReservationActive(ctx, tx, action.ProposalID, e); err != nil {
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
	return bulkReservationActive(ctx, tx, proposalID, e)
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
