package answer

import (
	"errors"
	"fmt"
)

var (
	// ErrCoreUnavailable is returned when the Rust core engine is unreachable or not connected.
	ErrCoreUnavailable = errors.New("core engine is currently unavailable")

	// ErrCoreTimeout is returned when the Rust core engine call exceeds the deadline.
	ErrCoreTimeout = errors.New("core engine request timed out")

	// ErrRequestCanceled is returned when the caller cancels the request context.
	ErrRequestCanceled = errors.New("request was cancelled")

	// ErrMalformedCoreResponse is returned when the core engine returns an incomplete or corrupt response.
	ErrMalformedCoreResponse = errors.New("core engine returned a malformed response")
)

// ValidationError indicates a client-provided parameter is invalid.
type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string {
	if e.Field != "" {
		return fmt.Sprintf("validation failed on %s: %s", e.Field, e.Message)
	}
	return fmt.Sprintf("validation failed: %s", e.Message)
}

// ErrorResponse defines the standard JSON error structure matching the platform API conventions.
type ErrorResponse struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}

// Standard ErrorCode values matching platform conventions.
const (
	CodeBadRequest      = "BAD_REQUEST"
	CodeValidationError = "VALIDATION_ERROR"
	CodeUnauthorized    = "UNAUTHORIZED"
	CodeForbidden       = "FORBIDDEN"
	CodeNotFound        = "NOT_FOUND"
	CodeCoreUnavailable = "CORE_UNAVAILABLE"
	CodeCoreTimeout     = "CORE_TIMEOUT"
	CodeRequestCanceled = "REQUEST_CANCELED"
	CodeInternalError   = "INTERNAL_ERROR"
)
