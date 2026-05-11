package broker

import (
	"errors"
	"fmt"
)

// Stable error codes. Five classes cover the operational surface: caller
// inputs, addressing, state conflicts, reviewer failures, and broker bugs.
const (
	CodeUserError      = "user_error"
	CodeNotFound       = "not_found"
	CodeConflict       = "conflict"
	CodeReviewerFailed = "reviewer_failed"
	CodeInternalError  = "internal_error"
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
			Code:    e.Code,
			Message: e.Message,
			Details: details,
		}
	}
	return &ErrorInfo{
		Code:    CodeInternalError,
		Message: "internal error",
		Details: map[string]any{"cause": err.Error()},
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
