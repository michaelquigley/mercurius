package broker

import (
	"errors"
	"fmt"
)

const (
	CodeInvalidArtifacts      = "invalid_artifacts"
	CodeInvalidBudget         = "invalid_budget"
	CodeInvalidLogDestination = "invalid_log_destination"
	CodePanelModeUnsupported  = "panel_mode_unsupported"
	CodeUnknownReviewer       = "unknown_reviewer"
	CodeUnknownSession        = "unknown_session"
	CodeSessionClosed         = "session_closed"
	CodeBudgetExhausted       = "budget_exhausted"
	CodeRoundInProgress       = "round_in_progress"
	CodeReviewerFailed        = "reviewer_failed"
	CodeSchemaViolation       = "schema_violation"
	CodeUnknownRound          = "unknown_round"
	CodeEmptyNotes            = "empty_notes"
	CodeUnknownRef            = "unknown_ref"
	CodeInvalidDecision       = "invalid_decision"
	CodeAlreadyClosed         = "already_closed"
	CodeInvalidVerdict        = "invalid_verdict"
	CodeInternalError         = "internal_error"
)

// Error is a structured broker error ready for MCP wrapping.
type Error struct {
	Code    string
	Message string
	Details map[string]any
	Err     error
}

func (e *Error) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.Err)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e *Error) Unwrap() error {
	return e.Err
}

func brokerError(code string, message string, err error, details map[string]any) *Error {
	return &Error{
		Code:    code,
		Message: message,
		Err:     err,
		Details: details,
	}
}

// ErrorInfoFrom returns a session/tool visible error summary.
func ErrorInfoFrom(err error) *ErrorInfo {
	if err == nil {
		return nil
	}
	var e *Error
	if errors.As(err, &e) {
		details := cloneDetails(e.Details)
		if e.Err != nil {
			details["cause"] = e.Err.Error()
		}
		return &ErrorInfo{
			Code:       e.Code,
			Message:    e.Message,
			Details:    details,
			Retryable:  Retryable(e.Code),
			NextAction: NextAction(e.Code),
		}
	}
	return &ErrorInfo{
		Code:       CodeInternalError,
		Message:    "internal error",
		Details:    map[string]any{"cause": err.Error()},
		Retryable:  Retryable(CodeInternalError),
		NextAction: NextAction(CodeInternalError),
	}
}

// Retryable reports whether retrying the same operation may reasonably succeed.
func Retryable(code string) bool {
	return code == CodeReviewerFailed || code == CodeSchemaViolation || code == CodeRoundInProgress
}

// NextAction returns agent-facing guidance for an error code.
func NextAction(code string) string {
	switch code {
	case CodeInvalidArtifacts:
		return "fix artifact names and absolute readable paths, then retry the request"
	case CodeInvalidBudget:
		return "retry with a budget greater than zero"
	case CodeInvalidLogDestination:
		return "fix the configured log_destination and restart the Mercurius server"
	case CodePanelModeUnsupported:
		return "call list_reviewers and open a new session with exactly one reviewer"
	case CodeUnknownReviewer:
		return "call list_reviewers and retry with one configured reviewer name"
	case CodeUnknownSession:
		return "call list_sessions to find an active session or open a new session"
	case CodeSessionClosed:
		return "open a new session before requesting another review round"
	case CodeBudgetExhausted:
		return "close the session or open a new session with a larger budget"
	case CodeRoundInProgress:
		return "monitor the active round and collect it after it completes"
	case CodeReviewerFailed:
		return "inspect details.cause; retry if it looks transient, otherwise escalate to the user or server operator"
	case CodeSchemaViolation:
		return "retry the round once; if it repeats, escalate with the schema violation details"
	case CodeUnknownRound:
		return "call session_status and record notes for an existing round number"
	case CodeEmptyNotes:
		return "provide commentary or at least one decision before recording round notes"
	case CodeUnknownRef:
		return "use a concern or question id from the round review output"
	case CodeInvalidDecision:
		return "use disposition accepted, rejected, or deferred"
	case CodeAlreadyClosed:
		return "no further cleanup is needed for this session"
	case CodeInvalidVerdict:
		return "close the session with verdict ready_to_build, paused, or abandoned"
	default:
		return "inspect details and escalate if the issue is not clear"
	}
}

// ErrorCode returns a broker error code, or an empty string for other errors.
func ErrorCode(err error) string {
	if err == nil {
		return ""
	}
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

func cloneDetails(details map[string]any) map[string]any {
	out := map[string]any{}
	for key, value := range details {
		out[key] = value
	}
	return out
}
