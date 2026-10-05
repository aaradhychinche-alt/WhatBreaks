package auth

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aaradhychinche-alt/WhatBreaks/internal/logging"
)

func TestAuthMiddleware_WorkerCallAuthentication(t *testing.T) {
	workerAuth := NewWorkerAuthenticator("super-secret-worker-token")
	var logBuf bytes.Buffer
	logger := logging.NewJSONLogger(&logBuf, logging.LevelDebug, "auth-test")

	mw := NewAuthMiddleware(workerAuth, nil, logger, false)

	handlerCalled := false
	var capturedUser *User
	var isWorker bool

	protectedHandler := mw.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true
		capturedUser, _ = UserFromContext(r.Context())
		isWorker = IsWorkerCallFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	// 1. Valid worker request
	req := httptest.NewRequest(http.MethodPost, "/api/workers/discover", nil)
	req.Header.Set("Authorization", "Bearer super-secret-worker-token")
	rr := httptest.NewRecorder()

	protectedHandler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if !handlerCalled {
		t.Fatalf("expected inner handler to be called")
	}
	if !isWorker {
		t.Errorf("expected isWorkerCall to be true in context")
	}
	if capturedUser == nil || capturedUser.Role != "admin" || capturedUser.Email != "worker@internal" {
		t.Errorf("unexpected worker user in context: %+v", capturedUser)
	}
	if !bytes.Contains(logBuf.Bytes(), []byte("Internal worker call authenticated")) {
		t.Errorf("expected log to contain worker authenticated message")
	}
}

func TestAuthMiddleware_SessionValidation(t *testing.T) {
	workerAuth := NewWorkerAuthenticator("worker-token")
	var logBuf bytes.Buffer
	logger := logging.NewJSONLogger(&logBuf, logging.LevelDebug, "auth-test")

	// Mock session validator
	var currentSessionUser *User
	sessionVal := SessionValidatorFunc(func(req *http.Request) (*User, error) {
		return currentSessionUser, nil
	})

	mw := NewAuthMiddleware(workerAuth, sessionVal, logger, false)

	protectedHandler := mw.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))

	// 1. Unauthenticated request (no worker token, no session)
	reqUnauth := httptest.NewRequest(http.MethodGet, "/api/workspaces", nil)
	rr1 := httptest.NewRecorder()
	protectedHandler.ServeHTTP(rr1, reqUnauth)

	if rr1.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rr1.Code)
	}
	var errBody map[string]string
	_ = json.Unmarshal(rr1.Body.Bytes(), &errBody)
	if errBody["error"] != "Not authenticated" {
		t.Errorf("expected error 'Not authenticated', got %q", errBody["error"])
	}

	// 2. Invalid session (session exists but has no user ID)
	currentSessionUser = &User{ID: "", Email: "no-id@test.com"}
	reqInvalidSession := httptest.NewRequest(http.MethodGet, "/api/workspaces", nil)
	rr2 := httptest.NewRecorder()
	protectedHandler.ServeHTTP(rr2, reqInvalidSession)

	if rr2.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for invalid session, got %d", rr2.Code)
	}
	_ = json.Unmarshal(rr2.Body.Bytes(), &errBody)
	if errBody["error"] != "Invalid session" {
		t.Errorf("expected error 'Invalid session', got %q", errBody["error"])
	}

	// 3. Valid authenticated session
	currentSessionUser = &User{ID: "usr-42", Email: "test@example.com", AuthMethod: "sso", EmailVerified: true}
	reqValid := httptest.NewRequest(http.MethodGet, "/api/workspaces", nil)
	rr3 := httptest.NewRecorder()
	protectedHandler.ServeHTTP(rr3, reqValid)

	if rr3.Code != http.StatusOK {
		t.Errorf("expected 200 for valid session, got %d", rr3.Code)
	}
}

func TestAuthMiddleware_EmailVerificationEnforcement(t *testing.T) {
	var currentSessionUser *User
	sessionVal := SessionValidatorFunc(func(req *http.Request) (*User, error) {
		return currentSessionUser, nil
	})

	var logBuf bytes.Buffer
	logger := logging.NewJSONLogger(&logBuf, logging.LevelDebug, "auth-test")

	// 1. Production mode: local auth user without email verification -> 403 Forbidden
	mwProd := NewAuthMiddleware(nil, sessionVal, logger, false)
	currentSessionUser = &User{
		ID:            "usr-1",
		Email:         "unverified@test.com",
		AuthMethod:    "local",
		EmailVerified: false,
	}

	req := httptest.NewRequest(http.MethodGet, "/api/data", nil)
	rr := httptest.NewRecorder()

	mwProd.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for unverified local user in production, got %d", rr.Code)
	}

	var res map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &res)
	if res["error"] != "Email verification required" || res["needsVerification"] != true {
		t.Errorf("unexpected error payload: %+v", res)
	}
	if !bytes.Contains(logBuf.Bytes(), []byte("SECURITY_BREACH_UNVERIFIED_ACCESS")) {
		t.Errorf("expected security breach log")
	}

	// 2. Test mode: local auth user allowed through
	mwTest := NewAuthMiddleware(nil, sessionVal, logger, true)
	rrTest := httptest.NewRecorder()
	mwTest.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rrTest, req)

	if rrTest.Code != http.StatusOK {
		t.Errorf("expected 200 in test mode, got %d", rrTest.Code)
	}

	// 3. SSO user without email verification is not blocked by local email verification rule
	currentSessionUser = &User{
		ID:            "usr-2",
		Email:         "sso@test.com",
		AuthMethod:    "sso",
		EmailVerified: false,
	}
	rrSSO := httptest.NewRecorder()
	mwProd.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rrSSO, req)

	if rrSSO.Code != http.StatusOK {
		t.Errorf("expected SSO user to pass, got %d", rrSSO.Code)
	}
}

func TestEnforceEmailVerification_Exemptions(t *testing.T) {
	sessionVal := SessionValidatorFunc(func(req *http.Request) (*User, error) {
		return &User{
			ID:            "usr-unverified",
			AuthMethod:    "local",
			EmailVerified: false,
		}, nil
	})

	logger := logging.NewJSONLogger(nil, logging.LevelDebug, "auth-test")
	mw := NewAuthMiddleware(nil, sessionVal, logger, true)

	dummyHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	enforced := mw.EnforceEmailVerification(dummyHandler)

	// 1. /api/session in test mode is exempt
	reqSession := httptest.NewRequest(http.MethodGet, "/api/session", nil)
	rr1 := httptest.NewRecorder()
	enforced.ServeHTTP(rr1, reqSession)
	if rr1.Code != http.StatusOK {
		t.Errorf("expected 200 for exempt /api/session, got %d", rr1.Code)
	}

	// 2. /logout and /api/logout are exempt
	reqLogout := httptest.NewRequest(http.MethodPost, "/api/logout", nil)
	rr2 := httptest.NewRecorder()
	enforced.ServeHTTP(rr2, reqLogout)
	if rr2.Code != http.StatusOK {
		t.Errorf("expected 200 for exempt /api/logout, got %d", rr2.Code)
	}
}
