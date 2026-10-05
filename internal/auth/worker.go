package auth

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/aaradhychinche-alt/WhatBreaks/internal/config"
)

// ConstantTimeCompare performs a timing-attack-resistant comparison of two secrets,
// faithfully implementing infrastructure/auth/internal-worker-auth.js safeEqual.
func ConstantTimeCompare(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// ExtractBearerToken extracts the token from an Authorization header if it has the "Bearer " prefix.
func ExtractBearerToken(authHeader string) string {
	const prefix = "Bearer "
	if !strings.HasPrefix(authHeader, prefix) {
		return ""
	}
	return authHeader[len(prefix):]
}

// WorkerAuthenticator defines the contract for authenticating internal background workers.
type WorkerAuthenticator interface {
	AuthenticateBearer(token string) bool
	AuthenticateRequest(req *http.Request) (*User, bool)
}

// TokenWorkerAuthenticator validates internal worker calls via constant-time bearer token matching,
// migrating infrastructure/auth/internal-worker-auth.js.
type TokenWorkerAuthenticator struct {
	expectedKey string
}

// NewWorkerAuthenticator constructs a TokenWorkerAuthenticator with an explicit key.
func NewWorkerAuthenticator(expectedKey string) *TokenWorkerAuthenticator {
	return &TokenWorkerAuthenticator{
		expectedKey: expectedKey,
	}
}

// NewWorkerAuthenticatorFromEnv constructs an authenticator reading WORKER_API_KEY with fallback to SESSION_SECRET.
func NewWorkerAuthenticatorFromEnv(env config.EnvLookup) *TokenWorkerAuthenticator {
	if env == nil {
		env = config.OsEnv()
	}
	key, _ := config.LookupWithFallback(env, "WORKER_API_KEY", "SESSION_SECRET")
	return NewWorkerAuthenticator(strings.TrimSpace(key))
}

// AuthenticateBearer compares the provided bearer token with the expected key in constant time.
func (a *TokenWorkerAuthenticator) AuthenticateBearer(token string) bool {
	if a == nil || a.expectedKey == "" || token == "" {
		return false
	}
	return ConstantTimeCompare(token, a.expectedKey)
}

// AuthenticateRequest inspects the Authorization header, validates the bearer token,
// and returns the synthetic internal worker identity on success.
func (a *TokenWorkerAuthenticator) AuthenticateRequest(req *http.Request) (*User, bool) {
	if req == nil {
		return nil, false
	}

	authHeader := req.Header.Get("Authorization")
	token := ExtractBearerToken(authHeader)
	if token == "" {
		return nil, false
	}

	if !a.AuthenticateBearer(token) {
		return nil, false
	}

	return &User{
		ID:            "", // null in JS (matches internal-worker-auth.js line 30)
		Role:          WorkerRole,
		Email:         WorkerEmail,
		AuthMethod:    "internal",
		EmailVerified: true,
	}, true
}
