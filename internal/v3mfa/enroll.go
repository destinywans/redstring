package v3mfa

import (
	"context"
	"encoding/base32"
	"errors"
	"net/url"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/store"
)

func (s *Service) Status(ctx context.Context, a Actor) (Status, error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return Status{}, err
	}
	defer tx.Rollback(ctx)
	now := s.clock()
	if _, _, err = s.lockedActor(ctx, tx, a, now, false); err != nil {
		return Status{}, err
	}
	f, err := s.loadFactor(ctx, tx, a.UserID)
	if _, _, authErr := s.lockedActor(ctx, tx, a, s.clock(), false); authErr != nil {
		return Status{}, authErr
	}
	now = s.clock()
	if errors.Is(err, pgx.ErrNoRows) {
		return Status{State: "none"}, s.commitCurrent(ctx, tx, a, false, time.Time{}, nil)
	}
	if err != nil {
		return Status{}, err
	}
	if _, err = s.decryptFactor(f, a.UserID); err != nil {
		return Status{}, err
	}
	result := Status{State: f.state}
	if f.state == "pending" && !now.Before(f.pendingExpiresAt) {
		result.State = "expired"
	}
	if f.state == "active" {
		err = tx.QueryRow(ctx, `SELECT count(*) FROM v3_mfa_recovery_codes
		 WHERE instance_id=$1 AND user_id=$2 AND generation=$3 AND consumed_at IS NULL`,
			s.instanceID, a.UserID, f.generation).Scan(&result.RecoveryRemaining)
		if err != nil {
			return Status{}, err
		}
	}
	return result, s.commitCurrent(ctx, tx, a, false, time.Time{}, nil)
}

// BeginEnrollment returns the seed exactly once. An unexpired pending
// enrollment is never re-disclosed or silently replaced after a lost reply.
func (s *Service) BeginEnrollment(ctx context.Context, a Actor) (Enrollment, error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return Enrollment{}, err
	}
	defer tx.Rollback(ctx)
	now := s.clock()
	email, sessionHash, err := s.lockedActor(ctx, tx, a, now, true)
	if err != nil {
		return Enrollment{}, err
	}
	f, err := s.loadFactor(ctx, tx, a.UserID)
	if _, _, authErr := s.lockedActor(ctx, tx, a, s.clock(), true); authErr != nil {
		return Enrollment{}, authErr
	}
	now = s.clock()
	if err == nil {
		if f.state == "active" || now.Before(f.pendingExpiresAt) {
			return Enrollment{}, ErrConflict
		}
		if _, err = tx.Exec(ctx, `DELETE FROM v3_mfa_totp WHERE instance_id=$1 AND user_id=$2`,
			s.instanceID, a.UserID); err != nil {
			return Enrollment{}, err
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return Enrollment{}, err
	}
	seed, err := randomBytes(seedBytes)
	if err != nil {
		return Enrollment{}, ErrUnavailable
	}
	generation := store.ID()
	nonce, ciphertext, err := s.seal(seed, "pending", s.instanceID, a.UserID,
		generation, sessionHash, a.Tier, a.EnvironmentID)
	if err != nil {
		return Enrollment{}, ErrUnavailable
	}
	expiresAt := now.Add(enrollmentLifetime)
	_, err = tx.Exec(ctx, `INSERT INTO v3_mfa_totp
	 (instance_id,user_id,generation,state,seed_nonce,seed_ciphertext,key_id,
	  bound_session_hash,bound_tier,bound_environment_id,pending_expires_at,created_at,updated_at)
	 VALUES($1,$2,$3,'pending',$4,$5,$6,$7,$8,$9,$10,$11,$11)`,
		s.instanceID, a.UserID, generation, nonce, ciphertext, s.keyID,
		sessionHash, a.Tier, a.EnvironmentID, expiresAt, now)
	if err != nil {
		return Enrollment{}, err
	}
	if err = s.audit(ctx, tx, a, sessionHash, "mfa.totp.enrollment_started", "success", "totp"); err != nil {
		return Enrollment{}, err
	}
	if err = s.commitCurrent(ctx, tx, a, true, expiresAt, nil); err != nil {
		return Enrollment{}, err
	}
	secret := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(seed)
	q := url.Values{"secret": {secret}, "issuer": {"AIOps Canvas"},
		"algorithm": {"SHA1"}, "digits": {"6"}, "period": {"30"}}
	uri := "otpauth://totp/" + url.PathEscape("AIOps Canvas "+s.instanceID+":"+email) + "?" + q.Encode()
	return Enrollment{Secret: secret, OTPAuthURL: uri, ExpiresAt: expiresAt}, nil
}

// ConfirmEnrollment consumes the confirming time step and returns newly
// generated recovery codes once, after seed, code hashes and audit commit.
func (s *Service) ConfirmEnrollment(ctx context.Context, a Actor, code string) (Confirmation, error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return Confirmation{}, err
	}
	defer tx.Rollback(ctx)
	now := s.clock()
	_, sessionHash, err := s.lockedActor(ctx, tx, a, now, true)
	if err != nil {
		return Confirmation{}, err
	}
	f, err := s.loadFactor(ctx, tx, a.UserID)
	if _, _, authErr := s.lockedActor(ctx, tx, a, s.clock(), true); authErr != nil {
		return Confirmation{}, authErr
	}
	now = s.clock()
	if errors.Is(err, pgx.ErrNoRows) {
		return Confirmation{}, ErrConflict
	}
	if err != nil {
		return Confirmation{}, err
	}
	if f.state != "pending" || f.boundSession != sessionHash || f.boundTier != a.Tier ||
		f.boundEnvironment != a.EnvironmentID || !now.Before(f.pendingExpiresAt) {
		return Confirmation{}, ErrConflict
	}
	seed, err := s.decryptFactor(f, a.UserID)
	if err != nil {
		return Confirmation{}, err
	}
	if !f.lockedUntil.IsZero() && now.Before(f.lockedUntil) {
		return Confirmation{}, s.lockedDenial(ctx, tx, a, sessionHash, "confirm")
	}
	step, valid := matchTOTP(seed, code, now, -1)
	if !valid {
		return Confirmation{}, s.failedAttempt(ctx, tx, a, sessionHash, f, now, "confirm")
	}
	codes, salts, hashes, err := newRecoveryCodes()
	if err != nil {
		return Confirmation{}, ErrUnavailable
	}
	nonce, ciphertext, err := s.seal(seed, "active", s.instanceID, a.UserID, f.generation)
	if err != nil {
		return Confirmation{}, ErrUnavailable
	}
	_, err = tx.Exec(ctx, `UPDATE v3_mfa_totp SET state='active',seed_nonce=$3,
	 seed_ciphertext=$4,bound_session_hash=NULL,bound_tier=NULL,bound_environment_id=NULL,
	 pending_expires_at=NULL,last_accepted_step=$5,failed_attempts=0,locked_until=NULL,
	 activated_at=$6,updated_at=$6 WHERE instance_id=$1 AND user_id=$2`,
		s.instanceID, a.UserID, nonce, ciphertext, step, now)
	if err != nil {
		return Confirmation{}, err
	}
	for i := range codes {
		if _, err = tx.Exec(ctx, `INSERT INTO v3_mfa_recovery_codes
		 (instance_id,user_id,generation,salt,code_hash,created_at)
		 VALUES($1,$2,$3,$4,$5,$6)`, s.instanceID, a.UserID, f.generation,
			salts[i], hashes[i], now); err != nil {
			return Confirmation{}, err
		}
	}
	if err = s.audit(ctx, tx, a, sessionHash, "mfa.totp.enrollment_confirmed", "success", "totp"); err != nil {
		return Confirmation{}, err
	}
	if err = s.commitCurrent(ctx, tx, a, true, f.pendingExpiresAt, &step); err != nil {
		return Confirmation{}, err
	}
	return Confirmation{Enabled: true, RecoveryCodes: codes}, nil
}

// CancelEnrollment discards a pending seed only from its initiating session,
// tier and environment. The one-time seed can never be recovered afterward.
func (s *Service) CancelEnrollment(ctx context.Context, a Actor) error {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	now := s.clock()
	_, sessionHash, err := s.lockedActor(ctx, tx, a, now, true)
	if err != nil {
		return err
	}
	f, err := s.loadFactor(ctx, tx, a.UserID)
	if _, _, authErr := s.lockedActor(ctx, tx, a, s.clock(), true); authErr != nil {
		return authErr
	}
	now = s.clock()
	if errors.Is(err, pgx.ErrNoRows) || err == nil && f.state != "pending" {
		return ErrConflict
	}
	if err != nil {
		return err
	}
	if f.boundSession != sessionHash || f.boundTier != a.Tier ||
		f.boundEnvironment != a.EnvironmentID {
		return ErrConflict
	}
	if _, err = s.decryptFactor(f, a.UserID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM v3_mfa_totp WHERE instance_id=$1 AND user_id=$2`,
		s.instanceID, a.UserID); err != nil {
		return err
	}
	if err = s.audit(ctx, tx, a, sessionHash, "mfa.totp.enrollment_cancelled", "success", "totp"); err != nil {
		return err
	}
	return s.commitCurrent(ctx, tx, a, true, time.Time{}, nil)
}

func (s *Service) decryptFactor(f factor, userID string) ([]byte, error) {
	if f.state == "pending" {
		return s.open(f.keyID, f.nonce, f.ciphertext, "pending", s.instanceID, userID,
			f.generation, f.boundSession, f.boundTier, f.boundEnvironment)
	}
	if f.state == "active" {
		return s.open(f.keyID, f.nonce, f.ciphertext, "active", s.instanceID, userID, f.generation)
	}
	return nil, ErrUnavailable
}

func (s *Service) failedAttempt(ctx context.Context, tx pgx.Tx, a Actor, sessionHash string,
	f factor, now time.Time, method string) error {
	attempts := f.failed + 1
	var lockedUntil any
	if attempts >= maxFailures {
		attempts = 0
		lockedUntil = now.Add(lockoutDuration)
	}
	if _, err := tx.Exec(ctx, `UPDATE v3_mfa_totp SET failed_attempts=$3,locked_until=$4,
	 updated_at=$5 WHERE instance_id=$1 AND user_id=$2`, s.instanceID, a.UserID,
		attempts, lockedUntil, now); err != nil {
		return err
	}
	if err := s.audit(ctx, tx, a, sessionHash, "mfa.totp."+method+"_denied", "blocked", method); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	if lockedUntil != nil {
		return ErrRateLimited
	}
	return ErrInvalidCode
}
