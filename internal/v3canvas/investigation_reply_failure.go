package v3canvas

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
)

// errNodeReplyInvalid marks a model answer that is empty or over the stored
// reply limit.
var errNodeReplyInvalid = errors.New("empty or oversized model reply")

// investigationReplyFailure names why an investigation node reply failed
// (PA-267). Before, every failure was saved as model_unavailable, so a read the
// collector refused looked like the model being down.
//   - reply_interrupted: the reply's context was canceled (access lost during
//     the run, or the request went away);
//   - reply_timeout: the run passed its time budget;
//   - evidence_unavailable: the evidence collector stopped the run (a refused
//     or uncoded read failure, a session or audit failure, an invalid event);
//   - model_reply_invalid: the model's answer was empty or too large;
//   - model_unavailable: anything else, the model call itself.
func investigationReplyFailure(runErr, collectorErr error) string {
	switch {
	case errors.Is(runErr, context.Canceled) || errors.Is(collectorErr, context.Canceled):
		return "reply_interrupted"
	case errors.Is(runErr, context.DeadlineExceeded) || errors.Is(collectorErr, context.DeadlineExceeded):
		return "reply_timeout"
	case collectorErr != nil || errors.Is(runErr, errInvestigationEvidence):
		return "evidence_unavailable"
	case errors.Is(runErr, errNodeReplyInvalid):
		return "model_reply_invalid"
	}
	return "model_unavailable"
}

// investigationReplyFailureText says why in the request language; the codes
// match the reply list's wording (NodeReplies.tsx).
func investigationReplyFailureText(code string, english bool) string {
	text := map[string][2]string{
		"reply_interrupted":    {"回复被中断了（会话或权限有变化，或连接已断开），没有完成。", "The reply was interrupted (a session or access change, or the connection closed) and didn't finish."},
		"reply_timeout":        {"回复超过了时间上限，没有完成。可以把问题拆小一些再问。", "The reply ran past its time limit and didn't finish. Try a narrower question."},
		"evidence_unavailable": {"取证失败，没能读到证据，这条回复没有完成。", "Evidence couldn't be read, so the reply didn't finish."},
		"model_reply_invalid":  {"模型没有给出可用的回答，这条回复没有保存。", "The model gave no usable answer, so nothing was saved."},
		"model_unavailable":    {"模型回复失败，请稍后重试。", "The model didn't reply. Try again later."},
	}[code]
	if text[0] == "" {
		text = [2]string{"这条回复没有完成。", "The reply didn't finish."}
	}
	if english {
		return text[1]
	}
	return text[0]
}

// logInvestigationReplyFailure records the cause of a failed reply. Model
// errors log only their type: provider failures are logged where the call is
// made, and their text may carry upstream content.
func logInvestigationReplyFailure(id, code string, runErr, collectorErr error) {
	cause := fmt.Sprintf("%T", runErr)
	if code != "model_unavailable" {
		cause = runErr.Error()
		if collectorErr != nil {
			cause = collectorErr.Error()
		}
	}
	slog.Warn("investigation node reply failed", "reply_id", id, "failure_code", code, "cause", cause)
}
