package v3conversation

import (
	"context"
	"errors"
	"regexp"

	"github.com/wans/aiops-canvas/internal/inference"
)

var errToolInvocation = errors.New("invalid tool invocation event")
var auditTargetID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,256}$`)

// appendToolAudit is the durable boundary around an inference tool read. The
// runtime awaits the started callback before calling Read, and awaits the
// terminal callback before returning evidence to the model. Never put tool
// arguments, query text, evidence, or provider errors into management audit.
func (s *Service) appendToolAudit(ctx context.Context, owner, env, conv, reply string, invocation *inference.ToolInvocation) error {
	if invocation == nil || owner == "" || env == "" || env == "all" || conv == "" || reply == "" ||
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
	_, err := s.db.Exec(ctx, `INSERT INTO management_audit(actor_id,actor_kind,action,target,outcome,changes)
		VALUES($1,'agent','v3.conversation.tool',$2,$3,
		jsonb_build_object('environment_id',$4,'conversation_id',$5,'reply_id',$6,'step_id',$7,'duration_ms',$8,'resource_id',$9))`,
		owner, invocation.Name, outcome, env, conv, reply, invocation.StepID, invocation.DurationMS, invocation.TargetID)
	return err
}
