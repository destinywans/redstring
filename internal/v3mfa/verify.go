package v3mfa

import (
	"context"
	"crypto/subtle"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

type checkedCode struct {
	method       string
	step         int64
	recoveryHash []byte
}

func (s *Service) checkCode(ctx context.Context, tx pgx.Tx, f factor, userID, code string,
	now time.Time) (checkedCode, error) {
	seed, err := s.decryptFactor(f, userID)
	if err != nil {
		return checkedCode{}, err
	}
	if step, ok := matchTOTP(seed, code, now, f.lastStep); ok {
		return checkedCode{method: "totp", step: step}, nil
	}
	canonical, ok := canonicalRecovery(code)
	if !ok {
		return checkedCode{}, ErrInvalidCode
	}
	rows, err := tx.Query(ctx, `SELECT salt,code_hash FROM v3_mfa_recovery_codes
	 WHERE instance_id=$1 AND user_id=$2 AND generation=$3 AND consumed_at IS NULL
	 FOR UPDATE`, s.instanceID, userID, f.generation)
	if err != nil {
		return checkedCode{}, err
	}
	var matchingHash []byte
	for rows.Next() {
		var salt, hash []byte
		if err = rows.Scan(&salt, &hash); err != nil {
			rows.Close()
			return checkedCode{}, err
		}
		if subtle.ConstantTimeCompare(recoveryHash(salt, canonical), hash) == 1 {
			matchingHash = append([]byte(nil), hash...)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return checkedCode{}, err
	}
	if matchingHash == nil {
		return checkedCode{}, ErrInvalidCode
	}
	return checkedCode{method: "recovery", recoveryHash: matchingHash}, nil
}

func (s *Service) consumeCode(ctx context.Context, tx pgx.Tx, a Actor, f factor,
	checked checkedCode, now time.Time) error {
	if checked.method == "totp" {
		_, err := tx.Exec(ctx, `UPDATE v3_mfa_totp SET last_accepted_step=$3,
		 failed_attempts=0,locked_until=NULL,updated_at=$4
		 WHERE instance_id=$1 AND user_id=$2`, s.instanceID, a.UserID, checked.step, now)
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE v3_mfa_recovery_codes SET consumed_at=$5
	 WHERE instance_id=$1 AND user_id=$2 AND generation=$3 AND code_hash=$4
	 AND consumed_at IS NULL`, s.instanceID, a.UserID, f.generation, checked.recoveryHash, now)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrInvalidCode
	}
	_, err = tx.Exec(ctx, `UPDATE v3_mfa_totp SET failed_attempts=0,
	 locked_until=NULL,updated_at=$3 WHERE instance_id=$1 AND user_id=$2`,
		s.instanceID, a.UserID, now)
	return err
}

func (s *Service) lockedDenial(ctx context.Context, tx pgx.Tx, a Actor, sessionHash, action string) error {
	if err := s.audit(ctx, tx, a, sessionHash, "mfa.totp."+action+"_denied", "blocked", "locked"); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	return ErrRateLimited
}

// Verify consumes one TOTP time step or one recovery code. The returned result
// is scoped to this call only; no passkey approval or login session is issued.
func (s *Service) Verify(ctx context.Context, a Actor, code string) (Verification, error) {
	if len(code) > 64 {
		return Verification{}, ErrInvalidInput
	}
	tx, err := s.beginTx(ctx)
	if err != nil {
		return Verification{}, err
	}
	defer tx.Rollback(ctx)
	now := s.clock()
	_, sessionHash, err := s.lockedActor(ctx, tx, a, now, false)
	if err != nil {
		return Verification{}, err
	}
	f, err := s.loadFactor(ctx, tx, a.UserID)
	if _, _, authErr := s.lockedActor(ctx, tx, a, s.clock(), false); authErr != nil {
		return Verification{}, authErr
	}
	now = s.clock()
	if errors.Is(err, pgx.ErrNoRows) || err == nil && f.state != "active" {
		return Verification{}, ErrNoFactor
	}
	if err != nil {
		return Verification{}, err
	}
	if _, err = s.decryptFactor(f, a.UserID); err != nil {
		return Verification{}, err
	}
	if now.Before(f.lockedUntil) {
		return Verification{}, s.lockedDenial(ctx, tx, a, sessionHash, "verify")
	}
	checked, err := s.checkCode(ctx, tx, f, a.UserID, code, now)
	if errors.Is(err, ErrInvalidCode) {
		return Verification{}, s.failedAttempt(ctx, tx, a, sessionHash, f, now, "verify")
	}
	if err != nil {
		return Verification{}, err
	}
	if err = s.consumeCode(ctx, tx, a, f, checked, now); err != nil {
		return Verification{}, err
	}
	var acceptedStep *int64
	if checked.method == "totp" {
		acceptedStep = &checked.step
	}
	if err = s.audit(ctx, tx, a, sessionHash, "mfa.totp.verified", "success", checked.method); err != nil {
		return Verification{}, err
	}
	if err = s.commitCurrent(ctx, tx, a, false, time.Time{}, acceptedStep); err != nil {
		return Verification{}, err
	}
	return Verification{Verified: true, Method: checked.method}, nil
}

// Cancel requires both a fresh login and possession of the active factor or
// one recovery code. Deletion cascades to all remaining recovery codes.
func (s *Service) Cancel(ctx context.Context, a Actor, code string) error {
	if len(code) > 64 {
		return ErrInvalidInput
	}
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
	if errors.Is(err, pgx.ErrNoRows) || err == nil && f.state != "active" {
		return ErrNoFactor
	}
	if err != nil {
		return err
	}
	if _, err = s.decryptFactor(f, a.UserID); err != nil {
		return err
	}
	if now.Before(f.lockedUntil) {
		return s.lockedDenial(ctx, tx, a, sessionHash, "cancel")
	}
	checked, err := s.checkCode(ctx, tx, f, a.UserID, code, now)
	if errors.Is(err, ErrInvalidCode) {
		return s.failedAttempt(ctx, tx, a, sessionHash, f, now, "cancel")
	}
	if err != nil {
		return err
	}
	if err = s.consumeCode(ctx, tx, a, f, checked, now); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM v3_mfa_totp WHERE instance_id=$1 AND user_id=$2`,
		s.instanceID, a.UserID); err != nil {
		return err
	}
	var acceptedStep *int64
	if checked.method == "totp" {
		acceptedStep = &checked.step
	}
	if err = s.audit(ctx, tx, a, sessionHash, "mfa.totp.cancelled", "success", checked.method); err != nil {
		return err
	}
	return s.commitCurrent(ctx, tx, a, true, time.Time{}, acceptedStep)
}
