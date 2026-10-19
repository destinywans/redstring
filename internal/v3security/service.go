package v3security

import (
	"context"
	"errors"
	"time"

	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/rbac"
	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var errOperationPending = errors.New("security operation pending reconciliation")
var errSecurityChanged = errors.New("security revision changed")

type Service struct {
	Store      *store.Store
	InstanceID string
	Authority  RuntimeController
}

func New(st *store.Store, instanceID string, authority RuntimeController) *Service {
	return &Service{Store: st, InstanceID: instanceID, Authority: authority}
}

func (s *Service) runtimeScope(environmentID string) (RuntimeScope, error) {
	if s == nil || s.Store == nil || s.InstanceID == "" || environmentID == "" || environmentID == "all" {
		return RuntimeScope{}, ErrAuthorityUnavailable
	}
	return RuntimeScope{InstanceID: s.InstanceID, EnvironmentID: environmentID}, nil
}

// authorize takes account-writer advisory locks before the user/session rows
// and rechecks the V3 matrix on this transaction connection. The route gate is
// only a first filter; a scoped workspace grant never authorizes this policy.
func (s *Service) authorize(ctx context.Context, tx pgx.Tx, actor core.User, token, environmentID string) error {
	for _, key := range []int64{752018992, 752018995, 752018998} {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared($1)`, key); err != nil {
			return err
		}
	}
	if err := store.LockActiveSession(ctx, tx, token, actor.ID); err != nil {
		return err
	}
	var eligible bool
	if err := tx.QueryRow(ctx, `SELECT source<>'settings_demo' AND v3_identity_kind='human' FROM users WHERE id=$1`, actor.ID).Scan(&eligible); err != nil {
		return err
	}
	if !eligible {
		return store.ErrForbidden
	}
	if err := (&rbac.V3Policy{Store: s.Store}).AuthorizeWithTx(ctx, tx, actor,
		rbac.V3Decision{Module: "workspace", Action: "manage", EnvironmentID: environmentID}); err != nil {
		return err
	}
	var globalAdministrator bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM v3_effective_role_grants g
 JOIN rbac_platform_administrators a ON a.user_id=g.user_id
 WHERE g.user_id=$1 AND g.v3_identity_kind='human'
 AND g.module='workspace' AND g.action='manage'
 AND g.scope_kind='global' AND g.scope_id='*')`, actor.ID).Scan(&globalAdministrator); err != nil {
		return err
	}
	if !globalAdministrator {
		return store.ErrForbidden
	}
	return nil
}

func unresolvedOperation(ctx context.Context, tx pgx.Tx, scope RuntimeScope) (bool, error) {
	var unresolved bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM v3_security_policy_operations
 WHERE instance_id=$1 AND environment_id=$2 AND status IN ('pending','unknown'))`,
		scope.InstanceID, scope.EnvironmentID).Scan(&unresolved)
	return unresolved, err
}

func lockScope(ctx context.Context, tx pgx.Tx, scope RuntimeScope, exclusive bool) error {
	query := `SELECT pg_advisory_xact_lock_shared($1)`
	if exclusive {
		query = `SELECT pg_advisory_xact_lock($1)`
	}
	_, err := tx.Exec(ctx, query, scopeLockKey(scope))
	return err
}

func (s *Service) snapshot(ctx context.Context, scope RuntimeScope) (RuntimeSnapshot, error) {
	if s.Authority == nil {
		return RuntimeSnapshot{}, ErrAuthorityUnavailable
	}
	snapshot, err := s.Authority.Snapshot(ctx, scope)
	if err != nil || !validSnapshot(snapshot, scope, time.Now()) {
		return RuntimeSnapshot{}, ErrAuthorityUnavailable
	}
	return snapshot, nil
}

// read returns the authority snapshot. Without a configured authority it
// returns the "not_configured" reason instead, after the same authorization,
// pending-operation and session checks; no field is then known.
func (s *Service) read(ctx context.Context, actor core.User, token, environmentID string) (RuntimeSnapshot, string, error) {
	snapshot, err := s.readSnapshot(ctx, actor, token, environmentID)
	if errors.Is(err, errAuthorityNotConfigured) {
		return RuntimeSnapshot{}, "not_configured", nil
	}
	return snapshot, "", err
}

var errAuthorityNotConfigured = errors.New("security runtime authority is not configured")

func (s *Service) readSnapshot(ctx context.Context, actor core.User, token, environmentID string) (RuntimeSnapshot, error) {
	scope, err := s.runtimeScope(environmentID)
	if err != nil {
		return RuntimeSnapshot{}, err
	}
	tx, err := s.Store.DB.Begin(ctx)
	if err != nil {
		return RuntimeSnapshot{}, err
	}
	defer tx.Rollback(ctx)
	if err := s.authorize(ctx, tx, actor, token, environmentID); err != nil {
		return RuntimeSnapshot{}, err
	}
	if err := lockScope(ctx, tx, scope, false); err != nil {
		return RuntimeSnapshot{}, err
	}
	unresolved, err := unresolvedOperation(ctx, tx, scope)
	if err != nil {
		return RuntimeSnapshot{}, err
	}
	if unresolved {
		return RuntimeSnapshot{}, errOperationPending
	}
	// The authority call is bounded. Holding the shared auth locks prevents a
	// simultaneous revocation from committing before this response is ready.
	var snapshot RuntimeSnapshot
	unconfigured := s.Authority == nil
	if !unconfigured {
		if snapshot, err = s.snapshot(ctx, scope); err != nil {
			return RuntimeSnapshot{}, err
		}
	}
	if err := store.LockActiveSession(ctx, tx, token, actor.ID); err != nil {
		return RuntimeSnapshot{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return RuntimeSnapshot{}, err
	}
	if unconfigured {
		return RuntimeSnapshot{}, errAuthorityNotConfigured
	}
	return snapshot, nil
}

func (s *Service) prepare(ctx context.Context, actor core.User, token string, scope RuntimeScope, expected int64, desired SecurityView) (string, SecurityControls, error) {
	tx, err := s.Store.DB.Begin(ctx)
	if err != nil {
		return "", SecurityControls{}, err
	}
	defer tx.Rollback(ctx)
	if err := s.authorize(ctx, tx, actor, token, scope.EnvironmentID); err != nil {
		return "", SecurityControls{}, err
	}
	if err := lockScope(ctx, tx, scope, true); err != nil {
		return "", SecurityControls{}, err
	}
	unresolved, err := unresolvedOperation(ctx, tx, scope)
	if err != nil {
		return "", SecurityControls{}, err
	}
	if unresolved {
		return "", SecurityControls{}, errOperationPending
	}
	current, err := s.snapshot(ctx, scope)
	if err != nil {
		return "", SecurityControls{}, err
	}
	if current.Revision != expected {
		return "", SecurityControls{}, errSecurityChanged
	}
	controls, err := validateEditableView(current.View, desired)
	if err != nil {
		return "", SecurityControls{}, err
	}
	if sameControls(controls, controlsFromView(current.View)) {
		return "", SecurityControls{}, errInvalidView
	}
	if err := store.LockActiveSession(ctx, tx, token, actor.ID); err != nil {
		return "", SecurityControls{}, err
	}
	operationID := store.ID()
	_, err = tx.Exec(ctx, `INSERT INTO v3_security_policy_operations
 (id,instance_id,environment_id,actor_id,expected_revision,requested_controls,status)
 VALUES($1,$2,$3,$4,$5,$6,'pending')`, operationID, scope.InstanceID,
		scope.EnvironmentID, actor.ID, expected, marshal(controls))
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return "", SecurityControls{}, errOperationPending
		}
		return "", SecurityControls{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO management_audit(actor_id,action,target,outcome,changes)
 VALUES($1,'v3.security.change_requested',$2,'pending',$3)`, actor.ID,
		scope.InstanceID+"/"+scope.EnvironmentID,
		marshal(map[string]any{"operation_id": operationID, "expected_revision": expected, "controls": controls}))
	if err != nil {
		return "", SecurityControls{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", SecurityControls{}, err
	}
	return operationID, controls, nil
}

func finishFailure(ctx context.Context, tx pgx.Tx, actorID, operationID string, scope RuntimeScope, status, code string) error {
	if status != "failed" && status != "unknown" {
		return ErrAuthorityUnavailable
	}
	command, err := tx.Exec(ctx, `UPDATE v3_security_policy_operations SET status=$2,error_code=$3,updated_at=clock_timestamp()
 WHERE id=$1 AND status='pending'`, operationID, status, code)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return errOperationPending
	}
	_, err = tx.Exec(ctx, `INSERT INTO management_audit(actor_id,action,target,outcome,changes)
 VALUES($1,'v3.security.change_finished',$2,$3,$4)`, actorID,
		scope.InstanceID+"/"+scope.EnvironmentID, status,
		marshal(map[string]any{"operation_id": operationID, "error_code": code}))
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) applyPrepared(ctx context.Context, actor core.User, token string, scope RuntimeScope, expected int64, operationID string, desired SecurityControls) (RuntimeSnapshot, error) {
	tx, err := s.Store.DB.Begin(ctx)
	if err != nil {
		return RuntimeSnapshot{}, err
	}
	defer tx.Rollback(ctx)
	// Recheck after the first transaction. No runtime action can occur after a
	// revocation that committed between preparation and this final transaction.
	if err := s.authorize(ctx, tx, actor, token, scope.EnvironmentID); err != nil {
		if finishFailure(ctx, tx, actor.ID, operationID, scope, "failed", "authorization_revoked") != nil {
			return RuntimeSnapshot{}, errOperationPending
		}
		return RuntimeSnapshot{}, err
	}
	var currentStatus string
	if err := tx.QueryRow(ctx, `SELECT status FROM v3_security_policy_operations WHERE id=$1
 AND instance_id=$2 AND environment_id=$3 AND actor_id=$4 AND expected_revision=$5 FOR UPDATE`,
		operationID, scope.InstanceID, scope.EnvironmentID, actor.ID, expected).Scan(&currentStatus); err != nil {
		return RuntimeSnapshot{}, err
	}
	if currentStatus != "pending" {
		return RuntimeSnapshot{}, errOperationPending
	}
	current, err := s.snapshot(ctx, scope)
	if err != nil {
		if finishFailure(ctx, tx, actor.ID, operationID, scope, "failed", "preapply_snapshot_unavailable") != nil {
			return RuntimeSnapshot{}, errOperationPending
		}
		return RuntimeSnapshot{}, ErrAuthorityUnavailable
	}
	if current.Revision != expected {
		if finishFailure(ctx, tx, actor.ID, operationID, scope, "failed", "revision_changed") != nil {
			return RuntimeSnapshot{}, errOperationPending
		}
		return RuntimeSnapshot{}, errSecurityChanged
	}
	if err := store.LockActiveSession(ctx, tx, token, actor.ID); err != nil {
		if finishFailure(ctx, tx, actor.ID, operationID, scope, "failed", "session_expired_before_apply") != nil {
			return RuntimeSnapshot{}, errOperationPending
		}
		return RuntimeSnapshot{}, err
	}
	if s.Authority == nil {
		return RuntimeSnapshot{}, ErrAuthorityUnavailable
	}
	request := RuntimeApply{InstanceID: scope.InstanceID,
		EnvironmentID: scope.EnvironmentID, ExpectedRevision: expected,
		OperationID: operationID, Controls: desired}
	receipt, err := s.Authority.Apply(ctx, request)
	if err != nil {
		status, code := "unknown", "apply_outcome_unknown"
		if errors.Is(err, ErrRuntimeConflict) {
			status, code = "failed", "revision_changed"
		} else if errors.Is(err, ErrRuntimeRejected) {
			status, code = "failed", "control_rejected"
		}
		if finishFailure(ctx, tx, actor.ID, operationID, scope, status, code) != nil {
			return RuntimeSnapshot{}, errOperationPending
		}
		if status == "failed" {
			if code == "control_rejected" {
				return RuntimeSnapshot{}, ErrRuntimeRejected
			}
			return RuntimeSnapshot{}, errSecurityChanged
		}
		return RuntimeSnapshot{}, ErrAuthorityUnavailable
	}
	if receipt.InstanceID != request.InstanceID || receipt.EnvironmentID != request.EnvironmentID ||
		receipt.ExpectedRevision != request.ExpectedRevision || receipt.OperationID != request.OperationID ||
		receipt.ControlsSHA256 != controlsDigest(request.Controls) ||
		!receipt.Durable || !receipt.Enforced || receipt.Revision <= expected {
		if finishFailure(ctx, tx, actor.ID, operationID, scope, "unknown", "apply_unverified") != nil {
			return RuntimeSnapshot{}, errOperationPending
		}
		return RuntimeSnapshot{}, ErrAuthorityUnavailable
	}
	verified, err := s.snapshot(ctx, scope)
	if err != nil || verified.Revision != receipt.Revision ||
		!sameControls(controlsFromView(verified.View), desired) {
		if finishFailure(ctx, tx, actor.ID, operationID, scope, "unknown", "readback_unverified") != nil {
			return RuntimeSnapshot{}, errOperationPending
		}
		return RuntimeSnapshot{}, ErrAuthorityUnavailable
	}
	if err := store.LockActiveSession(ctx, tx, token, actor.ID); err != nil {
		if finishFailure(ctx, tx, actor.ID, operationID, scope, "unknown", "session_expired_after_apply") != nil {
			return RuntimeSnapshot{}, errOperationPending
		}
		return RuntimeSnapshot{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO v3_security_policy_applied
 (instance_id,environment_id,runtime_revision,controls,operation_id,updated_at)
 VALUES($1,$2,$3,$4,$5,clock_timestamp())
 ON CONFLICT(instance_id,environment_id) DO UPDATE SET
 runtime_revision=EXCLUDED.runtime_revision,controls=EXCLUDED.controls,
 operation_id=EXCLUDED.operation_id,updated_at=clock_timestamp()`,
		scope.InstanceID, scope.EnvironmentID, verified.Revision, marshal(desired), operationID)
	if err != nil {
		return RuntimeSnapshot{}, err
	}
	command, err := tx.Exec(ctx, `UPDATE v3_security_policy_operations
 SET status='applied',applied_revision=$2,updated_at=clock_timestamp()
 WHERE id=$1 AND status='pending'`, operationID, verified.Revision)
	if err != nil || command.RowsAffected() != 1 {
		return RuntimeSnapshot{}, errOperationPending
	}
	_, err = tx.Exec(ctx, `INSERT INTO management_audit(actor_id,action,target,outcome,changes)
 VALUES($1,'v3.security.change_finished',$2,'success',$3)`, actor.ID,
		scope.InstanceID+"/"+scope.EnvironmentID,
		marshal(map[string]any{"operation_id": operationID, "revision_before": expected,
			"revision_after": verified.Revision, "controls": desired}))
	if err != nil {
		return RuntimeSnapshot{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		// The committed pending row blocks further writes and GET until an
		// operator reconciles the authority and operation audit.
		return RuntimeSnapshot{}, errOperationPending
	}
	return verified, nil
}

func (s *Service) write(ctx context.Context, actor core.User, token, environmentID string, expected int64, desired SecurityView) (RuntimeSnapshot, error) {
	scope, err := s.runtimeScope(environmentID)
	if err != nil {
		return RuntimeSnapshot{}, err
	}
	if expected < 1 || validView(desired) != nil {
		return RuntimeSnapshot{}, errInvalidView
	}
	operationID, controls, err := s.prepare(ctx, actor, token, scope, expected, desired)
	if err != nil {
		return RuntimeSnapshot{}, err
	}
	return s.applyPrepared(ctx, actor, token, scope, expected, operationID, controls)
}
