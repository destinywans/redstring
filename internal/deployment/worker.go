package deployment

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/store"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

func (s *Service) claim(ctx context.Context) (Execution, error) {
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return Execution{}, e
	}
	defer tx.Rollback(ctx)
	var id, previous string
	e = tx.QueryRow(ctx, `SELECT id,status FROM deployment_executions WHERE status IN('queued','reconciling') OR (status IN('executing','verifying') AND lease_until<now()) ORDER BY created_at FOR UPDATE SKIP LOCKED LIMIT 1`).Scan(&id, &previous)
	if e != nil {
		return Execution{}, e
	}
	token := store.ID()
	if _, e = tx.Exec(ctx, "UPDATE deployment_executions SET status='executing',lease_token=$2,lease_until=now()+interval '30 seconds',updated_at=now() WHERE id=$1", id, token); e != nil {
		return Execution{}, e
	}
	if e = tx.Commit(ctx); e != nil {
		return Execution{}, e
	}
	x, e := s.loadExecution(ctx, id)
	x.token = token
	x.recovery = previous != "queued"
	return x, e
}
func (s *Service) transition(ctx context.Context, x Execution, status string, result Result) error {
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	raw, _ := json.Marshal(result)
	tag, e := tx.Exec(ctx, "UPDATE deployment_executions SET status=$3,result=$4,updated_at=now() WHERE id=$1 AND lease_token=$2 AND lease_until>now()", x.ID, x.token, status, raw)
	if e != nil {
		return e
	}
	if tag.RowsAffected() == 0 {
		return store.ErrLease
	}
	if _, e = tx.Exec(ctx, "INSERT INTO deployment_events(execution_id,type,payload) VALUES($1,$2,$3)", x.ID, status, raw); e != nil {
		return e
	}
	if status == "succeeded" || status == "cancelled" || status == "failed" {
		if _, e = tx.Exec(ctx, "DELETE FROM deployment_environment_locks WHERE execution_id=$1", x.ID); e != nil {
			return e
		}
	}
	var applicationID string
	if e = tx.QueryRow(ctx, "SELECT application_id FROM deployment_plans WHERE id=$1", x.PlanID).Scan(&applicationID); e != nil {
		return e
	}
	if e = audit(ctx, tx, x.CreatedBy, applicationID, "deployment."+status, x.ID, result); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (s *Service) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ticker := time.NewTicker(time.Second)
			defer ticker.Stop()
			for {
				if ctx.Err() != nil {
					return
				}
				x, e := s.claim(ctx)
				if e == nil {
					s.perform(ctx, x)
					continue
				}
				if !errors.Is(e, pgx.ErrNoRows) && ctx.Err() == nil {
					slog.Error("deployment claim failed", "error", e)
				}
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
				}
			}
		}()
	}
	wg.Wait()
}
func (s *Service) perform(parent context.Context, x Execution) {
	ctx, cancel := context.WithTimeout(parent, 20*time.Minute)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(8 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				tag, e := s.DB.Exec(ctx, "UPDATE deployment_executions SET lease_until=now()+interval '30 seconds' WHERE id=$1 AND lease_token=$2 AND lease_until>now()", x.ID, x.token)
				if e != nil || tag.RowsAffected() == 0 {
					cancel()
					return
				}
			}
		}
	}()
	defer func() { cancel(); <-done }()
	finish := func(status, message string, result Result) {
		result.Message = message
		result.Status = status
		result.ObservedAt = time.Now().UTC()
		cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer stop()
		if e := s.transition(cleanup, x, status, result); e != nil && !errors.Is(e, store.ErrLease) {
			slog.Error("deployment result commit failed", "execution", x.ID, "error", e)
		}
	}
	if x.CancelRequested && !x.recovery {
		finish("cancelled", "Cancelled before external dispatch", Result{})
		return
	}
	p, e := s.loadPlan(ctx, x.PlanID)
	if e != nil {
		finish("needs_attention", "Plan cannot be recovered", Result{})
		return
	}
	u, e := s.Store.ActiveUser(ctx, x.CreatedBy)
	if e != nil {
		finish("needs_attention", "Execution authority no longer valid", Result{})
		return
	}
	approved, e := s.Store.ActiveUser(ctx, p.ApprovedBy)
	if e != nil {
		finish("needs_attention", "Approval authority no longer valid", Result{})
		return
	}
	if e = s.check(ctx, approved, p, false); e != nil {
		finish("needs_attention", "Approved input or authority changed", Result{})
		return
	}
	if e = s.check(ctx, u, p, !x.recovery); e != nil {
		if !x.recovery {
			finish("failed", "Target or input changed before dispatch; create a new plan", Result{})
		} else {
			finish("needs_attention", "Target authority or input changed during recovery", Result{})
		}
		return
	}
	t, _ := s.target(p.TargetID)
	adapter := s.adapters[t.Kind]
	var result Result
	if x.recovery {
		result, e = adapter.Reconcile(ctx, t, p, x.ID)
	} else {
		result, e = adapter.Apply(ctx, t, p, x.ID)
	}
	if ctx.Err() != nil {
		return
	}
	if e != nil || result.Status == "needs_attention" || result.Status == "not_found" {
		finish("needs_attention", "External outcome needs inspection; no blind write retry", result)
		return
	}
	if result.Status == "failed" || result.Status == "drift" {
		finish("needs_attention", "External operation did not converge; inspect target before retry", result)
		return
	}
	deadline := time.NewTimer(10 * time.Minute)
	defer deadline.Stop()
	for result.Status != "succeeded" {
		select {
		case <-ctx.Done():
			return
		case <-deadline.C:
			finish("needs_attention", "Timed out waiting for the recorded external operation", result)
			return
		case <-time.After(3 * time.Second):
		}
		result, e = adapter.Reconcile(ctx, t, p, x.ID)
		if e != nil || result.Status == "needs_attention" || result.Status == "failed" || result.Status == "not_found" {
			finish("needs_attention", "External operation could not be confirmed", result)
			return
		}
	}
	if e = s.transition(ctx, x, "verifying", result); e != nil {
		return
	}
	samples, e := s.health(ctx, t.Health, func() error {
		observed, err := adapter.Reconcile(ctx, t, p, x.ID)
		if err != nil || observed.Status != "succeeded" {
			// Losing remote confirmation is not a known completed deployment.
			// Keep environment ownership until explicit reconciliation establishes state.
			return ErrUnknown
		}
		return nil
	})
	result.HealthSamples = samples
	if ctx.Err() != nil {
		return
	}
	if e != nil {
		if errors.Is(e, ErrUnknown) {
			finish("needs_attention", "Deployment state became uncertain during the health window", result)
		} else {
			finish("failed", "Deployment completed but business health verification failed", result)
		}
		return
	}
	finish("succeeded", "Approved artifact deployed and business HTTP checks remained healthy", result)
}
func (s *Service) health(ctx context.Context, h HealthCheck, verify func() error) (int, error) {
	seconds := h.WindowSeconds
	if seconds == 0 {
		seconds = 180
	}
	deadline := time.Now().Add(time.Duration(seconds) * time.Second)
	samples := 0
	for {
		r, e := http.NewRequestWithContext(ctx, "GET", h.URL, nil)
		if e != nil {
			return samples, e
		}
		response, e := s.client.Do(r)
		if e != nil {
			return samples, e
		}
		raw, e := io.ReadAll(io.LimitReader(response.Body, 65537))
		response.Body.Close()
		if e != nil || len(raw) > 65536 || response.StatusCode != 200 || (h.Contains != "" && !strings.Contains(string(raw), h.Contains)) {
			return samples, errors.New("business health check failed")
		}
		if verify != nil {
			if err := verify(); err != nil {
				return samples, err
			}
		}
		samples++
		if time.Now().After(deadline) {
			return samples, nil
		}
		select {
		case <-ctx.Done():
			return samples, ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
}
