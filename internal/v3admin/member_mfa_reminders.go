package v3admin

import (
	"context"
	"errors"
	"io"
	"net/http"
	"regexp"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/store"
)

var errMFAReminderUnavailable = errors.New("authoritative MFA evidence or Feishu delivery unavailable")
var errMFAReminderLimited = errors.New("MFA reminder rate limit reached")
var errMFAReminderUncertain = errors.New("MFA reminder delivery outcome uncertain")

// An evidence source must be authoritative for the configured product instance,
// not a projection of this product's local TOTP table. The snapshot covers
// every requested active visible human, including externally managed accounts.
type MFAEvidenceSource interface {
	Snapshot(context.Context, string, []string) (MFASnapshot, error)
}

type MFAMemberEvidence struct {
	Enrollment   string `json:"enrollment"`     // enrolled, unenrolled, unknown
	FactorKind   string `json:"factor_kind"`    // passkey, totp, none, unknown
	FeishuOpenID string `json:"feishu_open_id"` // current verified binding, never inferred from email
}

type MFASnapshot struct {
	Complete   bool                         `json:"complete"`
	ObservedAt time.Time                    `json:"observed_at"`
	GraceDays  *int                         `json:"grace_days"`
	Members    map[string]MFAMemberEvidence `json:"members"`
}

var feishuOpenIDPattern = regexp.MustCompile(`^ou_[A-Za-z0-9]{8,128}$`)

// ValidateMFASnapshot is shared by the list and reminder paths. The source
// must return a fresh, complete, exact set; absence never means unenrolled.
func ValidateMFASnapshot(snapshot MFASnapshot, requested []string, now time.Time) error {
	if !snapshot.Complete || snapshot.GraceDays == nil || *snapshot.GraceDays < 0 ||
		snapshot.ObservedAt.IsZero() || snapshot.ObservedAt.After(now.Add(time.Minute)) ||
		now.Sub(snapshot.ObservedAt) > 5*time.Minute || len(snapshot.Members) != len(requested) {
		return errMFAReminderUnavailable
	}
	seen := make(map[string]bool, len(requested))
	for _, id := range requested {
		if id == "" || seen[id] {
			return errMFAReminderUnavailable
		}
		seen[id] = true
		evidence, ok := snapshot.Members[id]
		if !ok || !validMFAMemberEvidence(evidence) {
			return errMFAReminderUnavailable
		}
	}
	for id := range snapshot.Members {
		if !seen[id] {
			return errMFAReminderUnavailable
		}
	}
	return nil
}

func validMFAMemberEvidence(v MFAMemberEvidence) bool {
	if v.FeishuOpenID != "" && !feishuOpenIDPattern.MatchString(v.FeishuOpenID) {
		return false
	}
	switch v.Enrollment {
	case "enrolled":
		return v.FactorKind == "passkey" || v.FactorKind == "totp" || v.FactorKind == "unknown"
	case "unenrolled":
		return v.FactorKind == "none"
	default:
		return false
	}
}

type FeishuMFAReminder struct {
	OpenID      string
	Environment string
	DeliveryID  string
}

type FeishuMFAReminderReceipt struct {
	MessageID string
}

// The sender must return only after Feishu accepts the message and supplies
// a message ID. This is API acceptance, not proof the recipient read it.
type FeishuMFAReminderSender interface {
	SendMFAReminder(context.Context, FeishuMFAReminder) (FeishuMFAReminderReceipt, error)
}

// MFAReminderService is injected by the composition root only when both
// authoritative evidence and a Feishu app bot are configured.
type MFAReminderService struct {
	Sender FeishuMFAReminderSender
}

func (s *Service) remindMembersMFA(w http.ResponseWriter, r *http.Request) {
	envs, ok := scoped(r)
	if !ok || len(envs) != 1 {
		httpapi.Error(w, 422, "environment_required", "Select one environment")
		return
	}
	if r.ContentLength > 0 || (r.ContentLength < 0 && r.Body != nil && bodyHasContent(r.Body)) {
		httpapi.Error(w, 422, "invalid_request", "No request body is accepted")
		return
	}
	if s.MFAEvidence == nil || s.MFAReminders == nil || s.MFAReminders.Sender == nil {
		mfaReminderError(w, errMFAReminderUnavailable)
		return
	}
	actor, token := httpapi.CurrentUser(r), roleSessionToken(r)
	ids, err := s.mfaReminderTargets(r.Context(), actor, token, envs[0])
	if err != nil {
		mfaReminderError(w, err)
		return
	}
	snapshot, err := s.MFAEvidence.Snapshot(r.Context(), envs[0], ids)
	if err != nil || ValidateMFASnapshot(snapshot, ids, time.Now()) != nil {
		mfaReminderError(w, errMFAReminderUnavailable)
		return
	}
	// A complete source snapshot is required before the first external send.
	candidates := make([]string, 0, len(ids))
	feishuRecipients := map[string]bool{}
	for _, id := range ids {
		if snapshot.Members[id].Enrollment == "unenrolled" {
			openID := snapshot.Members[id].FeishuOpenID
			if !feishuOpenIDPattern.MatchString(openID) || feishuRecipients[openID] {
				mfaReminderError(w, errMFAReminderUnavailable)
				return
			}
			feishuRecipients[openID] = true
			candidates = append(candidates, id)
		}
	}
	// Keep the entire external batch within the five-minute evidence window
	// even when individual calls approach their eight-second timeout.
	if len(candidates) > 20 {
		mfaReminderError(w, errMFAReminderLimited)
		return
	}
	sent := 0
	for _, id := range candidates {
		if time.Since(snapshot.ObservedAt) > 5*time.Minute {
			mfaReminderError(w, errMFAReminderUnavailable)
			return
		}
		attemptID, skip, err := s.prepareMFAReminder(r.Context(), actor, token, envs[0], id, snapshot.ObservedAt)
		if err != nil {
			mfaReminderError(w, err)
			return
		}
		if skip {
			continue // A recent accepted send is not counted again.
		}
		started, sendErr := s.sendAuthorizedMFAReminder(r.Context(), actor, token, envs[0], id,
			attemptID, snapshot.ObservedAt, snapshot.Members[id].FeishuOpenID)
		if sendErr != nil {
			if !started || errors.Is(sendErr, errMFAReminderUnavailable) {
				// The concrete sender returns unavailable only before issuing
				// the Feishu message request.
				s.markMFAReminderFailed(attemptID)
				mfaReminderError(w, sendErr)
				return
			}
			s.markMFAReminderUncertain(attemptID)
			mfaReminderError(w, errMFAReminderUncertain)
			return
		}
		sent++
	}
	if sent == 0 {
		// The source read may have taken time even when nobody needs a
		// message. Do not return an authorized-looking success for a revoked
		// actor or a changed visible population.
		if time.Since(snapshot.ObservedAt) > 5*time.Minute {
			mfaReminderError(w, errMFAReminderUnavailable)
			return
		}
		latest, err := s.mfaReminderTargets(r.Context(), actor, token, envs[0])
		if err != nil {
			mfaReminderError(w, err)
			return
		}
		if !slices.Equal(ids, latest) {
			mfaReminderError(w, errMFAReminderUnavailable)
			return
		}
	}
	httpapi.WriteJSON(w, 200, map[string]int{"sent": sent})
}

func bodyHasContent(body io.Reader) bool {
	var one [1]byte
	n, _ := body.Read(one[:])
	return n > 0
}

func mfaReminderError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrSessionRevoked):
		httpapi.Error(w, 401, "unauthorized", "Login required")
	case errors.Is(err, store.ErrForbidden):
		httpapi.Error(w, 403, "forbidden", "Insufficient permission")
	case errors.Is(err, errMFAReminderLimited):
		httpapi.Error(w, 429, "mfa_reminder_rate_limited", "Try again later")
	case errors.Is(err, errMFAReminderUncertain):
		httpapi.Error(w, 503, "mfa_reminder_uncertain", "Delivery outcome requires reconciliation")
	default:
		httpapi.Error(w, 503, "mfa_reminder_unavailable", "Authoritative MFA evidence or Feishu delivery is unavailable")
	}
}

// mfaReminderTargets mirrors the directory's environment admission rule and
// only includes active human accounts. No email or Feishu identity is guessed.
func (s *Service) mfaReminderTargets(ctx context.Context, actor core.User, token, env string) ([]string, error) {
	tx, err := s.Store.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if err = memberIdentityLocks(ctx, tx, false); err != nil {
		return nil, err
	}
	if err = s.authorizeInvitation(ctx, tx, actor, token, env); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT DISTINCT u.id FROM users u
 WHERE u.deleted_at IS NULL AND NOT u.disabled AND u.v3_identity_kind='human'
		AND u.source<>'settings_demo' AND (
		 NOT EXISTS(SELECT 1 FROM v3_effective_role_grants g WHERE g.user_id=u.id
		  AND g.module='environment' AND g.action='view'
		  AND g.scope_kind IN ('global','environment'))
		 OR EXISTS(SELECT 1 FROM v3_effective_role_grants g WHERE g.user_id=u.id
		  AND g.module='environment' AND g.action='view'
		  AND (g.scope_kind='global' OR (g.scope_kind='environment' AND g.scope_id=$1))))
 ORDER BY u.id`, env)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	if err = store.LockActiveSession(ctx, tx, token, actor.ID); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return ids, nil
}

func (s *Service) prepareMFAReminder(ctx context.Context, actor core.User, token, env, target string, evidenceAt time.Time) (string, bool, error) {
	tx, err := s.Store.DB.Begin(ctx)
	if err != nil {
		return "", false, err
	}
	defer tx.Rollback(ctx)
	if err = memberIdentityLocks(ctx, tx, false); err != nil {
		return "", false, err
	}
	if err = s.authorizeInvitation(ctx, tx, actor, token, env); err != nil {
		return "", false, err
	}
	// The singleton ledger row serializes concurrent requests for this target.
	var active bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users u
 WHERE u.id=$1 AND u.deleted_at IS NULL AND NOT u.disabled
		 AND u.v3_identity_kind='human' AND u.source<>'settings_demo'
		 AND (NOT EXISTS(SELECT 1 FROM v3_effective_role_grants g WHERE g.user_id=u.id
		  AND g.module='environment' AND g.action='view'
		  AND g.scope_kind IN ('global','environment'))
		 OR EXISTS(SELECT 1 FROM v3_effective_role_grants g WHERE g.user_id=u.id
		  AND g.module='environment' AND g.action='view'
		  AND (g.scope_kind='global' OR (g.scope_kind='environment' AND g.scope_id=$2)))))`, target, env).Scan(&active); err != nil {
		return "", false, err
	}
	if !active {
		return "", false, store.ErrForbidden
	}
	// Serialize the aggregate actor limit across different target IDs.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(752019000)`); err != nil {
		return "", false, err
	}
	var recent int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM v3_mfa_reminder_attempts
 WHERE actor_id=$1 AND created_at>clock_timestamp()-interval '1 hour'`, actor.ID).Scan(&recent); err != nil {
		return "", false, err
	}
	if recent >= 100 {
		return "", false, errMFAReminderLimited
	}
	// pg advisory lock is scoped per target and environment, including when no
	// ledger row exists yet. It prevents duplicate concurrent first sends.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,752018999))`, env+"\x00"+target); err != nil {
		return "", false, err
	}
	var status string
	var created time.Time
	err = tx.QueryRow(ctx, `SELECT status,created_at FROM v3_mfa_reminder_attempts
 WHERE environment_id=$1 AND target_id=$2 ORDER BY created_at DESC LIMIT 1 FOR UPDATE`, env, target).Scan(&status, &created)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", false, err
	}
	if err == nil {
		if status == "accepted" && time.Since(created) < 24*time.Hour {
			return "", true, nil
		}
		if status != "accepted" && status != "failed" {
			return "", false, errMFAReminderUncertain
		}
	}
	id := store.ID()
	if _, err = tx.Exec(ctx, `INSERT INTO v3_mfa_reminder_attempts
 (id,environment_id,target_id,actor_id,status,mfa_observed_at)
 VALUES($1,$2,$3,$4,'pending',$5)`, id, env, target, actor.ID, evidenceAt); err != nil {
		return "", false, err
	}
	if err = store.LockActiveSession(ctx, tx, token, actor.ID); err != nil {
		return "", false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return "", false, err
	}
	return id, false, nil
}

func (s *Service) markMFAReminderUncertain(id string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, _ = s.Store.DB.Exec(ctx, `UPDATE v3_mfa_reminder_attempts SET status='uncertain'
 WHERE id=$1 AND status='pending'`, id)
}

func (s *Service) markMFAReminderFailed(id string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, _ = s.Store.DB.Exec(ctx, `UPDATE v3_mfa_reminder_attempts SET status='failed'
 WHERE id=$1 AND status='pending'`, id)
}

// sendAuthorizedMFAReminder keeps the current actor, role matrix, target, and
// pending attempt locked across the external send. A revocation that commits
// after prepare but before these locks is observed before Feishu is called;
// one that races after the locks waits until the send and audit finish.
// started means the sender was invoked, so an unknown outcome must not be
// retried. The concrete sender's unavailable error is a pre-message failure.
func (s *Service) sendAuthorizedMFAReminder(ctx context.Context, actor core.User, token, env, target, id string,
	evidenceAt time.Time, openID string) (started bool, err error) {
	operationCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	tx, err := s.Store.DB.Begin(operationCtx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(operationCtx)
	if err = memberIdentityLocks(operationCtx, tx, false); err != nil {
		return false, err
	}
	if err = s.authorizeInvitation(operationCtx, tx, actor, token, env); err != nil {
		return false, err
	}
	// Lock the concrete recipient account as well as the actor account.
	// Identity writers take the advisory locks above; direct disable/delete
	// writers also have to wait for this row lock.
	var activeID string
	err = tx.QueryRow(operationCtx, `SELECT u.id FROM users u
 WHERE u.id=$1 AND u.deleted_at IS NULL AND NOT u.disabled
 AND u.v3_identity_kind='human' AND u.source<>'settings_demo'
 AND (NOT EXISTS(SELECT 1 FROM v3_effective_role_grants g WHERE g.user_id=u.id
  AND g.module='environment' AND g.action='view'
  AND g.scope_kind IN ('global','environment'))
 OR EXISTS(SELECT 1 FROM v3_effective_role_grants g WHERE g.user_id=u.id
  AND g.module='environment' AND g.action='view'
  AND (g.scope_kind='global' OR (g.scope_kind='environment' AND g.scope_id=$2))))
 FOR SHARE OF u`, target, env).Scan(&activeID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, store.ErrForbidden
	}
	if err != nil {
		return false, err
	}
	var recordedAt time.Time
	err = tx.QueryRow(operationCtx, `SELECT mfa_observed_at FROM v3_mfa_reminder_attempts
 WHERE id=$1 AND environment_id=$2 AND target_id=$3 AND actor_id=$4 AND status='pending'
 FOR UPDATE`, id, env, target, actor.ID).Scan(&recordedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, errMFAReminderUncertain
	}
	if err != nil {
		return false, err
	}
	deadline, _ := operationCtx.Deadline()
	if !recordedAt.Equal(evidenceAt) || evidenceAt.IsZero() || time.Since(evidenceAt) > 5*time.Minute ||
		!feishuOpenIDPattern.MatchString(openID) || time.Until(deadline) < 9*time.Second {
		return false, errMFAReminderUnavailable
	}
	// A session expiring during the bounded send cannot authorize that send.
	// The extra margin covers audit/commit and is checked again afterwards.
	var sessionLongEnough bool
	if err = tx.QueryRow(operationCtx, `SELECT expires_at>clock_timestamp()+interval '10 seconds'
 FROM sessions WHERE token_hash=$1 AND user_id=$2 FOR SHARE`, store.Hash(token), actor.ID).Scan(&sessionLongEnough); err != nil {
		return false, err
	}
	if !sessionLongEnough {
		return false, store.ErrSessionRevoked
	}
	sendCtx, stopSend := context.WithTimeout(operationCtx, 8*time.Second)
	defer stopSend()
	started = true
	receipt, sendErr := s.MFAReminders.Sender.SendMFAReminder(sendCtx, FeishuMFAReminder{
		OpenID: openID, Environment: env, DeliveryID: id,
	})
	if sendErr != nil {
		return true, sendErr
	}
	if receipt.MessageID == "" {
		return true, errMFAReminderUncertain
	}
	var changed string
	if err = tx.QueryRow(operationCtx, `UPDATE v3_mfa_reminder_attempts
 SET status='accepted',accepted_at=clock_timestamp(),feishu_message_id=$1
 WHERE id=$2 AND environment_id=$3 AND target_id=$4 AND actor_id=$5 AND status='pending'
	 RETURNING id`, receipt.MessageID, id, env, target, actor.ID).Scan(&changed); err != nil {
		return true, errMFAReminderUncertain
	}
	if _, err = tx.Exec(operationCtx, `INSERT INTO management_audit(actor_id,action,target,outcome,changes)
 VALUES($1,'v3.member.mfa_reminder.feishu_accepted',$2,'success',
 jsonb_build_object('environment',$3,'attempt_id',$4,'feishu_message_id',$5,
 'mfa_observed_at',(SELECT mfa_observed_at FROM v3_mfa_reminder_attempts WHERE id=$4)))`,
		actor.ID, target, env, id, receipt.MessageID); err != nil {
		return true, errMFAReminderUncertain
	}
	if err = store.LockActiveSession(operationCtx, tx, token, actor.ID); err != nil {
		return true, errMFAReminderUncertain
	}
	if err = tx.Commit(operationCtx); err != nil {
		return true, errMFAReminderUncertain
	}
	return true, nil
}
