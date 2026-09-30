package v3conversation

import (
	"context"
	"errors"
	"regexp"
	"unicode"
	"unicode/utf8"

	"github.com/wans/aiops-canvas/internal/inference"
)

var errToolInvocation = errors.New("invalid tool invocation event")
var errEvidenceReceipt = errors.New("invalid or duplicate evidence receipt")
var auditTargetID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,256}$`)

// appendToolAudit is the durable boundary around an inference tool read. The
// runtime awaits the started callback before calling Read, and awaits the
// terminal callback before returning evidence to the model. Never put tool
// arguments, query text, evidence, or provider errors into management audit.
func (s *Service) appendToolAudit(ctx context.Context, owner, env, conv, reply, claim string, invocation *inference.ToolInvocation) error {
	if invocation == nil || owner == "" || env == "" || env == "all" || conv == "" || reply == "" || claim == "" ||
		invocation.Name == "" || invocation.StepID == "" || invocation.DurationMS < 0 ||
		(invocation.TargetID != "" && !auditTargetID.MatchString(invocation.TargetID)) {
		return errToolInvocation
	}
	outcome := "accepted"
	switch invocation.Status {
	case "started":
		if invocation.DurationMS != 0 || invocation.TargetID != "" {
			return errToolInvocation
		}
	case "succeeded":
		outcome = "succeeded"
	case "failed":
		if invocation.TargetID != "" {
			return errToolInvocation
		}
		outcome = "failed"
	default:
		return errToolInvocation
	}
	result, err := s.db.Exec(ctx, `WITH owned AS MATERIALIZED (
		SELECT r.id FROM v3_conversation_replies r JOIN v3_conversations c ON c.id=r.conversation_id
		WHERE r.id=$6 AND r.conversation_id=$5 AND r.worker_claim_id=$10
			AND r.state='running' AND r.lease_expires_at>clock_timestamp() AND c.active_reply_id=r.id
		FOR UPDATE OF r
	)
	INSERT INTO management_audit(actor_id,actor_kind,action,target,outcome,changes)
	SELECT $1,'agent','v3.conversation.tool',$2,$3,
		jsonb_build_object('environment_id',$4::text,'conversation_id',$5,'reply_id',owned.id,'step_id',$7::text,'duration_ms',$8::bigint,'resource_id',$9::text)
	FROM owned`, owner, invocation.Name, outcome, env, conv, reply, invocation.StepID, invocation.DurationMS, invocation.TargetID, claim)
	if err == nil && result.RowsAffected() == 0 {
		return ErrBusy
	}
	return err
}

// appendEvidenceReceipt records that a successful read produced evidence. The
// receipt intentionally contains only source identity and correlation IDs; the
// model-visible evidence and read query never enter the immutable audit log.
func (s *Service) appendEvidenceReceipt(ctx context.Context, owner, env, conv, reply, claim string, invocation inference.ToolInvocation, source string) error {
	if owner == "" || env == "" || env == "all" || conv == "" || reply == "" || claim == "" ||
		invocation.Status != "succeeded" || invocation.Name == "" || invocation.StepID == "" ||
		source == "" || len(source) > 256 || !utf8.ValidString(source) {
		return errEvidenceReceipt
	}
	for _, r := range source {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return errEvidenceReceipt
		}
	}
	result, err := s.db.Exec(ctx, `WITH owned AS MATERIALIZED (
		SELECT r.id FROM v3_conversation_replies r JOIN v3_conversations c ON c.id=r.conversation_id
		WHERE r.id=$5 AND r.conversation_id=$4 AND r.worker_claim_id=$6
			AND c.owner_id=$1 AND c.environment_id=$3
			AND r.state='running' AND r.lease_expires_at>clock_timestamp() AND c.active_reply_id=r.id
		FOR UPDATE OF r
	)
	INSERT INTO management_audit(actor_id,actor_kind,action,target,outcome,changes)
	SELECT $1,'agent','v3.conversation.evidence',$2,'succeeded',
		jsonb_build_object('environment_id',$3::text,'conversation_id',$4,'reply_id',owned.id,'step_id',$7::text,'source',$8::text)
	FROM owned
	WHERE EXISTS (
		SELECT 1 FROM management_audit tool
		WHERE tool.actor_id=$1 AND tool.actor_kind='agent'
			AND tool.action='v3.conversation.tool' AND tool.outcome='succeeded' AND tool.target=$2
			AND tool.changes->>'conversation_id'=$4 AND tool.changes->>'reply_id'=owned.id
			AND tool.changes->>'step_id'=$7::text AND tool.changes->>'environment_id'=$3::text
	) AND NOT EXISTS (
		SELECT 1 FROM management_audit a
		WHERE a.action='v3.conversation.evidence'
			AND a.changes->>'reply_id'=owned.id
			AND a.changes->>'step_id'=$7::text
	)`, owner, invocation.Name, env, conv, reply, claim, invocation.StepID, source)
	if err == nil && result.RowsAffected() == 0 {
		return errEvidenceReceipt
	}
	return err
}
