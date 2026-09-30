package v3alertmanager

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

var (
	ErrSilenceCreateUnresolved  = errors.New("silence create outcome remains unknown")
	ErrSilenceCreateNeedsReview = errors.New("silence create requires manual review")
)

// reconcileCreate reads the complete, unfiltered upstream snapshot before it
// confirms an operation. It never retries the POST. A partial or invalid list
// is an error and cannot establish uniqueness.
func (l *DatabaseSilenceLedger) reconcileCreate(ctx context.Context, id string, client *Client) (SilenceCreateOperation, error) {
	if l == nil || l.DB == nil || client == nil {
		return SilenceCreateOperation{}, errors.New("silence create reconciliation unavailable")
	}
	initial, err := scanSilenceCreate(l.DB.QueryRow(ctx, `SELECT `+silenceCreateColumns+`
 FROM v3_silence_create_operations WHERE id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return SilenceCreateOperation{}, ErrSilenceCreateNotFound
	}
	if err != nil {
		return SilenceCreateOperation{}, err
	}
	if initial.State == SilenceCreateManualReview {
		return initial.SilenceCreateOperation, ErrSilenceCreateNeedsReview
	}
	if initial.State == SilenceCreateConfirmed {
		return initial.SilenceCreateOperation, nil
	}
	if initial.State != SilenceCreateDispatching && initial.State != SilenceCreateUnknown && initial.State != SilenceCreateAcknowledged {
		return initial.SilenceCreateOperation, ErrSilenceCreateBusy
	}
	silences, err := client.ListSilences(ctx, initial.EnvironmentID)
	if err != nil {
		// A failed enumeration cannot prove absence. Keep the intent terminal for
		// automatic dispatch while a later read or human review resolves it.
		if markErr := l.markCreateUnknown(ctx, id); markErr != nil {
			return SilenceCreateOperation{}, errors.Join(err, markErr)
		}
		return SilenceCreateOperation{}, fmt.Errorf("list Alertmanager silences: %w", err)
	}
	return l.reconcileCreateSnapshot(ctx, initial, silences)
}

// reconcileCreateSnapshot is kept private so product code cannot confirm one
// caller-supplied silence. The only production path supplies the full list
// returned by Client.ListSilences.
func (l *DatabaseSilenceLedger) reconcileCreateSnapshot(ctx context.Context, initial silenceCreateRow, silences []Silence) (SilenceCreateOperation, error) {
	tx, err := l.DB.Begin(ctx)
	if err != nil {
		return SilenceCreateOperation{}, err
	}
	defer tx.Rollback(ctx)
	v, err := scanSilenceCreate(tx.QueryRow(ctx, `SELECT `+silenceCreateColumns+`
 FROM v3_silence_create_operations WHERE id=$1 FOR UPDATE`, initial.ID))
	if errors.Is(err, pgx.ErrNoRows) {
		return SilenceCreateOperation{}, ErrSilenceCreateNotFound
	}
	if err != nil {
		return SilenceCreateOperation{}, err
	}
	if v.PayloadHash != initial.PayloadHash || v.EnvironmentID != initial.EnvironmentID {
		return SilenceCreateOperation{}, ErrSilenceCreateConflict
	}
	if v.State == SilenceCreateManualReview {
		return v.SilenceCreateOperation, ErrSilenceCreateNeedsReview
	}
	if v.State != SilenceCreateDispatching && v.State != SilenceCreateUnknown &&
		v.State != SilenceCreateAcknowledged && v.State != SilenceCreateConfirmed {
		return v.SilenceCreateOperation, ErrSilenceCreateBusy
	}
	observed, disposition := evaluateCreateSnapshot(v.SilenceCreateOperation, silences)
	switch disposition {
	case "missing":
		if v.State == SilenceCreateConfirmed {
			return v.SilenceCreateOperation, tx.Commit(ctx)
		}
		if v.State != SilenceCreateUnknown {
			if _, err = tx.Exec(ctx, `UPDATE v3_silence_create_operations
 SET state='unknown',updated_at=clock_timestamp() WHERE id=$1`, v.ID); err != nil {
				return SilenceCreateOperation{}, err
			}
			if err = auditSilenceCreate(ctx, tx, v.ActorID, v.ApplicationID,
				"v3.silence_create.unknown", v.ID, "unknown", v.EnvironmentID, SilenceCreateUnknown); err != nil {
				return SilenceCreateOperation{}, err
			}
		}
		v.State = SilenceCreateUnknown
		if err = tx.Commit(ctx); err != nil {
			return SilenceCreateOperation{}, err
		}
		return v.SilenceCreateOperation, ErrSilenceCreateUnresolved
	case "manual_review":
		if _, err = tx.Exec(ctx, `UPDATE v3_silence_create_operations
 SET state='manual_review',updated_at=clock_timestamp() WHERE id=$1`, v.ID); err != nil {
			return SilenceCreateOperation{}, err
		}
		if err = auditSilenceCreate(ctx, tx, v.ActorID, v.ApplicationID,
			"v3.silence_create.manual_review", v.ID, "unknown", v.EnvironmentID, SilenceCreateManualReview); err != nil {
			return SilenceCreateOperation{}, err
		}
		v.State = SilenceCreateManualReview
		if err = tx.Commit(ctx); err != nil {
			return SilenceCreateOperation{}, err
		}
		return v.SilenceCreateOperation, ErrSilenceCreateNeedsReview
	case "unique":
		if observed == nil {
			return SilenceCreateOperation{}, ErrSilenceCreateMismatch
		}
		if v.State == SilenceCreateConfirmed {
			return v.SilenceCreateOperation, tx.Commit(ctx)
		}
		if _, err = tx.Exec(ctx, `UPDATE v3_silence_create_operations
 SET state='confirmed',upstream_id=$2,updated_at=clock_timestamp() WHERE id=$1`, v.ID, observed.ID); err != nil {
			return SilenceCreateOperation{}, err
		}
		if err = auditSilenceCreate(ctx, tx, v.ActorID, v.ApplicationID,
			"v3.silence_create.confirmed", v.ID, "success", v.EnvironmentID, SilenceCreateConfirmed); err != nil {
			return SilenceCreateOperation{}, err
		}
		v.State, v.UpstreamID = SilenceCreateConfirmed, observed.ID
		if err = tx.Commit(ctx); err != nil {
			return SilenceCreateOperation{}, err
		}
		return v.SilenceCreateOperation, nil
	default:
		return SilenceCreateOperation{}, ErrSilenceCreateMismatch
	}
}
