package v3rollouts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/store"
	"github.com/wans/aiops-canvas/internal/v3alertmanager"
	"github.com/wans/aiops-canvas/internal/v3kube"
	"github.com/wans/aiops-canvas/internal/v3proposals"
	"github.com/wans/aiops-canvas/internal/v3telemetry"
)

// Ledger states. Terminal states never change again.
const (
	stateReserved       = "reserved"
	stateDispatching    = "dispatching"
	stateUnknown        = "unknown"
	stateApplied        = "applied"
	stateRollingOut     = "rolling_out"
	stateVerifying      = "verifying"
	stateSucceeded      = "succeeded"
	stateFailed         = "failed"
	stateNeedsAttention = "needs_attention"
)

func terminal(status string) bool {
	return status == stateSucceeded || status == stateFailed || status == stateNeedsAttention
}

type timelineEntry struct {
	State string    `json:"state"`
	At    time.Time `json:"at"`
}

type rolloutSnapshot struct {
	Desired         int32     `json:"desired"`
	Updated         int32     `json:"updated"`
	Available       int32     `json:"available"`
	CurrentRevision *string   `json:"current_revision"`
	ObservedAt      time.Time `json:"observed_at"`
}

type verificationSample struct {
	At               time.Time `json:"at"`
	RequestRate      *float64  `json:"request_rate"`
	ErrorRatio       *float64  `json:"error_ratio"`
	SampleAgeSeconds *float64  `json:"sample_age_seconds"`
	OK               bool      `json:"ok"`
}

type verificationState struct {
	Plan       v3proposals.RollbackVerificationPlan `json:"plan"`
	StartedAt  *time.Time                           `json:"started_at"`
	FinishedAt *time.Time                           `json:"finished_at"`
	Result     string                               `json:"result"`
	Samples    []verificationSample                 `json:"samples"`
}

const maxSamples = 40

// confirmGrace is how long verification keeps sampling past the window when
// the Deployment cannot be read at its end.
const confirmGrace = 2 * time.Minute

// ledgerRow is one execution as the executor holds it.
type ledgerRow struct {
	ProposalID   string
	ExecutionID  string
	Env, App     string
	Status       string
	Reason       *string
	Timeline     []timelineEntry
	Rollout      *rolloutSnapshot
	Verification *verificationState
}

func (r *ledgerRow) enteredAt(state string) time.Time {
	for i := len(r.Timeline) - 1; i >= 0; i-- {
		if r.Timeline[i].State == state {
			return r.Timeline[i].At
		}
	}
	return time.Time{}
}

// Run is the independent executor loop. It claims approved rollback
// proposals, resumes executions whose lease expired, and keeps a heartbeat
// that Ready reports to the approval flow.
func (s *Service) Run(ctx context.Context) {
	s.workerID = store.ID()
	s.running.Store(true)
	defer s.running.Store(false)
	var mu sync.Mutex
	var wg sync.WaitGroup
	active := map[string]bool{}
	start := func(id string) {
		mu.Lock()
		if active[id] {
			mu.Unlock()
			return
		}
		active[id] = true
		mu.Unlock()
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { mu.Lock(); delete(active, id); mu.Unlock() }()
			s.run(ctx, id)
		}()
	}
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		s.beat.Store(time.Now().UnixNano())
		if ids, err := s.pending(ctx); err == nil {
			for _, id := range ids {
				claimed, err := s.claim(ctx, id)
				if err != nil && ctx.Err() == nil {
					log.Printf("v3 rollout claim failed (proposal=%s): %v", id, err)
				}
				if claimed {
					start(id)
				}
			}
		}
		if ids, err := s.stale(ctx); err == nil {
			for _, id := range ids {
				start(id)
			}
		}
		select {
		case <-ctx.Done():
			wg.Wait()
			return
		case <-ticker.C:
		}
	}
}

// environments are the environments this instance may write: those with a
// write binding of its tier. An instance sharing the database with another
// tier never claims (or marks) the other tier's proposals.
func (s *Service) environments() []string {
	seen := map[string]bool{}
	out := []string{}
	for _, b := range s.WriteBindings {
		if b.Tier == s.Tier && !seen[b.Environment] {
			seen[b.Environment] = true
			out = append(out, b.Environment)
		}
	}
	return out
}

// pending lists approved rollback proposals without an execution.
func (s *Service) pending(ctx context.Context) ([]string, error) {
	return s.ids(ctx, `SELECT d.proposal_id FROM v3_proposal_approval_decisions d
 JOIN v3_proposals p ON p.id=d.proposal_id AND p.status='approved'
 WHERE d.action_kind=$1 AND d.environment_id=ANY($2::text[])
   AND NOT EXISTS (SELECT 1 FROM v3_rollout_executions e WHERE e.proposal_id=d.proposal_id)
 ORDER BY d.approved_at LIMIT 8`, v3proposals.RollbackActionKind, s.environments())
}

// stale lists unfinished executions whose lease expired (a restart).
func (s *Service) stale(ctx context.Context) ([]string, error) {
	return s.ids(ctx, `SELECT proposal_id FROM v3_rollout_executions
 WHERE status NOT IN ('succeeded','failed','needs_attention') AND environment_id=ANY($1::text[])
   AND (lease_expires_at IS NULL OR lease_expires_at<clock_timestamp())
 ORDER BY updated_at LIMIT 8`, s.environments())
}

func (s *Service) ids(ctx context.Context, query string, args ...any) ([]string, error) {
	rows, err := s.DB.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// claim verifies the approval proof, the requester's grants and the binding
// in one transaction and records the execution. A failed check is recorded as
// needs_attention so the outcome is visible; nothing is written to the
// cluster before the claim commits.
func (s *Service) claim(ctx context.Context, proposalID string) (bool, error) {
	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var status string
	if err = tx.QueryRow(ctx, `SELECT status FROM v3_proposals WHERE id=$1 FOR UPDATE`, proposalID).Scan(&status); err != nil {
		return false, err
	}
	var exists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM v3_rollout_executions WHERE proposal_id=$1)`, proposalID).Scan(&exists); err != nil || exists || status != "approved" {
		return false, err
	}
	action, actionDigest, reviewDigest, err := v3proposals.LoadRollbackActionTx(ctx, tx, proposalID)
	if err != nil {
		return false, err
	}
	var executionID, approvalDigest string
	if err = tx.QueryRow(ctx, `SELECT execution_id,approval_digest FROM v3_proposal_approval_decisions WHERE proposal_id=$1 FOR SHARE`,
		proposalID).Scan(&executionID, &approvalDigest); err != nil {
		return false, err
	}
	reason := ""
	authority := &v3proposals.SilenceApprovalAuthority{Proposals: s.Proposals}
	proof, err := authority.VerifyApprovedWithTx(ctx, tx, v3alertmanager.SilenceReviewedAction{ProposalID: proposalID,
		Kind: v3proposals.RollbackActionKind, Status: "approved", ActionDigest: actionDigest, ReviewContentDigest: reviewDigest,
		CreatorID: action.ActorID, EnvironmentID: action.EnvironmentID, ApplicationID: action.ApplicationID,
		InvestigationID: action.InvestigationID, InvestigationUpdatedAt: action.InvestigationUpdatedAt})
	switch {
	case errors.Is(err, v3alertmanager.ErrSilenceApprovalUnavailable):
		reason = "approval_invalid"
	case err != nil:
		return false, err
	case proof.ApprovalDigest != approvalDigest:
		reason = "approval_invalid"
	}
	if reason == "" {
		reason, err = s.checkRequesterAndBindingTx(ctx, tx, action)
		if err != nil {
			return false, err
		}
	}
	now := s.now()
	row := ledgerRow{ProposalID: proposalID, ExecutionID: executionID, Env: action.EnvironmentID, App: action.ApplicationID,
		Status: stateReserved, Timeline: []timelineEntry{{stateReserved, now}}}
	if reason != "" {
		row.Status, row.Reason = stateNeedsAttention, &reason
		row.Timeline = append(row.Timeline, timelineEntry{stateNeedsAttention, now})
	}
	timeline, _ := json.Marshal(row.Timeline)
	if _, err = tx.Exec(ctx, `INSERT INTO v3_rollout_executions
 (proposal_id,execution_id,environment_id,application_id,action_digest,approval_digest,status,status_reason,timeline,lease_owner,lease_expires_at)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9::jsonb,$10,clock_timestamp()+interval '2 minutes')`,
		proposalID, executionID, row.Env, row.App, actionDigest, approvalDigest, row.Status, row.Reason, timeline, s.workerID); err != nil {
		return false, err
	}
	if err = auditTx(ctx, tx, action, proposalID, "v3.rollout.claimed", row.Status, row.Reason); err != nil {
		return false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return false, err
	}
	return row.Status == stateReserved, nil
}

// checkRequesterAndBindingTx rechecks that the requester may still request
// this rollback and that the workload, its scope and the write binding are
// the ones the proposal was built from.
func (s *Service) checkRequesterAndBindingTx(ctx context.Context, tx pgx.Tx, a v3proposals.RollbackAction) (string, error) {
	var active bool
	if err := tx.QueryRow(ctx, `SELECT NOT disabled AND deleted_at IS NULL AND v3_identity_kind='human' FROM users WHERE id=$1 FOR SHARE`,
		a.ActorID).Scan(&active); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	if !active {
		return "authorization_changed", nil
	}
	team, err := ownerTeamTx(ctx, tx, a.ApplicationID, a.EnvironmentID)
	if errors.Is(err, errNotFound) {
		return "binding_changed", nil
	}
	if err != nil {
		return "", err
	}
	err = s.authorizeTx(ctx, tx, core.User{ID: a.ActorID}, a.ApplicationID, a.EnvironmentID, team,
		[2]string{"resources", "change"}, [2]string{"proposals", "change"})
	if errors.Is(err, store.ErrForbidden) {
		return "authorization_changed", nil
	}
	if err != nil {
		return "", err
	}
	if _, err := s.boundWorkload(ctx, tx, a); err != nil {
		if errors.Is(err, errBindingChanged) {
			return "binding_changed", nil
		}
		return "", err
	}
	return "", nil
}

// boundWorkload re-resolves the workload and write binding of an action and
// requires the proposal's fingerprint.
func (s *Service) boundWorkload(ctx context.Context, q v3telemetry.Querier, a v3proposals.RollbackAction) (boundTarget, error) {
	w, err := s.Registry.ResolveWorkload(ctx, q, a.ApplicationID, a.EnvironmentID)
	if errors.Is(err, v3telemetry.ErrNotConfigured) || errors.Is(err, v3telemetry.ErrAmbiguous) {
		return boundTarget{}, errBindingChanged
	}
	if err != nil {
		return boundTarget{}, err
	}
	b, ok := findWriteBinding(s.WriteBindings, s.Tier, a.EnvironmentID, w.ConnectorID, w.Scope.Namespace, w.Scope.Deployment)
	if len(w.NotReady) > 0 || !ok || w.TargetID != a.TargetID || w.Scope.Namespace != a.Namespace ||
		w.Scope.Deployment != a.Deployment || bindingFingerprint(w, b) != a.BindingFingerprint {
		return boundTarget{}, errBindingChanged
	}
	return boundTarget{Workload: w, Binding: b}, nil
}

type boundTarget struct {
	Workload v3telemetry.Workload
	Binding  WriteBinding
}

func auditTx(ctx context.Context, tx pgx.Tx, a v3proposals.RollbackAction, proposalID, action, status string, reason *string) error {
	_, err := tx.Exec(ctx, `INSERT INTO management_audit(actor_id,actor_kind,application_id,action,target,outcome,changes)
 VALUES('system','system',$1,$2,$3,$4,jsonb_build_object('environment_id',$5::text,'namespace',$6::text,
 'deployment',$7::text,'target_revision',$8::text,'status',$9::text,'reason',$10::text))`,
		a.ApplicationID, action, proposalID, outcomeOf(status), a.EnvironmentID, a.Namespace, a.Deployment,
		a.TargetRevision, status, reason)
	return err
}

func outcomeOf(status string) string {
	switch status {
	case stateSucceeded, stateReserved:
		return "success"
	case stateFailed:
		return "failed"
	default:
		return "needs_attention"
	}
}

// run drives one claimed execution to a terminal state, holding its lease.
func (s *Service) run(parent context.Context, proposalID string) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	row, err := s.lease(ctx, proposalID)
	if err != nil {
		return
	}
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				tag, err := s.DB.Exec(ctx, `UPDATE v3_rollout_executions SET lease_expires_at=clock_timestamp()+interval '2 minutes'
 WHERE proposal_id=$1 AND lease_owner=$2 AND status NOT IN ('succeeded','failed','needs_attention')`, proposalID, s.workerID)
				if err != nil || tag.RowsAffected() != 1 {
					cancel()
					return
				}
			}
		}
	}()
	action, err := s.loadAction(ctx, proposalID)
	if err != nil {
		s.finish(ctx, &row, action, stateNeedsAttention, "binding_changed")
		return
	}
	for !terminal(row.Status) && ctx.Err() == nil {
		next := s.step(ctx, &row, action)
		if next != nil {
			return
		}
	}
}

// step performs one state's work. It returns non-nil to stop (lost lease,
// cancellation, or a state to resume later).
func (s *Service) step(ctx context.Context, row *ledgerRow, a v3proposals.RollbackAction) error {
	rollback := v3kube.Rollback{Namespace: a.Namespace, Deployment: a.Deployment, UID: a.UID,
		ExpectedTemplateDigest: a.ExpectedTemplateDigest, ExpectedSpecDigest: a.ExpectedSpecDigest,
		TargetTemplate: a.TargetTemplate, TargetTemplateDigest: a.TargetTemplateDigest, OperationID: row.ExecutionID,
		ChangeCause: "aiops rollback to revision " + a.TargetRevision + " (execution " + row.ExecutionID + ")"}
	switch row.Status {
	case stateReserved:
		bound, err := s.boundWorkload(ctx, s.DB, a)
		if err != nil {
			return s.finish(ctx, row, a, stateNeedsAttention, "binding_changed")
		}
		client, err := s.writeClient(ctx, a.EnvironmentID, bound.Workload, bound.Binding)
		if err != nil {
			return s.finish(ctx, row, a, stateNeedsAttention, "write_unavailable")
		}
		if err := s.transition(ctx, row, a, stateDispatching, "", nil); err != nil {
			return err
		}
		outcome, err := client.Apply(ctx, rollback)
		switch {
		case errors.Is(err, v3kube.ErrForbidden):
			return s.finish(ctx, row, a, stateNeedsAttention, "write_forbidden")
		case errors.Is(err, v3kube.ErrConflict), errors.Is(err, v3kube.ErrNotFound):
			return s.finish(ctx, row, a, stateNeedsAttention, "drift")
		case err != nil:
			return s.transition(ctx, row, a, stateUnknown, "", nil)
		case outcome.Drift:
			return s.finish(ctx, row, a, stateNeedsAttention, "drift")
		case outcome.Applied:
			return s.transition(ctx, row, a, stateApplied, "", func(r *ledgerRow) { r.Rollout = snapshot(outcome.Deployment, s.now()) })
		default:
			return s.transition(ctx, row, a, stateUnknown, "", nil)
		}
	case stateDispatching, stateUnknown:
		// The write may or may not have landed; only read from now on.
		since := row.enteredAt(row.Status)
		outcome, err := s.observe(ctx, a, rollback)
		switch {
		case err == nil && outcome.Applied:
			return s.transition(ctx, row, a, stateApplied, "", func(r *ledgerRow) { r.Rollout = snapshot(outcome.Deployment, s.now()) })
		case err == nil && outcome.Drift:
			return s.finish(ctx, row, a, stateNeedsAttention, "drift")
		case s.now().Sub(since) > 5*time.Minute:
			return s.finish(ctx, row, a, stateNeedsAttention, "reconcile_unknown")
		case row.Status == stateDispatching:
			return s.transition(ctx, row, a, stateUnknown, "", nil)
		}
		return s.sleep(ctx, or(s.PollInterval, defaultPollInterval))
	case stateApplied:
		return s.transition(ctx, row, a, stateRollingOut, "", nil)
	case stateRollingOut:
		since := row.enteredAt(stateRollingOut)
		outcome, err := s.observe(ctx, a, rollback)
		switch {
		case err == nil && !outcome.Applied:
			return s.finish(ctx, row, a, stateNeedsAttention, "target_changed")
		case err == nil && outcome.Deployment.ProgressDeadlineExceeded():
			return s.finish(ctx, row, a, stateFailed, "progress_deadline_exceeded", func(r *ledgerRow) { r.Rollout = snapshot(outcome.Deployment, s.now()) })
		case err == nil && outcome.Deployment.RolledOut():
			return s.transition(ctx, row, a, stateVerifying, "", func(r *ledgerRow) {
				r.Rollout = snapshot(outcome.Deployment, s.now())
				r.Verification = &verificationState{Plan: a.Plan(), Result: "pending", Samples: []verificationSample{}}
			})
		case s.now().Sub(since) > or(s.RolloutTimeout, defaultRolloutTimeout) && err != nil:
			// The Deployment could not be read (for example the read
			// credential is gone): the rollout's outcome is unknown, not failed.
			return s.finish(ctx, row, a, stateNeedsAttention, "reconcile_unknown")
		case s.now().Sub(since) > or(s.RolloutTimeout, defaultRolloutTimeout):
			return s.finish(ctx, row, a, stateFailed, "rollout_timeout")
		case err == nil:
			if err := s.save(ctx, row, func(r *ledgerRow) { r.Rollout = snapshot(outcome.Deployment, s.now()) }); err != nil {
				return err
			}
		}
		return s.sleep(ctx, or(s.PollInterval, defaultPollInterval))
	case stateVerifying:
		return s.verify(ctx, row, a, rollback)
	}
	return errors.New("unknown state")
}

// observe reads the Deployment with the connector's read credential.
func (s *Service) observe(ctx context.Context, a v3proposals.RollbackAction, r v3kube.Rollback) (v3kube.Outcome, error) {
	w, err := s.Registry.ResolveWorkload(ctx, s.DB, a.ApplicationID, a.EnvironmentID)
	if err != nil {
		return v3kube.Outcome{}, err
	}
	client, err := s.readClient(ctx, a.EnvironmentID, w)
	if err != nil {
		return v3kube.Outcome{}, err
	}
	return client.Reconcile(ctx, r)
}

func snapshot(d v3kube.Deployment, at time.Time) *rolloutSnapshot {
	snap := &rolloutSnapshot{Desired: d.Desired(), Updated: d.Status.UpdatedReplicas, Available: d.Status.AvailableReplicas, ObservedAt: at}
	if revision := d.Revision(); revision != "" {
		snap.CurrentRevision = &revision
	}
	return snap
}

// verify samples the reviewed queries. Samples in the first settle period
// are recorded but cannot end the rollback: the services' rate windows still
// include the bad release, and while Pods are replaced a query may have no
// value at all (PA-73). After it, every sample must pass: a value beyond a
// threshold fails the rollback, a missing value or query error needs
// attention. The observation window must be covered without a gap; a gap
// (the executor restarted and waited for the lease) restarts the window at
// the first sample after it.
func (s *Service) verify(ctx context.Context, row *ledgerRow, a v3proposals.RollbackAction, r v3kube.Rollback) error {
	settle := or(s.Settle, defaultSettle)
	observation := time.Duration(a.ObservationSeconds) * time.Second
	maxGap := max(3*or(s.SampleInterval, defaultSampleInterval), minSampleGap)
	started := row.enteredAt(stateVerifying)
	if row.Verification == nil || row.Verification.StartedAt == nil {
		now := s.now()
		if err := s.save(ctx, row, func(rw *ledgerRow) {
			rw.Verification = &verificationState{Plan: a.Plan(), StartedAt: &now, Result: "pending", Samples: []verificationSample{}}
		}); err != nil {
			return err
		}
		started = now
	} else {
		started = *row.Verification.StartedAt
	}
	for {
		now := s.now()
		outcome, err := s.observe(ctx, a, r)
		if err == nil && !outcome.Applied {
			return s.finish(ctx, row, a, stateNeedsAttention, "target_changed", func(rw *ledgerRow) { rw.Verification.Result = "unavailable" })
		}
		sample, state, queryErr := s.sample(ctx, a, now)
		if queryErr != nil {
			log.Printf("v3 rollout verification query failed (execution=%s): %v", row.ExecutionID, queryErr)
		}
		settled := now.Sub(started) >= settle
		if err := s.save(ctx, row, func(rw *ledgerRow) {
			rw.Verification.Samples = append(rw.Verification.Samples, sample)
			if len(rw.Verification.Samples) > maxSamples {
				rw.Verification.Samples = rw.Verification.Samples[len(rw.Verification.Samples)-maxSamples:]
			}
		}); err != nil {
			return err
		}
		switch {
		case settled && state == sampleFailed:
			return s.finish(ctx, row, a, stateFailed, "verification_failed", func(rw *ledgerRow) {
				rw.Verification.Result = "failed"
				rw.Verification.FinishedAt = &now
			})
		case settled && state == sampleUnavailable:
			return s.finish(ctx, row, a, stateNeedsAttention, "verification_unavailable", func(rw *ledgerRow) {
				rw.Verification.Result = "unavailable"
				rw.Verification.FinishedAt = &now
			})
		}
		windowEnd := observationStart(row.Verification.Samples, started.Add(settle), maxGap).Add(observation)
		if !now.Before(windowEnd.Add(confirmGrace)) && err != nil {
			// Recovery cannot be confirmed without seeing the Deployment
			// still at the target; a short read outage only extends the
			// sampling by up to confirmGrace.
			return s.finish(ctx, row, a, stateNeedsAttention, "verification_unavailable", func(rw *ledgerRow) {
				rw.Verification.Result = "unavailable"
				rw.Verification.FinishedAt = &now
			})
		}
		if !now.Before(windowEnd) && err == nil {
			return s.finish(ctx, row, a, stateSucceeded, "", func(rw *ledgerRow) {
				rw.Verification.Result = "passed"
				rw.Verification.FinishedAt = &now
			})
		}
		if err := s.sleep(ctx, or(s.SampleInterval, defaultSampleInterval)); err != nil {
			return err
		}
	}
}

// minSampleGap is the shortest gap between samples that breaks the
// observation window (normal spacing is the sample interval plus the reads).
const minSampleGap = 45 * time.Second

// observationStart is where the current continuous observation began: the
// end of the settle period, or the first sample after the last gap.
func observationStart(samples []verificationSample, settleEnd time.Time, maxGap time.Duration) time.Time {
	start := settleEnd
	for i := 1; i < len(samples); i++ {
		if samples[i].At.Sub(samples[i-1].At) > maxGap && samples[i].At.After(start) {
			start = samples[i].At
		}
	}
	return start
}

type sampleState int

const (
	sampleOK sampleState = iota
	sampleFailed
	sampleUnavailable
)

// sample evaluates the three reviewed queries at the sample time. Each must
// return one series; NaN or ±Inf (no traffic: 0/0) is a missing value. A value
// beyond its threshold makes the sample failed even when another is missing;
// otherwise any missing value makes it unavailable. Missing values are null
// in the ledger.
func (s *Service) sample(ctx context.Context, a v3proposals.RollbackAction, now time.Time) (verificationSample, sampleState, error) {
	out := verificationSample{At: now}
	if s.Metrics == nil {
		return out, sampleUnavailable, errors.New("metrics reader not configured")
	}
	v := a.Verification
	// Wall time: the reader rejects instants in the future, and the
	// injectable clock only governs elapsed time. Whole seconds, like the
	// other metric readers.
	at := time.Now().UTC().Truncate(time.Second)
	var firstErr error
	read := func(queryID string) *float64 {
		series, err := s.Metrics.QueryForExecutor(ctx, a.ApplicationID, a.EnvironmentID, v.ConnectorID, v.ResourceID, queryID, at)
		if err == nil && (len(series) != 1 || len(series[0].Samples) != 1) {
			err = errors.New("verification query must return one series")
		}
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("%s: %w", queryID, err)
			}
			return nil
		}
		value, err := strconv.ParseFloat(series[0].Samples[0].Value, 64)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
			return nil
		}
		return &value
	}
	out.RequestRate, out.ErrorRatio, out.SampleAgeSeconds = read(v.RequestRateQueryID), read(v.ErrorRatioQueryID), read(v.SampleAgeQueryID)
	failing := (out.RequestRate != nil && *out.RequestRate < v.MinRequestRate) ||
		(out.ErrorRatio != nil && *out.ErrorRatio > v.MaxErrorRatio) ||
		(out.SampleAgeSeconds != nil && *out.SampleAgeSeconds > maxSampleAge)
	switch {
	case failing:
		return out, sampleFailed, firstErr
	case out.RequestRate == nil || out.ErrorRatio == nil || out.SampleAgeSeconds == nil:
		return out, sampleUnavailable, firstErr
	}
	out.OK = true
	return out, sampleOK, nil
}

func (s *Service) sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// lease takes over an unfinished execution for this worker.
func (s *Service) lease(ctx context.Context, proposalID string) (ledgerRow, error) {
	row := ledgerRow{ProposalID: proposalID}
	var timeline, rollout, verification []byte
	err := s.DB.QueryRow(ctx, `UPDATE v3_rollout_executions SET lease_owner=$2,lease_expires_at=clock_timestamp()+interval '2 minutes'
 WHERE proposal_id=$1 AND status NOT IN ('succeeded','failed','needs_attention')
   AND (lease_owner=$2 OR lease_expires_at IS NULL OR lease_expires_at<clock_timestamp())
 RETURNING execution_id,environment_id,application_id,status,status_reason,timeline,rollout,verification`,
		proposalID, s.workerID).Scan(&row.ExecutionID, &row.Env, &row.App, &row.Status, &row.Reason, &timeline, &rollout, &verification)
	if err != nil {
		return row, err
	}
	_ = json.Unmarshal(timeline, &row.Timeline)
	if len(rollout) > 0 {
		row.Rollout = &rolloutSnapshot{}
		_ = json.Unmarshal(rollout, row.Rollout)
	}
	if len(verification) > 0 {
		row.Verification = &verificationState{}
		_ = json.Unmarshal(verification, row.Verification)
	}
	return row, nil
}

func (s *Service) loadAction(ctx context.Context, proposalID string) (v3proposals.RollbackAction, error) {
	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return v3proposals.RollbackAction{}, err
	}
	defer tx.Rollback(ctx)
	action, _, _, err := v3proposals.LoadRollbackActionTx(ctx, tx, proposalID)
	return action, err
}

// save persists the row's progress without changing its state.
func (s *Service) save(ctx context.Context, row *ledgerRow, mutate func(*ledgerRow)) error {
	return s.write(ctx, row, row.Status, "", mutate, false, v3proposals.RollbackAction{})
}

func (s *Service) transition(ctx context.Context, row *ledgerRow, a v3proposals.RollbackAction, status, reason string, mutate func(*ledgerRow)) error {
	return s.write(ctx, row, status, reason, mutate, false, a)
}

// finish records a terminal state and its audit row; it always returns a
// non-nil error so the caller stops.
func (s *Service) finish(ctx context.Context, row *ledgerRow, a v3proposals.RollbackAction, status, reason string, mutate ...func(*ledgerRow)) error {
	var m func(*ledgerRow)
	if len(mutate) > 0 {
		m = mutate[0]
	}
	if err := s.write(ctx, row, status, reason, m, true, a); err != nil {
		return err
	}
	return errFinished
}

var errFinished = errors.New("execution finished")

// write applies one fenced update: only the lease owner may change the row,
// and only from the state it last read.
func (s *Service) write(ctx context.Context, row *ledgerRow, status, reason string, mutate func(*ledgerRow), final bool,
	a v3proposals.RollbackAction) error {
	next := *row
	if mutate != nil {
		mutate(&next)
	}
	if status != row.Status {
		next.Timeline = append(append([]timelineEntry(nil), row.Timeline...), timelineEntry{status, s.now()})
	}
	next.Status = status
	next.Reason = nil
	if reason != "" {
		next.Reason = &reason
	}
	timeline, _ := json.Marshal(next.Timeline)
	var rollout, verification []byte
	if next.Rollout != nil {
		rollout, _ = json.Marshal(next.Rollout)
	}
	if next.Verification != nil {
		verification, _ = json.Marshal(next.Verification)
	}
	// Writes that finish an execution use a fresh context so a cancelled run
	// still records why it stopped.
	writeCtx := ctx
	if final {
		var cancel context.CancelFunc
		writeCtx, cancel = context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
	}
	tx, err := s.DB.Begin(writeCtx)
	if err != nil {
		return err
	}
	defer tx.Rollback(writeCtx)
	tag, err := tx.Exec(writeCtx, `UPDATE v3_rollout_executions SET status=$3,status_reason=$4,timeline=$5::jsonb,
 rollout=$6::jsonb,verification=$7::jsonb,updated_at=clock_timestamp()
 WHERE proposal_id=$1 AND lease_owner=$2 AND status=$8`, row.ProposalID, s.workerID, next.Status, next.Reason,
		timeline, nullJSON(rollout), nullJSON(verification), row.Status)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errLeaseLost
	}
	if status != row.Status && a.Kind != "" {
		if err = auditTx(writeCtx, tx, a, row.ProposalID, "v3.rollout."+status, status, next.Reason); err != nil {
			return err
		}
	}
	if err = tx.Commit(writeCtx); err != nil {
		return err
	}
	*row = next
	return nil
}

var errLeaseLost = errors.New("rollout lease lost")

func nullJSON(b []byte) any {
	if b == nil {
		return nil
	}
	return string(b)
}
