package v3proposals

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
	"github.com/wans/aiops-canvas/internal/v3alertmanager"
)

const silenceProposalSource = "human_request"

var silenceProposalKey = regexp.MustCompile(`^[A-Za-z0-9._:-]{8,128}$`)

// SilenceProposalRequest is accepted only from a trusted in-process caller
// with an authenticated human actor. No product POST route uses it yet.
type SilenceProposalRequest struct {
	InvestigationID string
	Silence         v3alertmanager.SilenceCreateInput
}

type SilenceProposalResult struct {
	ProposalID   string
	ActionDigest string
	Status       string
}

// SilenceActionSnapshot is a proposal-time fact, not approval or execution
// authority. The Alertmanager window and operation ID are deliberately absent.
type SilenceActionSnapshot struct {
	SourceKind             string                               `json:"source_kind"`
	InvestigationID        string                               `json:"investigation_id"`
	InvestigationUpdatedAt time.Time                            `json:"investigation_updated_at"`
	Silence                v3alertmanager.ProposedSilenceCreate `json:"silence"`
}

type silenceRequestIdentity struct {
	ActorID         string                   `json:"actor_id"`
	InvestigationID string                   `json:"investigation_id"`
	EnvironmentID   string                   `json:"environment_id"`
	ApplicationID   string                   `json:"application_id"`
	IdempotencyKey  string                   `json:"idempotency_key"`
	Matchers        []v3alertmanager.Matcher `json:"matchers"`
	DurationSeconds int64                    `json:"duration_seconds"`
	Comment         string                   `json:"comment"`
}

func canonicalProposalJSON(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, err
	}
	return json.Marshal(decoded)
}

func proposalDigest(domain string, canonical []byte) string {
	sum := sha256.Sum256(append([]byte(domain+"\x00"), canonical...))
	return hex.EncodeToString(sum[:])
}

func silenceRequestHash(action SilenceActionSnapshot) (string, error) {
	in := silenceRequestIdentity{
		ActorID: action.Silence.ActorID, InvestigationID: action.InvestigationID,
		EnvironmentID: action.Silence.EnvironmentID, ApplicationID: action.Silence.ApplicationID,
		IdempotencyKey: action.Silence.IdempotencyKey, Matchers: action.Silence.Matchers,
		DurationSeconds: action.Silence.DurationSeconds, Comment: action.Silence.Comment,
	}
	raw, err := canonicalProposalJSON(in)
	if err != nil {
		return "", err
	}
	return proposalDigest("aiops.v3.silence.proposal.request.v1", raw), nil
}

func silenceReviewContent(action SilenceActionSnapshot) Content {
	s := action.Silence
	lines := make([]string, 0, len(s.Matchers)+2)
	for _, matcher := range s.Matchers {
		op := "!="
		if matcher.IsEqual {
			op = "="
		}
		if matcher.IsRegex {
			if matcher.IsEqual {
				op = "=~"
			} else {
				op = "!~"
			}
		}
		lines = append(lines, fmt.Sprintf("%s %s %q", matcher.Name, op, matcher.Value))
	}
	lines = append(lines, fmt.Sprintf("Duration: %d seconds", s.DurationSeconds), fmt.Sprintf("Comment: %q", s.Comment))
	return Content{
		Title:           fmt.Sprintf("Silence application %q", s.ApplicationLabel),
		Risk:            "high",
		Impact:          "Matching alerts for this application and environment would be silenced for the requested duration if a separate approval and execution flow is later enabled.",
		Reversible:      false,
		ProposedBy:      s.ActorID,
		InvestigationID: action.InvestigationID,
		Target:          fmt.Sprintf("Alertmanager application=%q, %s=%q", s.ApplicationLabel, s.EnvironmentLabel, s.EnvironmentID),
		Diff:            Diff{Path: "alertmanager/silences", Lines: lines},
		Method:          "Human-requested application-scoped Alertmanager silence. No silence is created by this proposal.",
		Evidence: []Evidence{{N: 1, Icon: "activity", Title: "Request context",
			Desc: "Authenticated human request linked to an open investigation in the same active application and environment."}},
		Verification:     []string{"Recheck the immutable action digest and current application, environment, access grants, and server Alertmanager binding before any future execution."},
		VerificationNote: "No current alert count or upstream silence state was asserted at proposal time.",
		Checklist:        []string{"Independent human approval with fresh passkey proof is required before execution."},
		Requirement:      "This record is pending review and grants no permission to write to Alertmanager.",
	}
}

// CreateSilenceProposal persists a canonical, digest-bound action and the
// human-readable review content atomically. It makes no Alertmanager call and
// creates no write reservation. The caller must supply the authenticated user.
func (s *Service) CreateSilenceProposal(ctx context.Context, actor core.User, in SilenceProposalRequest,
	ledger *v3alertmanager.DatabaseSilenceLedger, client *v3alertmanager.Client) (SilenceProposalResult, error) {
	var zero SilenceProposalResult
	if s == nil || s.DB == nil || s.Access == nil || ledger == nil || ledger.DB != s.DB || client == nil ||
		!bounded(in.InvestigationID, 128) || !silenceProposalKey.MatchString(in.Silence.IdempotencyKey) {
		return zero, ErrInvalidProposal
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback(ctx)
	// The unique actor/key index is the durable boundary. This lock makes
	// concurrent retries resolve to one row without catching an aborted tx.
	lockKey := proposalDigest("aiops.v3.silence.proposal.idempotency.lock.v1",
		[]byte(actor.ID+"\x00"+in.Silence.IdempotencyKey))
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, lockKey); err != nil {
		return zero, err
	}
	// Reject non-human or inactive identities before resolving a catalog label.
	// This read holds no row lock; the authoritative check follows below.
	if err = preauthorizeSilenceProposalActor(ctx, tx, actor); err != nil {
		return zero, err
	}
	// Resolve and lock the server-bound catalog target before any global grant
	// table lock. A slow application or investigation writer must not stall
	// unrelated account and role revocations across the instance.
	target, err := ledger.LockCreateProposalTargetWithTx(ctx, tx, in.Silence, client)
	if err != nil {
		return zero, err
	}
	// This non-locking admission check prevents investigation existence/status
	// from being observable to a caller without both permissions. A revoke may
	// still race here; the locked checks below are the commit-time authority.
	if err = s.preauthorizeSilenceProposal(ctx, tx, actor, target, ledger); err != nil {
		return zero, err
	}
	var investigationUpdatedAt time.Time
	err = tx.QueryRow(ctx, `SELECT i.updated_at FROM investigations i
 JOIN application_environments e ON e.id=i.environment_id AND e.application_id=i.application_id
 WHERE i.id=$1 AND i.application_id=$2 AND i.status<>'closed'
   AND e.key=$3 AND NOT e.archived
 FOR SHARE OF i`, in.InvestigationID, target.ApplicationID, target.EnvironmentID).Scan(&investigationUpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return zero, store.ErrNotFound
	}
	if err != nil {
		return zero, err
	}
	if err = lockSilenceProposalGrantSources(ctx, tx, actor); err != nil {
		return zero, err
	}
	prepared, err := ledger.PrepareCreateProposalWithTx(ctx, tx, actor, in.Silence, client)
	if err != nil {
		return zero, err
	}
	if prepared.EnvironmentID != target.EnvironmentID || prepared.ApplicationID != target.ApplicationID ||
		prepared.OwnerTeamID != target.OwnerTeamID || prepared.BindingFingerprint != target.BindingFingerprint {
		return zero, store.ErrConflict
	}
	decision := rbac.V3Decision{Module: "proposals", Action: "change", EnvironmentID: prepared.EnvironmentID,
		ApplicationID: prepared.ApplicationID}
	if err = s.Access.AuthorizeWithTx(ctx, tx, actor, decision); errors.Is(err, store.ErrForbidden) && prepared.OwnerTeamID != "" {
		decision.ApplicationID = ""
		decision.TeamID = prepared.OwnerTeamID
		err = s.Access.AuthorizeWithTx(ctx, tx, actor, decision)
	}
	if err != nil {
		return zero, err
	}
	// Recheck the authoritative investigation/scope after the grant locks.
	// The prelock rows remain stable, and the policy reads now see any revoke
	// that committed while this transaction was waiting for those rows.
	err = tx.QueryRow(ctx, `SELECT i.updated_at FROM investigations i
 JOIN application_environments e ON e.id=i.environment_id AND e.application_id=i.application_id
 WHERE i.id=$1 AND i.application_id=$2 AND i.status<>'closed'
   AND e.key=$3 AND NOT e.archived
 FOR SHARE OF i`, in.InvestigationID, prepared.ApplicationID, prepared.EnvironmentID).Scan(&investigationUpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return zero, store.ErrNotFound
	}
	if err != nil {
		return zero, err
	}
	action := SilenceActionSnapshot{SourceKind: silenceProposalSource,
		InvestigationID: in.InvestigationID, InvestigationUpdatedAt: investigationUpdatedAt.UTC(), Silence: prepared}
	requestHash, err := silenceRequestHash(action)
	if err != nil {
		return zero, err
	}
	var existingID string
	err = tx.QueryRow(ctx, `SELECT proposal_id FROM v3_proposal_actions
 WHERE creator_id=$1 AND idempotency_key=$2 FOR UPDATE`, actor.ID, prepared.IdempotencyKey).Scan(&existingID)
	if err == nil {
		stored, result, loadErr := loadSilenceActionWithTx(ctx, tx, existingID)
		if loadErr != nil {
			return zero, loadErr
		}
		storedRequestHash, hashErr := silenceRequestHash(stored)
		if hashErr != nil {
			return zero, ErrInvalidProposal
		}
		if !reflect.DeepEqual(stored.Silence, prepared) || storedRequestHash != requestHash {
			return zero, store.ErrConflict
		}
		if err = tx.Commit(ctx); err != nil {
			return zero, err
		}
		return result, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return zero, err
	}
	actionRaw, err := canonicalProposalJSON(action)
	if err != nil {
		return zero, err
	}
	content := silenceReviewContent(action)
	if !validContent(content) {
		return zero, ErrInvalidProposal
	}
	contentRaw, err := canonicalProposalJSON(content)
	if err != nil {
		return zero, err
	}
	actionDigest := proposalDigest("aiops.v3.proposal.action.v1", actionRaw)
	contentDigest := proposalDigest("aiops.v3.proposal.review.v1", contentRaw)
	id := store.ID()
	_, err = tx.Exec(ctx, `INSERT INTO v3_proposals
 (id,investigation_id,application_id,environment_id,content,created_by,creator_kind)
 VALUES($1,$2,$3,$4,$5::jsonb,$6,'user')`, id, action.InvestigationID,
		prepared.ApplicationID, prepared.EnvironmentID, contentRaw, actor.ID)
	if err != nil {
		return zero, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO v3_proposal_actions
 (proposal_id,action_kind,schema_version,action,action_digest,review_content_digest,request_hash,
  binding_fingerprint,creator_id,environment_id,application_id,investigation_id,
  investigation_updated_at,idempotency_key,source_kind)
 VALUES($1,$2,$3,$4::jsonb,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`,
		id, prepared.Kind, prepared.SchemaVersion, actionRaw, actionDigest, contentDigest, requestHash,
		prepared.BindingFingerprint, actor.ID, prepared.EnvironmentID, prepared.ApplicationID,
		action.InvestigationID, investigationUpdatedAt, prepared.IdempotencyKey, action.SourceKind)
	if err != nil {
		return zero, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO management_audit(actor_id,actor_kind,application_id,action,target,outcome,changes)
 VALUES($1,'user',$2,'v3.proposal.silence_create_requested',$3,'success',
 jsonb_build_object('environment_id',$4::text,'investigation_id',$5::text,'action_digest',$6::text,'review_content_digest',$7::text))`,
		actor.ID, prepared.ApplicationID, id, prepared.EnvironmentID, action.InvestigationID, actionDigest, contentDigest)
	if err != nil {
		return zero, err
	}
	if err = tx.Commit(ctx); err != nil {
		return zero, err
	}
	return SilenceProposalResult{ProposalID: id, ActionDigest: actionDigest, Status: "pending"}, nil
}

func preauthorizeSilenceProposalActor(ctx context.Context, tx pgx.Tx, actor core.User) error {
	if actor.ID == "" || actor.Disabled {
		return store.ErrForbidden
	}
	var active bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id=$1 AND NOT disabled
 AND deleted_at IS NULL AND source<>'settings_demo' AND v3_identity_kind='human')`, actor.ID).Scan(&active); err != nil {
		return err
	}
	if !active {
		return store.ErrForbidden
	}
	return nil
}

func (s *Service) preauthorizeSilenceProposal(ctx context.Context, tx pgx.Tx, actor core.User,
	target v3alertmanager.ProposedSilenceTarget, ledger *v3alertmanager.DatabaseSilenceLedger) error {
	for _, permission := range []struct {
		access rbacAccess
		module string
		action string
		hide   bool
	}{
		{ledger.Access, "silences", "manage", true},
		{s.Access, "proposals", "change", false},
	} {
		decision := rbac.V3Decision{Module: permission.module, Action: permission.action,
			EnvironmentID: target.EnvironmentID, ApplicationID: target.ApplicationID}
		err := permission.access.AuthorizeWithTx(ctx, tx, actor, decision)
		if errors.Is(err, store.ErrForbidden) && target.OwnerTeamID != "" {
			decision.ApplicationID = ""
			decision.TeamID = target.OwnerTeamID
			err = permission.access.AuthorizeWithTx(ctx, tx, actor, decision)
		}
		if errors.Is(err, store.ErrForbidden) && permission.hide {
			return store.ErrNotFound
		}
		if err != nil {
			return err
		}
	}
	return nil
}

type rbacAccess interface {
	AuthorizeWithTx(context.Context, pgx.Tx, core.User, rbac.V3Decision) error
}

// Lock every physical source of a V3 grant before the authoritative policy read and
// hold the locks through proposal commit. A concurrent revoke either commits
// first and is observed or waits until the proposal is committed. Locking the
// actor row before the grant tables matches account writers' row/table order;
// do not lock rbac_roles afterward because role writers lock it first.
func lockSilenceProposalGrantSources(ctx context.Context, tx pgx.Tx, actor core.User) error {
	if actor.ID == "" || actor.Disabled {
		return store.ErrForbidden
	}
	var id string
	err := tx.QueryRow(ctx, `SELECT id FROM users WHERE id=$1 AND NOT disabled AND deleted_at IS NULL
 AND source<>'settings_demo' AND v3_identity_kind='human' FOR SHARE`, actor.ID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.ErrForbidden
	}
	if err != nil {
		return err
	}
	// Table SHARE locks also cover direct grant/assignment DML that does not
	// participate in the account and role writers' advisory-lock protocol.
	_, err = tx.Exec(ctx, `LOCK TABLE rbac_user_roles,rbac_department_roles,v3_role_grants IN SHARE MODE`)
	return err
}

// LoadSilenceActionWithTx verifies the immutable snapshot and review content
// against their stored digests and relational identity. It does not approve or
// authorize the action; future gates must separately revalidate current scope.
func LoadSilenceActionWithTx(ctx context.Context, tx pgx.Tx, proposalID string) (SilenceActionSnapshot, SilenceProposalResult, error) {
	return loadSilenceActionWithTx(ctx, tx, proposalID)
}

func loadSilenceActionWithTx(ctx context.Context, tx pgx.Tx, proposalID string) (SilenceActionSnapshot, SilenceProposalResult, error) {
	var zero SilenceActionSnapshot
	var result SilenceProposalResult
	if tx == nil || proposalID == "" {
		return zero, result, ErrInvalidProposal
	}
	var actionRaw, contentRaw []byte
	var kind, actionDigest, contentDigest, requestHash, fingerprint, creatorID, envID, appID, invID, key, sourceKind, parentCreator, parentKind string
	var parentInvID, parentAppID, parentEnvID string
	var version int
	var invUpdatedAt time.Time
	err := tx.QueryRow(ctx, `SELECT a.action,a.action_kind,a.schema_version,a.action_digest,a.review_content_digest,
 a.request_hash,a.binding_fingerprint,a.creator_id,a.environment_id,a.application_id,
 a.investigation_id,a.investigation_updated_at,a.idempotency_key,a.source_kind,
 p.content,p.created_by,p.creator_kind,p.status,p.id,p.investigation_id,p.application_id,p.environment_id
 FROM v3_proposal_actions a JOIN v3_proposals p ON p.id=a.proposal_id WHERE a.proposal_id=$1 FOR SHARE OF a,p`,
		proposalID).Scan(&actionRaw, &kind, &version, &actionDigest, &contentDigest, &requestHash,
		&fingerprint, &creatorID, &envID, &appID, &invID, &invUpdatedAt, &key, &sourceKind,
		&contentRaw, &parentCreator, &parentKind, &result.Status, &result.ProposalID,
		&parentInvID, &parentAppID, &parentEnvID)
	if errors.Is(err, pgx.ErrNoRows) {
		return zero, SilenceProposalResult{}, store.ErrNotFound
	}
	if err != nil {
		return zero, SilenceProposalResult{}, err
	}
	var action SilenceActionSnapshot
	if err = json.Unmarshal(actionRaw, &action); err != nil {
		return zero, SilenceProposalResult{}, ErrInvalidProposal
	}
	canonicalAction, err := canonicalProposalJSON(action)
	if err != nil {
		return zero, SilenceProposalResult{}, ErrInvalidProposal
	}
	canonicalStored, err := canonicalProposalJSON(json.RawMessage(actionRaw))
	if err != nil || !bytes.Equal(canonicalAction, canonicalStored) {
		return zero, SilenceProposalResult{}, ErrInvalidProposal
	}
	computedRequestHash, err := silenceRequestHash(action)
	if err != nil || action.SourceKind != silenceProposalSource || sourceKind != action.SourceKind ||
		action.Silence.Kind != v3alertmanager.ProposedSilenceCreateKind || kind != action.Silence.Kind ||
		action.Silence.SchemaVersion != v3alertmanager.ProposedSilenceCreateVersion || version != action.Silence.SchemaVersion ||
		action.Silence.ActorID != creatorID || parentCreator != creatorID || parentKind != "user" ||
		action.Silence.EnvironmentID != envID || action.Silence.ApplicationID != appID ||
		action.InvestigationID != invID || parentInvID != invID || parentAppID != appID || parentEnvID != envID ||
		!action.InvestigationUpdatedAt.Equal(invUpdatedAt) ||
		action.Silence.IdempotencyKey != key || action.Silence.BindingFingerprint != fingerprint ||
		computedRequestHash != requestHash ||
		proposalDigest("aiops.v3.proposal.action.v1", canonicalAction) != actionDigest {
		return zero, SilenceProposalResult{}, ErrInvalidProposal
	}
	content := silenceReviewContent(action)
	if !validContent(content) {
		return zero, SilenceProposalResult{}, ErrInvalidProposal
	}
	canonicalContent, err := canonicalProposalJSON(content)
	if err != nil {
		return zero, SilenceProposalResult{}, ErrInvalidProposal
	}
	canonicalStoredContent, err := canonicalProposalJSON(json.RawMessage(contentRaw))
	if err != nil || !bytes.Equal(canonicalContent, canonicalStoredContent) ||
		proposalDigest("aiops.v3.proposal.review.v1", canonicalContent) != contentDigest {
		return zero, SilenceProposalResult{}, ErrInvalidProposal
	}
	result.ActionDigest = actionDigest
	return action, result, nil
}
