package inference

import "errors"

// ToolError is an evidence-tool failure the model can act on (PA-50). The
// runtime hands err.Error() to the model, so the message says what went wrong
// and what to try next. Codes are stable:
//
//   - invalid_input: the input does not have the documented shape;
//   - unknown_service: no service with this key is readable here. The same
//     message covers a missing and an unauthorized service, so it never
//     reveals whether a hidden service exists;
//   - not_permitted: the service is readable, but not this kind of data;
//   - not_configured: the source or query is not set up for this service;
//   - source_unavailable: a configured source failed; retrying may help.
type ToolError struct {
	Code    string
	Message string
}

const (
	ToolInvalidInput      = "invalid_input"
	ToolUnknownService    = "unknown_service"
	ToolNotPermitted      = "not_permitted"
	ToolNotConfigured     = "not_configured"
	ToolSourceUnavailable = "source_unavailable"
	// ToolEvidenceLimit answers a call after the run's tool budget is used.
	ToolEvidenceLimit = "evidence_limit"
)

func (e *ToolError) Error() string { return e.Code + ": " + e.Message }

// NewToolError returns a ToolError with code and message.
func NewToolError(code, message string) error { return &ToolError{Code: code, Message: message} }

// ToolErrorCode returns the code of a ToolError in err's chain, or "".
func ToolErrorCode(err error) string {
	var toolErr *ToolError
	if errors.As(err, &toolErr) {
		return toolErr.Code
	}
	return ""
}

// ErrUnknownService is the one answer for a service key that is absent or
// not visible to the caller in this environment.
var ErrUnknownService = NewToolError(ToolUnknownService,
	"no service with this key is readable in this environment; call list_services for the exact keys you can read")
