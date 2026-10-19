package v3alertmanager

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/rbac"
	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	ProposedSilenceCancelVersion = 1
	ProposedSilenceCancelKind    = "alertmanager.silence.cancel"
	maxSilenceCancelEvidence     = 64 << 10
)

var silenceSessionHashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ProposedSilenceCancel is an immutable, review-only action. The upstream ID
// alone never proves ownership and this value grants no permission to expire
// a silence. An executor must verify approval, scope, operation, and a fresh
// upstream snapshot again before making the independent write.
type ProposedSilenceCancel struct {
	SchemaVersion          int       `json:"schema_version"`
	Kind                   string    `json:"kind"`
	IntentID               string    `json:"intent_id"`
	CreateOperationID      string    `json:"create_operation_id"`
	CreatePayloadHash      string    `json:"create_payload_hash"`
	BindingFingerprint     string    `json:"binding_fingerprint"`
	ActorID                string    `json:"actor_id"`
	EnvironmentID          string    `json:"environment_id"`
	EnvironmentLabel       string    `json:"environment_label"`
	ApplicationID          string    `json:"application_id"`
	ApplicationLabel       string    `json:"application_label"`
	OwnerTeamID            string    `json:"owner_team_id"`
	InvestigationID        string    `json:"investigation_id"`
	InvestigationUpdatedAt time.Time `json:"investigation_updated_at"`
	UpstreamID             string    `json:"upstream_id"`
	IdempotencyKey         string    `json:"idempotency_key"`
	RequestHash            string    `json:"request_hash"`
	ObservedDigest         string    `json:"observed_digest"`
	ObservedAt             time.Time `json:"observed_at"`
	Reason                 string    `json:"reason"`
}

// SilenceCancelReviewWriter is implemented by the trusted proposal producer.
// It must insert a pending v3_proposals row and its immutable
// v3_proposal_actions snapshot in the supplied transaction and return their
// durable identity and action digest. This domain authorizes proposals.change
// while holding the current grant sources locked.
// A nil writer makes the product DELETE unavailable rather than leaving a
// local reservation that could be mistaken for a submitted proposal.
type SilenceCancelReviewWriter interface {
	WritePendingSilenceCancelWithTx(context.Context, pgx.Tx, core.User, ProposedSilenceCancel) (SilenceProductProposal, error)
}

// CancelBindingFingerprint pins the trusted endpoint and catalog label
// binding used for the observed cancellation proposal. It is checked again
// at review/execution; a changed connector cannot silently inherit consent.
func (c *Client) CancelBindingFingerprint(envID, envLabel, appLabel, appID string) (string, error) {
	if c == nil {
		return "", ErrNotConfigured
	}
	ep, ok := c.endpoints[envID]
	if !ok {
		return "", ErrNotConfigured
	}
	return silenceProposalBindingFingerprint(ep, envID, envLabel, appLabel, appID)
}

type DatabaseSilenceCancelProduct struct {
	Ledger *DatabaseSilenceLedger
	Source *Client
	Review SilenceCancelReviewWriter
}

// SubmitSilenceCancel creates one pending human-review proposal and its
// immutable cancellation evidence atomically. sessionTokenHash is the SHA-256
// hash of the current cookie, supplied only by the authenticated HTTP layer.
// No Alertmanager write occurs on this path.
func (p *DatabaseSilenceCancelProduct) SubmitSilenceCancel(ctx context.Context, actor core.User,
	sessionTokenHash, environmentID, upstreamID, investigationID, idempotencyKey, reason string) (SilenceProductProposal, error) {
	var zero SilenceProductProposal
	if p == nil || p.Ledger == nil || p.Ledger.DB == nil || p.Ledger.Access == nil || p.Source == nil || p.Review == nil {
		return zero, ErrNotConfigured
	}
	if actor.ID == "" || actor.Disabled {
		return zero, store.ErrForbidden
	}
	if !silenceSessionHashPattern.MatchString(sessionTokenHash) {
		return zero, store.ErrSessionRevoked
	}
	if environmentID == "" || environmentID == "all" || !validSilenceID(upstreamID) ||
		investigationID == "" || len(investigationID) > 128 || !silenceKeyPattern.MatchString(idempotencyKey) ||
		reason == "" || strings.TrimSpace(reason) != reason || len(reason) > 1000 {
		return zero, ErrSilenceCreateInvalid
	}
	requestHash, _, err := digestSilence(struct {
		ActorID, EnvironmentID, UpstreamID, InvestigationID, IdempotencyKey, Reason string
	}{actor.ID, environmentID, upstreamID, investigationID, idempotencyKey, reason})
	if err != nil {
		return zero, err
	}
	tx, err := p.Ledger.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return zero, err
	}
	defer tx.Rollback(ctx)
	// Serialize actor/key retries without relying on a failed unique insert.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1),hashtext($2))`,
		"v3.silence.cancel:"+actor.ID, idempotencyKey); err != nil {
		return zero, err
	}
	// Prelock the catalog target before grant-source tables. The operation is
	// read without a lock first; its identity is checked again under FOR UPDATE.
	initial, err := scanSilenceCreate(tx.QueryRow(ctx, `SELECT `+silenceCreateColumns+`
 FROM v3_silence_create_operations WHERE environment_id=$1 AND upstream_id=$2`, environmentID, upstreamID))
	if errors.Is(err, pgx.ErrNoRows) {
		return zero, store.ErrNotFound
	}
	if err != nil {
		return zero, err
	}
	if initial.State != SilenceCreateConfirmed || initial.ApplicationID == "" || initial.ApplicationLabel == "" ||
		initial.EnvironmentLabel != p.Ledger.environmentLabels[environmentID] ||
		p.Ledger.applicationBindings[environmentID][initial.ApplicationLabel] != initial.ApplicationID {
		return zero, store.ErrNotFound
	}
	appID, teamID, err := p.Ledger.lockApplicationScope(ctx, tx, environmentID, initial.ApplicationLabel, initial.ApplicationID)
	if err != nil {
		if errors.Is(err, store.ErrConflict) || errors.Is(err, store.ErrNotFound) {
			return zero, store.ErrNotFound
		}
		return zero, err
	}
	if appID != initial.ApplicationID || teamID != initial.OwnerTeamID {
		return zero, store.ErrNotFound
	}
	op, err := scanSilenceCreate(tx.QueryRow(ctx, `SELECT `+silenceCreateColumns+`
 FROM v3_silence_create_operations WHERE id=$1 FOR UPDATE`, initial.ID))
	if errors.Is(err, pgx.ErrNoRows) {
		return zero, store.ErrNotFound
	}
	if err != nil {
		return zero, err
	}
	if op.State != SilenceCreateConfirmed || op.UpstreamID != upstreamID ||
		op.EnvironmentID != environmentID || op.ApplicationID != appID || op.OwnerTeamID != teamID ||
		op.PayloadHash != initial.PayloadHash {
		return zero, store.ErrNotFound
	}
	if err = p.Ledger.activeHuman(ctx, tx, actor); err != nil {
		return zero, err
	}
	var sessionOwner string
	err = tx.QueryRow(ctx, `SELECT user_id FROM sessions
 WHERE token_hash=$1 AND user_id=$2 AND expires_at>clock_timestamp() FOR SHARE`,
		sessionTokenHash, actor.ID).Scan(&sessionOwner)
	if errors.Is(err, pgx.ErrNoRows) {
		return zero, store.ErrSessionRevoked
	}
	if err != nil {
		return zero, err
	}
	// Grant writers must finish before these authority reads. The row locks
	// above hold the actor, session, application, and operation until commit.
	if _, err = tx.Exec(ctx, `LOCK TABLE rbac_user_roles,rbac_department_roles,v3_role_grants IN SHARE MODE`); err != nil {
		return zero, err
	}
	for _, d := range []rbac.V3Decision{
		{Module: "silences", Action: "view", EnvironmentID: environmentID, ApplicationID: appID},
		{Module: "silences", Action: "manage", EnvironmentID: environmentID, ApplicationID: appID},
		{Module: "proposals", Action: "change", EnvironmentID: environmentID, ApplicationID: appID},
	} {
		if err = authorizeSilenceCancelScope(ctx, tx, p.Ledger.Access, actor, d, teamID); err != nil {
			if errors.Is(err, store.ErrForbidden) && d.Module == "silences" {
				return zero, store.ErrNotFound
			}
			return zero, err
		}
	}
	var investigationUpdatedAt time.Time
	err = tx.QueryRow(ctx, `SELECT i.updated_at FROM investigations i
 JOIN application_environments e ON e.id=i.environment_id AND e.application_id=i.application_id
 WHERE i.id=$1 AND i.application_id=$2 AND i.status<>'closed'
   AND e.key=$3 AND NOT e.archived FOR SHARE OF i`, investigationID, appID, environmentID).Scan(&investigationUpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return zero, store.ErrNotFound
	}
	if err != nil {
		return zero, err
	}
	bindingFingerprint, err := p.Source.CancelBindingFingerprint(environmentID, op.EnvironmentLabel,
		op.ApplicationLabel, appID)
	if err != nil {
		return zero, err
	}
	// An authorized retry sees its first pending proposal, never a new intent.
	var existingID, existingOperation, existingHash, existingProposal, existingDigest string
	err = tx.QueryRow(ctx, `SELECT id,create_operation_id,request_hash,proposal_id,observed_digest
 FROM v3_silence_cancel_intents WHERE actor_id=$1 AND idempotency_key=$2 FOR SHARE`,
		actor.ID, idempotencyKey).Scan(&existingID, &existingOperation, &existingHash, &existingProposal, &existingDigest)
	if err == nil {
		if existingOperation != op.ID || existingHash != requestHash {
			return zero, store.ErrConflict
		}
		result, loadErr := loadPendingSilenceCancelProposal(ctx, tx, existingProposal, actor, op.SilenceCreateOperation,
			investigationID, idempotencyKey, existingID, requestHash, bindingFingerprint, existingDigest, reason)
		if loadErr != nil {
			return zero, loadErr
		}
		if err = silenceCancelSessionCurrent(ctx, tx, sessionTokenHash, actor.ID); err != nil {
			return zero, err
		}
		if err = tx.Commit(ctx); err != nil {
			return zero, err
		}
		return result, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return zero, err
	}
	var occupiedKey bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM v3_proposal_actions
 WHERE creator_id=$1 AND idempotency_key=$2)`, actor.ID, idempotencyKey).Scan(&occupiedKey)
	if err != nil {
		return zero, err
	}
	if occupiedKey {
		return zero, store.ErrConflict
	}
	var pending bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM v3_silence_cancel_intents c
 JOIN v3_proposals p ON p.id=c.proposal_id
 WHERE c.create_operation_id=$1 AND p.status='pending')`, op.ID).Scan(&pending)
	if err != nil {
		return zero, err
	}
	if pending {
		return zero, store.ErrConflict
	}
	// Keep the full provider object from one complete environment-bound read.
	// The typed projection below proves every field in the original create
	// request, while the raw snapshot preserves additional provider fields.
	target, snapshot, err := p.Source.observeCancelSilence(ctx, environmentID, upstreamID, op.ID)
	if err != nil {
		return zero, err
	}
	if (target.Status.State != "active" && target.Status.State != "pending") || !target.EndsAt.After(p.Ledger.currentTime()) ||
		!matchesCreateSnapshot(op.SilenceCreateOperation, target) ||
		classifySilence(target, op.EnvironmentLabel, environmentID, p.Ledger.environmentLabels) != scopeOwn {
		return zero, store.ErrConflict
	}
	appLabel, exact, ambiguous := exactSilenceApplication(target)
	if !exact || ambiguous || appLabel != op.ApplicationLabel {
		return zero, store.ErrConflict
	}
	if len(snapshot) > maxSilenceCancelEvidence {
		return zero, store.ErrConflict
	}
	observedSum := sha256.Sum256(snapshot)
	observedDigest := hex.EncodeToString(observedSum[:])
	observedAt := p.Ledger.currentTime()
	intentID := store.ID()
	action := ProposedSilenceCancel{SchemaVersion: ProposedSilenceCancelVersion, Kind: ProposedSilenceCancelKind,
		IntentID: intentID, CreateOperationID: op.ID, CreatePayloadHash: op.PayloadHash,
		BindingFingerprint: bindingFingerprint,
		ActorID:            actor.ID, EnvironmentID: environmentID,
		EnvironmentLabel: op.EnvironmentLabel, ApplicationID: appID, ApplicationLabel: appLabel,
		OwnerTeamID: teamID, InvestigationID: investigationID, InvestigationUpdatedAt: investigationUpdatedAt.UTC(), UpstreamID: upstreamID,
		IdempotencyKey: idempotencyKey, RequestHash: requestHash, ObservedDigest: observedDigest,
		ObservedAt: observedAt, Reason: reason}
	result, err := p.Review.WritePendingSilenceCancelWithTx(ctx, tx, actor, action)
	if err != nil {
		return zero, silenceCancelConflictError(err)
	}
	if result.ProposalID == "" || result.ActionDigest == "" || result.Status != "pending" {
		return zero, store.ErrConflict
	}
	verified, err := loadPendingSilenceCancelProposal(ctx, tx, result.ProposalID, actor, op.SilenceCreateOperation,
		investigationID, idempotencyKey, intentID, requestHash, bindingFingerprint, observedDigest, reason)
	if err != nil || verified.ActionDigest != result.ActionDigest {
		return zero, store.ErrConflict
	}
	_, err = tx.Exec(ctx, `INSERT INTO v3_silence_cancel_intents
 (id,create_operation_id,proposal_id,actor_id,environment_id,application_id,investigation_id,
  upstream_id,idempotency_key,request_hash,observed_digest,observed_snapshot,observed_at,reason)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12::jsonb,$13,$14)`,
		intentID, op.ID, result.ProposalID, actor.ID, environmentID, appID, investigationID,
		upstreamID, idempotencyKey, requestHash, observedDigest, snapshot, observedAt, reason)
	if err != nil {
		return zero, silenceCancelConflictError(err)
	}
	if err = auditSilenceCreate(ctx, tx, actor.ID, appID, "v3.silence_cancel.proposed", intentID,
		"pending", environmentID, "pending_review"); err != nil {
		return zero, err
	}
	if err = silenceCancelSessionCurrent(ctx, tx, sessionTokenHash, actor.ID); err != nil {
		return zero, err
	}
	if err = tx.Commit(ctx); err != nil {
		return zero, err
	}
	return result, nil
}

func authorizeSilenceCancelScope(ctx context.Context, tx pgx.Tx, access SilenceWriteAccess,
	actor core.User, decision rbac.V3Decision, teamID string) error {
	err := access.AuthorizeWithTx(ctx, tx, actor, decision)
	if errors.Is(err, store.ErrForbidden) && teamID != "" {
		decision.ApplicationID = ""
		decision.TeamID = teamID
		err = access.AuthorizeWithTx(ctx, tx, actor, decision)
	}
	return err
}

func silenceCancelSessionCurrent(ctx context.Context, tx pgx.Tx, tokenHash, actorID string) error {
	var active bool
	err := tx.QueryRow(ctx, `SELECT expires_at>clock_timestamp() FROM sessions
 WHERE token_hash=$1 AND user_id=$2`, tokenHash, actorID).Scan(&active)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !active {
		return store.ErrSessionRevoked
	}
	return err
}

func loadPendingSilenceCancelProposal(ctx context.Context, tx pgx.Tx, proposalID string, actor core.User,
	op SilenceCreateOperation, investigationID, key, intentID, requestHash, bindingFingerprint,
	observedDigest, reason string) (SilenceProductProposal, error) {
	var result SilenceProductProposal
	var kind, creator, env, app, inv, storedKey, sourceKind, status, storedHash, storedBinding, reviewDigest string
	var version int
	var investigationSnapshot time.Time
	var actionRaw, contentRaw []byte
	err := tx.QueryRow(ctx, `SELECT p.id,p.status,a.action_digest,a.action_kind,a.creator_id,
 a.environment_id,a.application_id,a.investigation_id,a.idempotency_key,a.source_kind,
 a.schema_version,a.request_hash,a.binding_fingerprint,a.action,a.review_content_digest,p.content,
 a.investigation_updated_at
 FROM v3_proposals p JOIN v3_proposal_actions a ON a.proposal_id=p.id
 WHERE p.id=$1 AND p.created_by=$2 AND p.creator_kind='user'
   AND p.environment_id=$3 AND p.application_id=$4 AND p.investigation_id=$5
	 FOR SHARE OF p,a`, proposalID, actor.ID, op.EnvironmentID, op.ApplicationID, investigationID).
		Scan(&result.ProposalID, &status, &result.ActionDigest, &kind, &creator, &env, &app, &inv,
			&storedKey, &sourceKind, &version, &storedHash, &storedBinding, &actionRaw, &reviewDigest,
			&contentRaw, &investigationSnapshot)
	if errors.Is(err, pgx.ErrNoRows) {
		return SilenceProductProposal{}, store.ErrConflict
	}
	if err != nil {
		return SilenceProductProposal{}, err
	}
	if status != "pending" || kind != ProposedSilenceCancelKind || version != ProposedSilenceCancelVersion ||
		creator != actor.ID || env != op.EnvironmentID || app != op.ApplicationID ||
		inv != investigationID || storedKey != key || sourceKind != "human_request" ||
		storedHash != requestHash || storedBinding != bindingFingerprint {
		return SilenceProductProposal{}, store.ErrConflict
	}
	var action ProposedSilenceCancel
	if json.Unmarshal(actionRaw, &action) != nil ||
		action.SchemaVersion != ProposedSilenceCancelVersion || action.Kind != ProposedSilenceCancelKind ||
		action.IntentID != intentID || action.CreateOperationID != op.ID ||
		action.CreatePayloadHash != op.PayloadHash || action.BindingFingerprint != bindingFingerprint ||
		action.ActorID != actor.ID || action.EnvironmentID != op.EnvironmentID ||
		action.EnvironmentLabel != op.EnvironmentLabel || action.ApplicationID != op.ApplicationID ||
		action.ApplicationLabel != op.ApplicationLabel || action.OwnerTeamID != op.OwnerTeamID ||
		action.InvestigationID != investigationID || action.UpstreamID != op.UpstreamID ||
		action.IdempotencyKey != key || action.RequestHash != requestHash ||
		action.ObservedDigest != observedDigest || action.ObservedAt.IsZero() || action.Reason != reason ||
		!action.InvestigationUpdatedAt.Equal(investigationSnapshot) {
		return SilenceProductProposal{}, store.ErrConflict
	}
	canonicalAction, err := canonicalSilenceCancelJSON(actionRaw)
	structAction, marshalErr := json.Marshal(action)
	canonicalStructAction, canonicalErr := canonicalSilenceCancelJSON(structAction)
	if err != nil || marshalErr != nil || canonicalErr != nil || !bytes.Equal(canonicalStructAction, canonicalAction) ||
		silenceCancelDigest("aiops.v3.proposal.action.v1", canonicalAction) != result.ActionDigest {
		return SilenceProductProposal{}, store.ErrConflict
	}
	canonicalContent, err := canonicalSilenceCancelJSON(contentRaw)
	if err != nil || silenceCancelDigest("aiops.v3.proposal.review.v1", canonicalContent) != reviewDigest {
		return SilenceProductProposal{}, store.ErrConflict
	}
	result.Status = status
	return result, nil
}

func canonicalSilenceCancelJSON(raw []byte) ([]byte, error) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

func silenceCancelDigest(domain string, canonical []byte) string {
	sum := sha256.Sum256(append([]byte(domain+"\x00"), canonical...))
	return hex.EncodeToString(sum[:])
}

func silenceCancelConflictError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return store.ErrConflict
	}
	return err
}

// observeCancelSilence reads one complete Alertmanager snapshot and requires
// the selected UUID and operation marker to identify exactly one same object.
// JSONB storage normalizes key order, so the evidence digest uses canonical
// JSON rather than the provider's incidental whitespace/key order.
func (c *Client) observeCancelSilence(ctx context.Context, environmentID, upstreamID, operationID string) (Silence, []byte, error) {
	if c == nil {
		return Silence{}, nil, ErrNotConfigured
	}
	body, err := c.get(ctx, environmentID, "/api/v2/silences")
	if err != nil {
		return Silence{}, nil, err
	}
	var records []json.RawMessage
	if err = decodeOne(body, &records); err != nil || records == nil {
		return Silence{}, nil, errors.New("invalid Alertmanager silence response")
	}
	var target Silence
	var snapshot []byte
	for _, raw := range records {
		var item Silence
		if json.Unmarshal(raw, &item) != nil || !validSilenceID(item.ID) ||
			item.StartsAt.IsZero() || item.EndsAt.IsZero() || item.UpdatedAt.IsZero() ||
			(item.Status.State != "active" && item.Status.State != "pending" && item.Status.State != "expired") ||
			len(item.Matchers) == 0 || len(item.Matchers) > 100 {
			return Silence{}, nil, errors.New("invalid Alertmanager silence record")
		}
		for _, m := range item.Matchers {
			if m.Name == "" {
				return Silence{}, nil, errors.New("invalid Alertmanager silence matcher")
			}
		}
		if item.ID != upstreamID && item.Annotations[silenceOperationAnnotation] != operationID {
			continue
		}
		if snapshot != nil || item.ID != upstreamID || item.Annotations[silenceOperationAnnotation] != operationID {
			return Silence{}, nil, store.ErrConflict
		}
		snapshot, err = canonicalSilenceCancelJSON(raw)
		if err != nil {
			return Silence{}, nil, errors.New("invalid Alertmanager silence record")
		}
		target = item
	}
	if snapshot == nil {
		return Silence{}, nil, store.ErrNotFound
	}
	return target, snapshot, nil
}
