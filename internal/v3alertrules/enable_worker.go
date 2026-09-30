package v3alertrules

import (
	"context"
	"time"
)

// RunEnableWorker is the independent execution/reconciliation loop. The HTTP
// request only creates an immutable proposal; the composition root must start
// this worker separately after binding approval and GitOps sources. Every
// selected proposal is reverified by ExecuteApproved before a remote write.
func (s *WriteService) RunEnableWorker(ctx context.Context) error {
	if s == nil || s.DB == nil || s.Access == nil || s.Reader == nil ||
		s.Reader.Source == nil || s.EnableApproval == nil || s.EnableApplier == nil {
		return problem(503, "enable_not_connected", "Alert rule executor is not connected")
	}
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	var after string
	for {
		var err error
		after, err = s.enableWorkerPass(ctx, after)
		if err != nil && ctx.Err() == nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (s *WriteService) enableWorkerPass(ctx context.Context, after string) (string, error) {
	// The query is only a bounded hint. Claim, approval, grants, source and
	// immutable action are all rechecked inside the domain execution method.
	rows, err := s.DB.Query(ctx, `SELECT a.proposal_id,COALESCE(x.state,'')
 FROM v3_proposal_actions a
 JOIN v3_proposals p ON p.id=a.proposal_id
 JOIN v3_proposal_approval_decisions d ON d.proposal_id=a.proposal_id
 LEFT JOIN v3_alert_rule_enable_executions x ON x.proposal_id=a.proposal_id
 WHERE a.action_kind=$1 AND p.status='approved' AND a.proposal_id>$2
 AND ((x.state IN ('pending_review','awaiting_provider')
       AND x.updated_at<clock_timestamp()-interval '15 seconds')
   OR ((x.proposal_id IS NULL OR
        (x.state IN ('reserved','uncertain','exporting')
         AND x.updated_at<clock_timestamp()-interval '15 seconds'))
       AND d.expires_at>clock_timestamp()
       AND NOT EXISTS(SELECT 1 FROM v3_proposal_approval_revocations r
                      WHERE r.proposal_id=a.proposal_id)))
 ORDER BY a.proposal_id LIMIT 32`, alertRuleEnableKind, after)
	if err != nil {
		return after, err
	}
	type work struct{ id, state string }
	var batch []work
	for rows.Next() {
		var item work
		if err := rows.Scan(&item.id, &item.state); err != nil {
			rows.Close()
			return after, err
		}
		batch = append(batch, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return after, err
	}
	rows.Close()
	if len(batch) == 0 {
		return "", nil
	}
	for _, item := range batch {
		if ctx.Err() != nil {
			return after, ctx.Err()
		}
		if item.state == "pending_review" || item.state == "awaiting_provider" {
			_, _ = s.ObserveEnabled(ctx, item.id)
			continue
		}
		result, err := s.ExecuteApproved(ctx, item.id)
		if err == nil && result.Status == "pending_review" {
			_, _ = s.ObserveEnabled(ctx, item.id)
		}
	}
	return batch[len(batch)-1].id, nil
}
