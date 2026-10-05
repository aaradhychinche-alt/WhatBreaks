package auth

import (
	"encoding/json"
	"net/http"

	"github.com/aaradhychinche-alt/WhatBreaks/internal/logging"
)

// SessionValidator defines the contract for validating user sessions from HTTP requests.
type SessionValidator interface {
	ValidateSession(req *http.Request) (*User, error)
}

// SessionValidatorFunc allows using a plain function as a SessionValidator.
type SessionValidatorFunc func(req *http.Request) (*User, error)

func (f SessionValidatorFunc) ValidateSession(req *http.Request) (*User, error) {
	return f(req)
}

// AuthMiddleware coordinates worker authentication, user session validation, and email verification,
// migrating infrastructure/auth/auth-middleware.js.
type AuthMiddleware struct {
	workerAuth WorkerAuthenticator
	sessionVal SessionValidator
	logger     logging.Logger
	isTestMode bool
}

// NewAuthMiddleware constructs a new AuthMiddleware.
func NewAuthMiddleware(workerAuth WorkerAuthenticator, sessionVal SessionValidator, logger logging.Logger, isTestMode bool) *AuthMiddleware {
	if logger == nil {
		logger = logging.NewJSONLogger(nil, logging.LevelInfo, "auth")
	}
	return &AuthMiddleware{
		workerAuth: workerAuth,
		sessionVal: sessionVal,
		logger:     logger,
		isTestMode: isTestMode,
	}
}

// RequireAuth validates internal worker requests or user sessions, matching
// infrastructure/auth/auth-middleware.js requireAuth.
func (m *AuthMiddleware) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		clientIP := ResolveClientIP(req)
		path := req.URL.Path
		method := req.Method

		// 1. Allow internal worker calls authenticated via Bearer token
		if m.workerAuth != nil {
			if workerUser, ok := m.workerAuth.AuthenticateRequest(req); ok {
				m.logger.Info("Internal worker call authenticated",
					"path", path,
					"method", method,
				)
				ctx := WithWorkerCall(req.Context(), true)
				ctx = WithUser(ctx, workerUser)
				next.ServeHTTP(w, req.WithContext(ctx))
				return
			}
		}

		// 2. Validate session
		var user *User
		var isAuth bool
		if m.sessionVal != nil {
			u, err := m.sessionVal.ValidateSession(req)
			if err == nil && u != nil {
				user = u
				isAuth = true
			}
		}

		userID := ""
		if user != nil {
			userID = user.ID
		}

		m.logger.Info("Authentication check",
			"method", method,
			"path", path,
			"authenticated", isAuth,
			"userId", userID,
			"ip", clientIP,
		)

		if !isAuth {
			m.logger.Warn("Unauthenticated access attempt",
				"path", path,
				"method", method,
				"ip", clientIP,
				"userAgent", req.UserAgent(),
			)
			writeJSONError(w, http.StatusUnauthorized, "Not authenticated", nil)
			return
		}

		if user == nil || user.ID == "" {
			hasUser := user != nil
			m.logger.Error("Invalid session data",
				"path", path,
				"method", method,
				"hasUser", hasUser,
				"hasUserId", false,
				"ip", clientIP,
			)
			writeJSONError(w, http.StatusUnauthorized, "Invalid session", nil)
			return
		}

		m.logger.Info("Authenticated user access",
			"userId", user.ID,
			"path", path,
			"method", method,
			"ip", clientIP,
		)

		// 3. Enforce email verification for local auth users (except in test mode)
		if user.AuthMethod == "local" && !user.EmailVerified {
			if !m.isTestMode {
				m.logger.Warn("SECURITY_BREACH_UNVERIFIED_ACCESS",
					"userId", user.ID,
					"email", user.Email,
					"path", path,
					"ip", clientIP,
				)
				writeJSONError(w, http.StatusForbidden, "Email verification required", map[string]any{
					"needsVerification": true,
				})
				return
			}

			m.logger.Info("Test mode: Allowing unverified user access (requireAuth)",
				"userId", user.ID,
				"email", user.Email,
				"path", path,
				"ip", clientIP,
			)
		}

		ctx := WithUser(req.Context(), user)
		next.ServeHTTP(w, req.WithContext(ctx))
	})
}

// EnforceEmailVerification validates that authenticated local users have verified their email,
// matching infrastructure/auth/auth-middleware.js enforceEmailVerification.
func (m *AuthMiddleware) EnforceEmailVerification(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		path := req.URL.Path
		clientIP := ResolveClientIP(req)

		// Exemptions
		if path == "/api/session" && m.isTestMode {
			next.ServeHTTP(w, req)
			return
		}
		if path == "/api/logout" || path == "/logout" {
			next.ServeHTTP(w, req)
			return
		}

		user, ok := UserFromContext(req.Context())
		if !ok && m.sessionVal != nil {
			u, err := m.sessionVal.ValidateSession(req)
			if err == nil && u != nil {
				user = u
				ok = true
			}
		}

		if ok && user != nil && user.ID != "" {
			if user.AuthMethod == "local" && !user.EmailVerified {
				if !m.isTestMode {
					m.logger.Warn("UNVERIFIED_ACCESS_ATTEMPT",
						"userId", user.ID,
						"email", user.Email,
						"path", path,
						"ip", clientIP,
					)
					writeJSONError(w, http.StatusForbidden, "Email verification required", map[string]any{
						"needsVerification": true,
					})
					return
				}

				m.logger.Info("Test mode: Allowing unverified user access",
					"userId", user.ID,
					"email", user.Email,
					"path", path,
					"ip", clientIP,
				)
			}
		}

		next.ServeHTTP(w, req)
	})
}

func writeJSONError(w http.ResponseWriter, statusCode int, message string, extra map[string]any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(statusCode)

	body := make(map[string]any)
	body["error"] = message
	for k, v := range extra {
		body[k] = v
	}

	_ = json.NewEncoder(w).Encode(body)
}
