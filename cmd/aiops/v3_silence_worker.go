package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync/atomic"
	"time"

	"github.com/destinywans/redstring/internal/v3alertmanager"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	v3SilenceWorkerInterval = 15 * time.Second
	v3SilenceWorkerBatch    = 8
	v3SilenceWorkerMaxAge   = 75 * time.Second
)

// v3SilenceWorker is the independent consumer of durable, two-person
// approvals. A successful Ready call means its loop is actually running and
// recently read the approval queue. It does not claim that Alertmanager is
// currently healthy or that any particular proposal can still execute.
type v3SilenceWorker struct {
	db       *pgxpool.Pool
	executor *v3alertmanager.SilenceExecutor
	running  atomic.Bool
	healthy  atomic.Bool
	beat     atomic.Int64

	// These cursors belong to Run. New approvals are drained before old
	// uncertain claims, so a slow upstream readback cannot consume the whole
	// approval window for unrelated proposals.
	newCursorTime       time.Time
	newCursorID         string
	reconcileCursorTime time.Time
	reconcileCursorID   string
}

func newV3SilenceWorker(db *pgxpool.Pool, executor *v3alertmanager.SilenceExecutor) *v3SilenceWorker {
	return &v3SilenceWorker{db: db, executor: executor}
}

func (w *v3SilenceWorker) Ready(ctx context.Context) error {
	if w == nil || w.db == nil || w.executor == nil || w.executor.Ledger == nil ||
		w.executor.Ledger.DB != w.db || !w.running.Load() {
		return v3alertmanager.ErrNotConfigured
	}
	if !w.healthy.Load() || time.Since(time.Unix(0, w.beat.Load())) > v3SilenceWorkerMaxAge {
		return errors.New("silence worker is not polling approvals")
	}
	probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := w.db.Ping(probeCtx); err != nil {
		return fmt.Errorf("silence worker database unavailable: %w", err)
	}
	return nil
}

// Run polls only persisted approved decisions. The executor owns the atomic
// one-shot claim before any Alertmanager write. A claimed or uncertain result
// is only reconciled by reading Alertmanager; this loop never retries a write.
func (w *v3SilenceWorker) Run(ctx context.Context) error {
	if w == nil || w.db == nil || w.executor == nil || w.executor.Ledger == nil ||
		w.executor.Ledger.DB != w.db {
		return v3alertmanager.ErrNotConfigured
	}
	if !w.running.CompareAndSwap(false, true) {
		return errors.New("silence worker is already running")
	}
	defer func() {
		w.healthy.Store(false)
		w.running.Store(false)
	}()

	ticker := time.NewTicker(v3SilenceWorkerInterval)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return nil
		}
		if err := w.poll(ctx); err != nil {
			w.healthy.Store(false)
			if ctx.Err() != nil {
				return nil
			}
			log.Printf("V3 silence worker approval poll unavailable: %v", err)
		} else {
			w.healthy.Store(true)
			w.touch()
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

type v3SilenceDecision struct {
	proposalID string
	kind       string
	claimState string
	approvedAt time.Time
}

func (w *v3SilenceWorker) poll(ctx context.Context) error {
	if err := w.processQueue(ctx, false, v3SilenceWorkerBatch); err != nil {
		return err
	}
	// At most one old uncertain write is read back each pass. This leaves
	// capacity for fresh approved decisions even during an AM read outage.
	return w.processQueue(ctx, true, 1)
}

func (w *v3SilenceWorker) processQueue(ctx context.Context, claimed bool, limit int) error {
	cursorTime, cursorID := w.newCursorTime, w.newCursorID
	if claimed {
		cursorTime, cursorID = w.reconcileCursorTime, w.reconcileCursorID
	}
	decisions, err := w.nextDecisions(ctx, claimed, limit, cursorTime, cursorID)
	if err != nil {
		return err
	}
	if len(decisions) == 0 && !cursorTime.IsZero() {
		// One bounded wrap query reaches decisions inserted behind the cursor.
		cursorTime, cursorID = time.Time{}, ""
		if claimed {
			w.reconcileCursorTime, w.reconcileCursorID = cursorTime, cursorID
		} else {
			w.newCursorTime, w.newCursorID = cursorTime, cursorID
		}
		decisions, err = w.nextDecisions(ctx, claimed, limit, cursorTime, cursorID)
		if err != nil {
			return err
		}
	}
	w.healthy.Store(true)
	w.touch()
	for _, decision := range decisions {
		if claimed {
			w.reconcileCursorTime, w.reconcileCursorID = decision.approvedAt, decision.proposalID
		} else {
			w.newCursorTime, w.newCursorID = decision.approvedAt, decision.proposalID
		}
		w.touch()
		itemCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
		err := w.handle(itemCtx, decision)
		cancel()
		w.touch()
		if err != nil && ctx.Err() == nil {
			// This records a failed attempt for operators. The next queue pass
			// may retry only an unclaimed decision; a claim is never resent.
			log.Printf("V3 silence worker proposal %s (%s) unresolved: %v",
				decision.proposalID, decision.kind, err)
		}
	}
	return nil
}

func (w *v3SilenceWorker) nextDecisions(ctx context.Context, claimed bool, limit int,
	cursorTime time.Time, cursorID string) ([]v3SilenceDecision, error) {
	queryCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	rows, err := w.db.Query(queryCtx, `SELECT d.proposal_id,d.action_kind,
 COALESCE(c.state,''),d.approved_at
 FROM v3_proposal_approval_decisions d
 JOIN v3_proposals p ON p.id=d.proposal_id
 LEFT JOIN v3_silence_execution_claims c ON c.proposal_id=d.proposal_id
 WHERE p.status='approved'
   AND d.action_kind IN ('alertmanager.silence.create','alertmanager.silence.cancel')
   AND (($4::boolean=false AND c.proposal_id IS NULL AND d.expires_at>clock_timestamp())
        OR ($4::boolean=true AND c.state IN ('dispatching','unknown')))
   AND (d.approved_at,d.proposal_id)>($1,$2)
 ORDER BY d.approved_at,d.proposal_id
 LIMIT $3`, cursorTime, cursorID, limit, claimed)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	decisions := make([]v3SilenceDecision, 0, limit)
	for rows.Next() {
		var item v3SilenceDecision
		if err := rows.Scan(&item.proposalID, &item.kind, &item.claimState, &item.approvedAt); err != nil {
			return nil, err
		}
		decisions = append(decisions, item)
	}
	return decisions, rows.Err()
}

func (w *v3SilenceWorker) handle(ctx context.Context, d v3SilenceDecision) error {
	switch d.kind {
	case v3alertmanager.ProposedSilenceCreateKind:
		if d.claimState != "" {
			_, err := w.executor.ReconcileCreate(ctx, d.proposalID)
			return err
		}
		_, err := w.executor.ExecuteApprovedCreate(ctx, d.proposalID)
		if errors.Is(err, v3alertmanager.ErrSilenceExecutionClaimed) {
			_, err = w.executor.ReconcileCreate(ctx, d.proposalID)
		}
		return err
	case v3alertmanager.ProposedSilenceCancelKind:
		if d.claimState != "" {
			_, err := w.executor.ReconcileCancel(ctx, d.proposalID)
			return err
		}
		_, err := w.executor.ExecuteApprovedCancel(ctx, d.proposalID)
		if errors.Is(err, v3alertmanager.ErrSilenceExecutionClaimed) {
			_, err = w.executor.ReconcileCancel(ctx, d.proposalID)
		}
		return err
	default:
		return errors.New("unsupported silence approval kind")
	}
}

func (w *v3SilenceWorker) touch() {
	w.beat.Store(time.Now().UnixNano())
}
