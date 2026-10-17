package v3proposals

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// ProposalDecision is how a proposal was decided (PA-102), read from the
// approval records: the approval decision and the passkey votes it names,
// or the rejection. It is null while the proposal is pending, also with one
// of two votes cast; a vote alone decides nothing.
type ProposalDecision struct {
	// State is approved, rejected, or expired: approved, but the approval
	// lapsed before any executor took it up.
	State     string             `json:"state"`
	DecidedAt string             `json:"decided_at"`
	Approvers []DecisionApprover `json:"approvers"`
	// RejectedBy is set for a rejection.
	RejectedBy *DecisionActor `json:"rejected_by"`
	// Reason is what the rejecter wrote, trimmed; null when they wrote
	// nothing and for any other state. The proposer reads it on the review
	// page (PA-173).
	Reason *string `json:"reason"`
	// Revoked is set when an approval was withdrawn after it was given, for
	// instance because an approver lost the authority; its execution is then
	// refused.
	Revoked *DecisionActor `json:"revoked"`
}

// DecisionApprover is one counted approval: who, how, and when it was
// verified.
type DecisionApprover struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Method string `json:"method"`
	At     string `json:"at"`
}

// DecisionActor is who rejected or revoked, and when. Name is the display
// name, or the ID when the account has none.
type DecisionActor struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	At   string `json:"at"`
}

// decisionTx reads the decision of proposal v. execution is its projection,
// which says whether an executor took the approval up.
func decisionTx(ctx context.Context, tx pgx.Tx, v record, execution *ProposalExecution) (*ProposalDecision, error) {
	if v.Status == "rejected" {
		var by, name, reason string
		var at time.Time
		err := tx.QueryRow(ctx, `SELECT p.rejected_by,COALESCE(NULLIF(btrim(u.name),''),p.rejected_by),p.rejected_at,p.rejection_reason
 FROM v3_proposals p LEFT JOIN users u ON u.id=p.rejected_by
 WHERE p.id=$1 AND p.rejected_by IS NOT NULL AND p.rejected_at IS NOT NULL`, v.ID).Scan(&by, &name, &at, &reason)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		when := at.UTC().Format(time.RFC3339)
		out := &ProposalDecision{State: "rejected", DecidedAt: when, Approvers: []DecisionApprover{},
			RejectedBy: &DecisionActor{ID: by, Name: name, At: when}}
		if reason = strings.TrimSpace(reason); reason != "" {
			out.Reason = &reason
		}
		return out, nil
	}
	var first, second string
	var approvedAt, expiresAt time.Time
	var lapsed bool
	err := tx.QueryRow(ctx, `SELECT first_vote_id,second_vote_id,approved_at,expires_at,expires_at<=clock_timestamp()
 FROM v3_proposal_approval_decisions WHERE proposal_id=$1`, v.ID).Scan(&first, &second, &approvedAt, &expiresAt, &lapsed)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out := &ProposalDecision{State: "approved", DecidedAt: approvedAt.UTC().Format(time.RFC3339), Approvers: []DecisionApprover{}}
	rows, err := tx.Query(ctx, `SELECT v.approver_id,COALESCE(NULLIF(btrim(u.name),''),v.approver_id),v.verified_at
 FROM v3_proposal_approval_votes v LEFT JOIN users u ON u.id=v.approver_id
 WHERE v.proposal_id=$1 AND v.id=ANY($2) ORDER BY v.verified_at,v.id`, v.ID, []string{first, second})
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var a DecisionApprover
		var at time.Time
		if err = rows.Scan(&a.ID, &a.Name, &at); err != nil {
			return nil, err
		}
		// Every counted vote is a verified passkey assertion.
		a.Method, a.At = "passkey", at.UTC().Format(time.RFC3339)
		out.Approvers = append(out.Approvers, a)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	var by, name string
	var at time.Time
	err = tx.QueryRow(ctx, `SELECT r.revoked_by,COALESCE(NULLIF(btrim(u.name),''),r.revoked_by),r.revoked_at
 FROM v3_proposal_approval_revocations r LEFT JOIN users u ON u.id=r.revoked_by WHERE r.proposal_id=$1`, v.ID).Scan(&by, &name, &at)
	switch {
	case err == nil:
		out.Revoked = &DecisionActor{ID: by, Name: name, At: at.UTC().Format(time.RFC3339)}
	case !errors.Is(err, pgx.ErrNoRows):
		return nil, err
	}
	if lapsed && out.Revoked == nil && (execution == nil || execution.Status == nil || *execution.Status == "") {
		out.State, out.DecidedAt = "expired", expiresAt.UTC().Format(time.RFC3339)
	}
	return out, nil
}
