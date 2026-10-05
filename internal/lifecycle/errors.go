package lifecycle

import (
	"fmt"
)

// CodedError represents an error with an associated machine-readable error code.
type CodedError struct {
	code    string
	message string
	cause   error
}

// NewCodedError creates a new CodedError.
func NewCodedError(code, message string) *CodedError {
	return &CodedError{code: code, message: message}
}

// WrapCodedError wraps an underlying cause with a code and message.
func WrapCodedError(code, message string, cause error) *CodedError {
	return &CodedError{code: code, message: message, cause: cause}
}

func (e *CodedError) Error() string {
	if e.cause != nil {
		return fmt.Sprintf("%s: %v", e.message, e.cause)
	}
	return e.message
}

func (e *CodedError) Code() string {
	return e.code
}

func (e *CodedError) Unwrap() error {
	return e.cause
}

// Standard errors matching controller lifecycle and runtime error codes from JS.
var (
	ErrControllerStopping       = NewCodedError("CONTROLLER_STOPPING", "Controller is stopping")
	ErrControllerStartupFailed  = NewCodedError("CONTROLLER_STARTUP_FAILED", "controller startup failed")
	ErrControllerShutdownFailed = NewCodedError("CONTROLLER_SHUTDOWN_FAILED", "controller shutdown failed")
	ErrInvalidConfig            = NewCodedError("INVALID_CONFIG", "invalid lifecycle configuration")
)

// GetErrorCode extracts the error code from an error or returns the fallback code.
func GetErrorCode(err error, fallback string) string {
	if err == nil {
		return fallback
	}
	type coder interface {
		Code() string
	}
	if c, ok := err.(coder); ok && c.Code() != "" {
		return c.Code()
	}
	return fallback
}
