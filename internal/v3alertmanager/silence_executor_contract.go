package v3alertmanager

import (
	"context"
	"errors"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/store"
)

var (
	ErrSilenceApprovalUnavailable = errors.New("silence approval proof unavailable")
	ErrSilenceExecutionClaimed    = errors.New("silence execution was already claimed")
	ErrSilenceExecutionUnknown    = errors.New("silence execution outcome needs reconciliation")
)

var silenceDigestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// SilenceReviewedAction is verified immutable proposal evidence, loaded from
// the same transaction that will claim execution. Exactly one action pointer
// must be set for its kind. The source must reconstruct the review content and
// check both canonical digests, relational scope, creator and cancellation
// intent before returning it.
type SilenceReviewedAction struct {
	ProposalID             string
	Kind                   string
	Status                 string
	ActionDigest           string
	ReviewContentDigest    string
	CreatorID              string
	EnvironmentID          string
	ApplicationID          string
	InvestigationID        string
	InvestigationUpdatedAt time.Time
	Create                 *ProposedSilenceCreate
	Cancel                 *ProposedSilenceCancel
	CancelObservedSnapshot []byte
}

type SilenceReviewedActionSource interface {
	Ready(context.Context) error
	LoadReviewedActionWithTx(context.Context, pgx.Tx, string) (SilenceReviewedAction, error)
}

// SilenceWorkerReadiness must reflect a running independent dispatcher, not
// merely successful construction of an executor value. WAN-117 supplies its
// lifecycle signal when it starts the worker.
type SilenceWorkerReadiness interface {
	Ready(context.Context) error
}

type SilenceHumanApproval struct {
	UserID         string
	PasskeyProofID string
	VerifiedAt     time.Time
}

// SilenceApprovalProof is a receipt from a trusted durable verifier, not an
// HTTP input. ApprovalDigest identifies one immutable approval generation.
// Two distinct active human approvers other than the proposal creator must
// have signed fresh passkey assertions for this exact proposal and digests.
type SilenceApprovalProof struct {
	ProposalID          string
	ActionKind          string
	ActionDigest        string
	ReviewContentDigest string
	CreatorID           string
	EnvironmentID       string
	ApplicationID       string
	ApprovalDigest      string
	Status              string
	ApprovedAt          time.Time
	ExpiresAt           time.Time
	Approvers           []SilenceHumanApproval
}

// SilenceApprovalAuthority is supplied by WAN-117's passkey approval owner.
// VerifyApprovedWithTx must lock persisted approval and proposal rows in tx,
// check their current approved/non-revoked state, exact proposal/action/review
// digests, two distinct current human approvers, their scoped approval grants
// and consumed passkey proofs. The approval revoke path must lock the same
// rows and reject revocation after an execution claim commits. An arbitrary
// in-memory boolean or caller-provided proof cannot implement this boundary.
type SilenceApprovalAuthority interface {
	Ready(context.Context, string, string) error
	VerifyApprovedWithTx(context.Context, pgx.Tx, SilenceReviewedAction) (SilenceApprovalProof, error)
}

// SilenceExecutionResult is a durable local execution state. A confirmed
// create requires a unique matching upstream readback; a confirmed cancel
// requires a fresh observation proving the exact silence expired.
type SilenceExecutionResult struct {
	ProposalID  string
	Kind        string
	State       string
	OperationID string
	UpstreamID  string
}

// SilenceExecutor is run by an independent worker, never an HTTP handler.
// WAN-117 supplies the immutable action source and durable passkey approval
// authority and starts this worker only after those pieces are connected.
type SilenceExecutor struct {
	Ledger    *DatabaseSilenceLedger
	Client    *Client
	Actions   SilenceReviewedActionSource
	Approvals SilenceApprovalAuthority
	Worker    SilenceWorkerReadiness
}

func (e *SilenceExecutor) CreateReady(ctx context.Context, environmentID string) error {
	return e.ready(ctx, environmentID, ProposedSilenceCreateKind)
}

func (e *SilenceExecutor) CancelReady(ctx context.Context, environmentID string) error {
	return e.ready(ctx, environmentID, ProposedSilenceCancelKind)
}

func (e *SilenceExecutor) ready(ctx context.Context, environmentID, kind string) error {
	if e == nil || e.Ledger == nil || e.Ledger.DB == nil || e.Ledger.Access == nil ||
		e.Client == nil || e.Actions == nil || e.Approvals == nil || e.Worker == nil ||
		environmentID == "" || environmentID == "all" || e.Ledger.EnvironmentLabel(environmentID) == "" {
		return ErrNotConfigured
	}
	if _, ok := e.Client.endpoints[environmentID]; !ok {
		return ErrNotConfigured
	}
	if err := e.Actions.Ready(ctx); err != nil {
		return err
	}
	if err := e.Worker.Ready(ctx); err != nil {
		return err
	}
	if err := e.Approvals.Ready(ctx, environmentID, kind); err != nil {
		if errors.Is(err, ErrNotConfigured) || errors.Is(err, ErrSilenceApprovalUnavailable) {
			return ErrNotConfigured
		}
		return err
	}
	if _, err := e.Ledger.DB.Exec(ctx, `SELECT 1 FROM v3_silence_execution_claims LIMIT 0`); err != nil {
		return ErrNotConfigured
	}
	return nil
}

func (e *SilenceExecutor) loadApprovedWithTx(ctx context.Context, tx pgx.Tx,
	proposalID, kind string) (SilenceReviewedAction, SilenceApprovalProof, error) {
	var empty SilenceReviewedAction
	var noProof SilenceApprovalProof
	if e == nil || e.Ledger == nil || e.Actions == nil || e.Approvals == nil || tx == nil || proposalID == "" {
		return empty, noProof, ErrNotConfigured
	}
	action, err := e.Actions.LoadReviewedActionWithTx(ctx, tx, proposalID)
	if err != nil {
		return empty, noProof, err
	}
	if action.ProposalID != proposalID || action.Kind != kind || action.Status != "approved" ||
		!silenceDigestPattern.MatchString(action.ActionDigest) ||
		!silenceDigestPattern.MatchString(action.ReviewContentDigest) ||
		action.CreatorID == "" || action.EnvironmentID == "" || action.EnvironmentID == "all" ||
		action.ApplicationID == "" || action.InvestigationID == "" ||
		(kind == ProposedSilenceCreateKind && (action.Create == nil || action.Cancel != nil)) ||
		(kind == ProposedSilenceCancelKind && (action.Cancel == nil || action.Create != nil)) {
		return empty, noProof, store.ErrConflict
	}
	proof, err := e.Approvals.VerifyApprovedWithTx(ctx, tx, action)
	if err != nil {
		return empty, noProof, err
	}
	if err := validSilenceApprovalProof(action, proof, e.Ledger.currentTime()); err != nil {
		return empty, noProof, err
	}
	return action, proof, nil
}

func validSilenceApprovalProof(action SilenceReviewedAction, proof SilenceApprovalProof, now time.Time) error {
	if proof.ProposalID != action.ProposalID || proof.ActionKind != action.Kind ||
		proof.ActionDigest != action.ActionDigest || proof.ReviewContentDigest != action.ReviewContentDigest ||
		proof.CreatorID != action.CreatorID || proof.EnvironmentID != action.EnvironmentID ||
		proof.ApplicationID != action.ApplicationID || proof.Status != "approved" ||
		!silenceDigestPattern.MatchString(proof.ApprovalDigest) ||
		proof.ApprovedAt.IsZero() || proof.ApprovedAt.After(now) || !proof.ExpiresAt.After(now) ||
		len(proof.Approvers) < 2 {
		return ErrSilenceApprovalUnavailable
	}
	seenUser, seenPasskey := map[string]bool{}, map[string]bool{}
	for _, approver := range proof.Approvers {
		if approver.UserID == "" || approver.UserID == action.CreatorID || seenUser[approver.UserID] ||
			approver.PasskeyProofID == "" || seenPasskey[approver.PasskeyProofID] ||
			approver.VerifiedAt.IsZero() || approver.VerifiedAt.After(now) ||
			approver.VerifiedAt.After(proof.ApprovedAt) {
			return ErrSilenceApprovalUnavailable
		}
		seenUser[approver.UserID], seenPasskey[approver.PasskeyProofID] = true, true
	}
	return nil
}
