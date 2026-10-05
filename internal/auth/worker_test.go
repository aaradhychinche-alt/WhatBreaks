package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aaradhychinche-alt/WhatBreaks/internal/config"
)

func TestTokenWorkerAuthenticator_AuthenticateBearer(t *testing.T) {
	auth := NewWorkerAuthenticator("super-secret-worker-key-12345")

	tests := []struct {
		name     string
		token    string
		expected bool
	}{
		{
			name:     "valid matching token",
			token:    "super-secret-worker-key-12345",
			expected: true,
		},
		{
			name:     "wrong token",
			token:    "wrong-secret-worker-key-12345",
			expected: false,
		},
		{
			name:     "prefix of token",
			token:    "super-secret",
			expected: false,
		},
		{
			name:     "empty token",
			token:    "",
			expected: false,
		},
		{
			name:     "extra whitespace",
			token:    " super-secret-worker-key-12345 ",
			expected: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := auth.AuthenticateBearer(tc.token)
			if got != tc.expected {
				t.Fatalf("AuthenticateBearer(%q) = %v; expected %v", tc.token, got, tc.expected)
			}
		})
	}
}

func TestTokenWorkerAuthenticator_ExtractBearerToken(t *testing.T) {
	tests := []struct {
		name     string
		header   string
		expected string
	}{
		{
			name:     "valid bearer header",
			header:   "Bearer my-worker-token",
			expected: "my-worker-token",
		},
		{
			name:     "missing bearer prefix",
			header:   "Basic dXNlcjpwYXNz",
			expected: "",
		},
		{
			name:     "lowercase bearer",
			header:   "bearer my-worker-token",
			expected: "",
		},
		{
			name:     "bearer prefix only",
			header:   "Bearer ",
			expected: "",
		},
		{
			name:     "empty header",
			header:   "",
			expected: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ExtractBearerToken(tc.header)
			if got != tc.expected {
				t.Fatalf("ExtractBearerToken(%q) = %q; expected %q", tc.header, got, tc.expected)
			}
		})
	}
}

func TestTokenWorkerAuthenticator_AuthenticateRequest(t *testing.T) {
	auth := NewWorkerAuthenticator("worker-secret-42")

	// 1. Valid request
	req := httptest.NewRequest(http.MethodGet, "/internal/worker/task", nil)
	req.Header.Set("Authorization", "Bearer worker-secret-42")

	user, ok := auth.AuthenticateRequest(req)
	if !ok || user == nil {
		t.Fatalf("expected successful worker authentication")
	}
	if user.Role != "admin" {
		t.Errorf("expected worker role admin, got %q", user.Role)
	}
	if user.Email != "worker@internal" {
		t.Errorf("expected worker email worker@internal, got %q", user.Email)
	}
	if user.AuthMethod != "internal" {
		t.Errorf("expected auth method internal, got %q", user.AuthMethod)
	}
	if !user.EmailVerified {
		t.Errorf("expected worker email to be marked verified")
	}

	// 2. Missing authorization header
	reqNoAuth := httptest.NewRequest(http.MethodGet, "/internal/worker/task", nil)
	_, ok = auth.AuthenticateRequest(reqNoAuth)
	if ok {
		t.Errorf("expected unauthenticated request to fail")
	}

	// 3. Wrong bearer token
	reqWrongAuth := httptest.NewRequest(http.MethodGet, "/internal/worker/task", nil)
	reqWrongAuth.Header.Set("Authorization", "Bearer wrong-token")
	_, ok = auth.AuthenticateRequest(reqWrongAuth)
	if ok {
		t.Errorf("expected wrong token to fail")
	}

	// 4. Non-bearer format
	reqBasicAuth := httptest.NewRequest(http.MethodGet, "/internal/worker/task", nil)
	reqBasicAuth.Header.Set("Authorization", "Basic worker-secret-42")
	_, ok = auth.AuthenticateRequest(reqBasicAuth)
	if ok {
		t.Errorf("expected basic auth to fail")
	}
}

func TestNewWorkerAuthenticatorFromEnv(t *testing.T) {
	// 1. WORKER_API_KEY takes precedence
	envWithBoth := config.MapEnv(map[string]string{
		"WORKER_API_KEY": "primary-key",
		"SESSION_SECRET": "fallback-key",
	})
	auth1 := NewWorkerAuthenticatorFromEnv(envWithBoth)
	if !auth1.AuthenticateBearer("primary-key") {
		t.Errorf("expected primary key to authenticate")
	}
	if auth1.AuthenticateBearer("fallback-key") {
		t.Errorf("expected fallback key NOT to authenticate when primary is present")
	}

	// 2. SESSION_SECRET used when WORKER_API_KEY is empty
	envFallback := config.MapEnv(map[string]string{
		"SESSION_SECRET": "fallback-key",
	})
	auth2 := NewWorkerAuthenticatorFromEnv(envFallback)
	if !auth2.AuthenticateBearer("fallback-key") {
		t.Errorf("expected fallback key to authenticate when WORKER_API_KEY is unset")
	}

	// 3. Both unset
	envEmpty := config.MapEnv(map[string]string{})
	auth3 := NewWorkerAuthenticatorFromEnv(envEmpty)
	if auth3.AuthenticateBearer("anything") {
		t.Errorf("expected authentication to fail when no secret is configured")
	}
}
