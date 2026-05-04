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
