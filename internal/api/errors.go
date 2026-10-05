package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"runtime/debug"

	"github.com/aaradhychinche-alt/WhatBreaks/internal/auth"
	"github.com/aaradhychinche-alt/WhatBreaks/internal/logging"
)

// ErrorResponse defines the standard JSON error structure matching Express errorHandler.
type ErrorResponse struct {
	Error string `json:"error"`
	Code  string `json:"code"`
	Stack string `json:"stack,omitempty"`
}

func writeErrorResponse(w http.ResponseWriter, status int, msg, code string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(ErrorResponse{
		Error: msg,
		Code:  code,
	})
}

// NotFoundHandler returns an http.Handler that emits a 404 JSON response matching
// the legacy API 404 handler (infrastructure/api/index.js lines 109-114).
func NotFoundHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeErrorResponse(w, http.StatusNotFound, "Endpoint not found", "NOT_FOUND")
	})
}

// RecoveryMiddleware catches panics, logs the error through the structured logger,
// and returns a safe JSON 500 response without leaking internal credentials or stack traces in production.
func RecoveryMiddleware(logger logging.Logger, isDevelopment bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					stack := string(debug.Stack())
					errMsg := fmt.Sprintf("%v", rec)

					if logger != nil {
						logger.Error("Unhandled error:",
							"error", errMsg,
							"stack", stack,
							"url", r.URL.String(),
							"method", r.Method,
							"ip", auth.ResolveClientIP(r),
						)
					}

					resp := ErrorResponse{
						Error: "Internal server error",
						Code:  "INTERNAL_ERROR",
					}
					if isDevelopment {
						resp.Error = errMsg
						resp.Stack = stack
					}

					w.Header().Set("Content-Type", "application/json; charset=utf-8")
					w.WriteHeader(http.StatusInternalServerError)
					_ = json.NewEncoder(w).Encode(resp)
				}
			}()

			next.ServeHTTP(w, r)
		})
	}
}
