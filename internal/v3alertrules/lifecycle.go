package v3alertrules

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/store"
)

// RuleLifecycle is where an active rule is in its GitOps lifecycle, apart
// from whether it fires (D11 §11.6): active, disable_pending_approval,
// disabling or needs_attention. The IDs are null without a disable in
// progress.
type RuleLifecycle struct {
	State       string  `json:"state"`
	ProposalID  *string `json:"proposal_id"`
	ExecutionID *string `json:"execution_id"`
}

// PendingRuleChange is an enable or disable not in effect yet (D11 §11.6).
type PendingRuleChange struct {
	ProposalID    string  `json:"proposal_id"`
	ExecutionID   *string `json:"execution_id"`
	Operation     string  `json:"operation"`
	RuleName      string  `json:"rule_name"`
	Group         string  `json:"group"`
	ApplicationID string  `json:"application_id"`
	EnvironmentID string  `json:"environment_id"`
	State         string  `json:"state"`
	UpdatedAt     string  `json:"updated_at"`
	Href          string  `json:"href"`
}

const (
	maxPendingRuleChanges = 50
	settledPendingWindow  = 7 * 24 * time.Hour
)

// pendingState is a proposal's place before it takes effect, or "" once it
// is in effect or too old to list: pending_approval, executing,
// needs_attention, and failed, rejected or expired within seven days.
// lapsed says the approval expired or was revoked; lapsedAt is when.
func pendingState(status, execution, failure string, updated time.Time, lapsed bool, lapsedAt, now time.Time) string {
	switch status {
	case "pending":
		return "pending_approval"
	case "rejected":
		if now.Sub(updated) <= settledPendingWindow {
			return "rejected"
		}
		return ""
	case "approved":
	default:
		return ""
	}
	// No executor takes up an approval that lapsed before any execution
	// claim, so the change will not run (PA-217).
	if execution == "" && lapsed {
		if now.Sub(lapsedAt) <= settledPendingWindow {
			return "expired"
		}
		return ""
	}
	switch execution {
	case "", "reserved", "exporting", "uncertain", "merged", "pending_review":
		return "executing"
	case "awaiting_provider":
		if failure == "load_timeout" {
			return "needs_attention"
		}
		return "executing"
	case "stale_remote":
		return "needs_attention"
	case "failed":
		if now.Sub(updated) <= settledPendingWindow {
			return "failed"
		}
	}
	return ""
}

// RuleLifecycle reads the lifecycle of the listed rules and the pending
// enables and disables in envs, limited to proposals user may view
// (proposals.view on the application or its owner team).
func (s *WriteService) RuleLifecycle(ctx context.Context, user core.User, envs []string, ruleIDs []string) (map[string]RuleLifecycle, []PendingRuleChange, error) {
	if s == nil || s.DB == nil || s.Access == nil {
		return nil, nil, errors.New("alert rule lifecycle unavailable")
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT p.id,p.environment_id,p.application_id,p.status,a.action_kind,
 COALESCE(a.action->'spec'->>'name',''),COALESCE(a.action->'spec'->>'group',''),COALESCE(a.action->>'observed_rule_id',''),
 COALESCE(x.execution_id,''),COALESCE(x.state,''),COALESCE(x.failure_code,''),
 GREATEST(p.created_at,COALESCE(p.rejected_at,p.created_at),COALESCE(d.approved_at,p.created_at),COALESCE(x.updated_at,p.created_at),
  CASE WHEN x.proposal_id IS NULL AND (r.proposal_id IS NOT NULL OR d.expires_at<=clock_timestamp()) THEN LEAST(r.revoked_at,d.expires_at) END),
 COALESCE(r.proposal_id IS NOT NULL OR d.expires_at<=clock_timestamp(),false),COALESCE(LEAST(r.revoked_at,d.expires_at),p.created_at)
 FROM v3_proposals p JOIN v3_proposal_actions a ON a.proposal_id=p.id
 LEFT JOIN v3_proposal_approval_decisions d ON d.proposal_id=p.id
 LEFT JOIN v3_proposal_approval_revocations r ON r.proposal_id=p.id
 LEFT JOIN v3_alert_rule_enable_executions x ON x.proposal_id=p.id
 WHERE a.action_kind=ANY($1) AND p.environment_id=ANY($2)
 AND (p.status<>'rejected' OR p.rejected_at>clock_timestamp()-interval '7 days')
 AND (x.state IS NULL OR x.state NOT IN ('enabled','disabled'))
 ORDER BY 12 DESC LIMIT 500`, alertRuleActionKinds, envs)
	if err != nil {
		return nil, nil, err
	}
	type row struct {
		id, env, app, status, kind, name, group, observed, execution, state, failure string
		updated, lapsedAt                                                            time.Time
		lapsed                                                                       bool
	}
	var all []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.env, &r.app, &r.status, &r.kind, &r.name, &r.group, &r.observed,
			&r.execution, &r.state, &r.failure, &r.updated, &r.lapsed, &r.lapsedAt); err != nil {
			rows.Close()
			return nil, nil, err
		}
		all = append(all, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	listed := make(map[string]bool, len(ruleIDs))
	for _, id := range ruleIDs {
		listed[id] = true
	}
	visible := map[string]bool{}
	lifecycle := map[string]RuleLifecycle{}
	pending := []PendingRuleChange{}
	now := time.Now()
	for _, r := range all {
		key := r.env + "\x00" + r.app
		allowed, known := visible[key]
		if !known {
			err := s.authorizeProposalsViewTx(ctx, tx, user, r.env, r.app)
			if err != nil && !errors.Is(err, store.ErrForbidden) && !errors.Is(err, pgx.ErrNoRows) {
				return nil, nil, err
			}
			allowed = err == nil
			visible[key] = allowed
		}
		state := pendingState(r.status, r.state, r.failure, r.updated, r.lapsed, r.lapsedAt, now)
		if !allowed || state == "" {
			continue
		}
		var execution *string
		if r.execution != "" {
			id := r.execution
			execution = &id
		}
		operation, href := "enable", "/review/"+r.id
		if r.kind == alertRuleDisableKind {
			operation = "disable"
		}
		if execution != nil {
			href = "/executions/" + *execution
		}
		if len(pending) < maxPendingRuleChanges {
			pending = append(pending, PendingRuleChange{ProposalID: r.id, ExecutionID: execution, Operation: operation,
				RuleName: r.name, Group: r.group, ApplicationID: r.app, EnvironmentID: r.env, State: state,
				UpdatedAt: r.updated.UTC().Format(time.RFC3339), Href: href})
		}
		// The newest disable in progress decides a listed rule's lifecycle.
		if operation != "disable" || !listed[r.observed] || state == "rejected" || state == "expired" {
			continue
		}
		if _, done := lifecycle[r.observed]; done {
			continue
		}
		life := map[string]string{"pending_approval": "disable_pending_approval", "executing": "disabling",
			"needs_attention": "needs_attention", "failed": "needs_attention"}[state]
		proposal := r.id
		lifecycle[r.observed] = RuleLifecycle{State: life, ProposalID: &proposal, ExecutionID: execution}
	}
	return lifecycle, pending, tx.Commit(ctx)
}
