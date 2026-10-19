package main

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/destinywans/redstring/internal/v3alertrules"
)

// v3AlertRuleEnableWorker is the readiness boundary for new human approvals.
// The domain worker rechecks every durable decision before any remote write;
// this wrapper only reports whether that independent consumer is running.
type v3AlertRuleEnableWorker struct {
	writer       *v3alertrules.WriteService
	environments map[string]bool
	running      atomic.Bool
}

func newV3AlertRuleEnableWorker(writer *v3alertrules.WriteService, environments map[string]bool) *v3AlertRuleEnableWorker {
	bound := make(map[string]bool, len(environments))
	for env, enabled := range environments {
		if enabled {
			bound[env] = true
		}
	}
	return &v3AlertRuleEnableWorker{writer: writer, environments: bound}
}

func (w *v3AlertRuleEnableWorker) configured(env string) bool {
	return w != nil && w.writer != nil && w.writer.DB != nil && w.writer.Access != nil &&
		w.writer.Reader != nil && w.writer.Reader.Source != nil &&
		w.writer.EnableApproval != nil && w.writer.EnableApplier != nil &&
		env != "" && w.environments[env]
}

// Ready requires the exact configured environment, a running worker, and
// locally accessible approval and enable tables. It makes no GitLab, Argo CD
// or Prometheus request, and does not claim those providers are healthy.
func (w *v3AlertRuleEnableWorker) Ready(ctx context.Context, env string) error {
	if !w.configured(env) || !w.running.Load() {
		return errors.New("alert rule enable worker is not connected for this environment")
	}
	probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_, err := w.writer.DB.Exec(probeCtx, `SELECT 1 FROM v3_proposals,
 v3_proposal_actions, v3_proposal_approval_decisions,
 v3_proposal_approval_revocations, v3_alert_rule_enable_executions LIMIT 0`)
	if err != nil {
		return fmt.Errorf("alert rule enable worker tables unavailable: %w", err)
	}
	return nil
}

func (w *v3AlertRuleEnableWorker) Run(ctx context.Context) error {
	if w == nil || len(w.environments) == 0 || w.writer == nil || w.writer.DB == nil ||
		w.writer.Access == nil || w.writer.Reader == nil || w.writer.Reader.Source == nil ||
		w.writer.EnableApproval == nil || w.writer.EnableApplier == nil {
		return errors.New("alert rule enable worker is not configured")
	}
	if !w.running.CompareAndSwap(false, true) {
		return errors.New("alert rule enable worker is already running")
	}
	defer w.running.Store(false)
	return w.writer.RunEnableWorker(ctx)
}
