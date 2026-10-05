package api

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"net/http"

	"github.com/aaradhychinche-alt/WhatBreaks/internal/auth"
)

// CSRFManager coordinates double-submit CSRF cookie token generation and validation,
// faithfully implementing infrastructure/api/middleware/csrf.js.
type CSRFManager struct {
	secret       []byte
	cookieName   string
	cookieConfig auth.CookieConfig
	isTestMode   bool
}

// NewCSRFManager creates a CSRFManager using the session secret and resolved cookie options.
func NewCSRFManager(sessionSecret string, cookieName string, cookieConfig auth.CookieConfig, isTestMode bool) *CSRFManager {
	if cookieName == "" {
		cookieName = "x-csrf-token"
	}

	return &CSRFManager{
		secret:       []byte(sessionSecret),
		cookieName:   cookieName,
		cookieConfig: cookieConfig,
		isTestMode:   isTestMode,
	}
}

// GenerateToken generates a cryptographically random cookie nonce,
// computes the HMAC-SHA256 token, and returns both the cookie nonce and the header token.
func (m *CSRFManager) GenerateToken() (cookieNonce string, headerToken string, err error) {
	rawNonce := make([]byte, 32)
	if _, err := rand.Read(rawNonce); err != nil {
		return "", "", err
	}
	cookieNonce = hex.EncodeToString(rawNonce)
	headerToken = m.signNonce(cookieNonce)
	return cookieNonce, headerToken, nil
}

func (m *CSRFManager) signNonce(nonce string) string {
	mac := hmac.New(sha256.New, m.secret)
	mac.Write([]byte(nonce))
	return hex.EncodeToString(mac.Sum(nil))
}

// ValidateToken verifies that the token provided in the header matches the HMAC
// of the nonce provided in the cookie using timing-safe comparison.
func (m *CSRFManager) ValidateToken(cookieNonce, headerToken string) bool {
	if cookieNonce == "" || headerToken == "" || len(m.secret) == 0 {
		return false
	}
	expected := m.signNonce(cookieNonce)
	return subtle.ConstantTimeCompare([]byte(headerToken), []byte(expected)) == 1
}

// TokenHandler returns an http.HandlerFunc for GET /api/csrf-token.
// It sets the CSRF cookie and responds with {"csrfToken": "<token>"}.
func (m *CSRFManager) TokenHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookieNonce, token, err := m.GenerateToken()
		if err != nil {
			writeErrorResponse(w, http.StatusInternalServerError, "Failed to generate CSRF token", "INTERNAL_ERROR")
			return
		}

		cookie := auth.ToHTTPCookie(m.cookieName, cookieNonce, m.cookieConfig)
		http.SetCookie(w, cookie)

		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"csrfToken": token,
		})
	}
}

// Middleware returns an http.Handler middleware that validates CSRF protection
// on state-changing HTTP methods (POST, PUT, PATCH, DELETE).
func (m *CSRFManager) Middleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// 1. Bypass in test mode
			if m.isTestMode {
				next.ServeHTTP(w, r)
				return
			}

			// 2. Safe HTTP methods require no CSRF check
			switch r.Method {
			case http.MethodGet, http.MethodHead, http.MethodOptions, "TRACE":
				next.ServeHTTP(w, r)
				return
			}

			// 3. Exempt endpoints
			path := r.URL.Path
			if path == "/api/csrf-token" || path == "/health" || path == "/healthz" || path == "/readyz" {
				next.ServeHTTP(w, r)
				return
			}

			// 4. Internal worker calls bypass CSRF
			if auth.IsWorkerCallFromContext(r.Context()) {
				next.ServeHTTP(w, r)
				return
			}
			if user, ok := auth.UserFromContext(r.Context()); ok && user.Role == auth.WorkerRole && user.AuthMethod == "internal" {
				next.ServeHTTP(w, r)
				return
			}

			// 5. Read cookie nonce
			cookie, err := r.Cookie(m.cookieName)
			if err != nil || cookie.Value == "" {
				writeErrorResponse(w, http.StatusForbidden, "Invalid CSRF token", "CSRF_ERROR")
				return
			}

			// 6. Read token from header
			headerToken := r.Header.Get("X-CSRF-Token")
			if headerToken == "" {
				// Also check case-insensitive x-csrf-token
				headerToken = r.Header.Get("x-csrf-token")
			}
			if headerToken == "" {
				writeErrorResponse(w, http.StatusForbidden, "Invalid CSRF token", "CSRF_ERROR")
				return
			}

			// 7. Validate token with timing-safe HMAC verification
			if !m.ValidateToken(cookie.Value, headerToken) {
				writeErrorResponse(w, http.StatusForbidden, "Invalid CSRF token", "CSRF_ERROR")
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
