package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aaradhychinche-alt/WhatBreaks/internal/auth"
)

func TestCSRFManager_TokenGenerationAndValidation(t *testing.T) {
	secret := "super-secure-production-session-secret"
	cookieCfg := auth.CookieConfig{
		Path:     "/",
		HTTPOnly: true,
		Secure:   false,
		SameSite: http.SameSiteLaxMode,
	}
	manager := NewCSRFManager(secret, "x-csrf-token", cookieCfg, false)

	// 1. GET /api/csrf-token
	tokenHandler := manager.TokenHandler()
	req := httptest.NewRequest(http.MethodGet, "/api/csrf-token", nil)
	rec := httptest.NewRecorder()

	tokenHandler.ServeHTTP(rec, req)

	res := rec.Result()
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", res.StatusCode)
	}

	// Verify cookie set
	cookies := res.Cookies()
	var csrfCookie *http.Cookie
	for _, c := range cookies {
		if c.Name == "x-csrf-token" {
			csrfCookie = c
			break
		}
	}
	if csrfCookie == nil || csrfCookie.Value == "" {
		t.Fatalf("expected x-csrf-token cookie to be set, got: %v", cookies)
	}

	// Verify body contains csrfToken
	var body map[string]string
	_ = json.NewDecoder(res.Body).Decode(&body)
	token, exists := body["csrfToken"]
	if !exists || token == "" {
		t.Fatalf("expected csrfToken in response body, got: %v", body)
	}

	// 2. Validate token
	if !manager.ValidateToken(csrfCookie.Value, token) {
		t.Errorf("token validation failed for valid pair")
	}

	// 3. Reject tampered token
	if manager.ValidateToken(csrfCookie.Value, token+"tampered") {
		t.Errorf("token validation should fail for tampered token")
	}

	// 4. Reject tampered cookie
	if manager.ValidateToken(csrfCookie.Value+"tampered", token) {
		t.Errorf("token validation should fail for tampered cookie")
	}
}

func TestCSRFMiddleware_Enforcement(t *testing.T) {
	secret := "production-test-secret"
	cookieCfg := auth.CookieConfig{Path: "/", HTTPOnly: true}
	manager := NewCSRFManager(secret, "x-csrf-token", cookieCfg, false)

	protectedHandler := manager.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("protected-action-ok"))
	}))

	cookieNonce, token, err := manager.GenerateToken()
	if err != nil {
		t.Fatalf("token generation failed: %v", err)
	}

	// 1. Safe methods (GET) pass without token
	getReq := httptest.NewRequest(http.MethodGet, "/api/data", nil)
	getRec := httptest.NewRecorder()
	protectedHandler.ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Errorf("GET should pass without CSRF token, got %d", getRec.Code)
	}

	// 2. POST without cookie or token fails (403)
	postNoTokenReq := httptest.NewRequest(http.MethodPost, "/api/data", strings.NewReader(`{}`))
	postNoTokenRec := httptest.NewRecorder()
	protectedHandler.ServeHTTP(postNoTokenRec, postNoTokenReq)
	if postNoTokenRec.Code != http.StatusForbidden {
		t.Errorf("POST without token expected 403, got %d", postNoTokenRec.Code)
	}
	if !strings.Contains(postNoTokenRec.Body.String(), "CSRF_ERROR") {
		t.Errorf("expected CSRF_ERROR in body, got: %s", postNoTokenRec.Body.String())
	}

	// 3. POST with cookie but invalid header token fails (403)
	postBadTokenReq := httptest.NewRequest(http.MethodPost, "/api/data", strings.NewReader(`{}`))
	postBadTokenReq.AddCookie(&http.Cookie{Name: "x-csrf-token", Value: cookieNonce})
	postBadTokenReq.Header.Set("X-CSRF-Token", "invalid-token-xyz")
	postBadTokenRec := httptest.NewRecorder()
	protectedHandler.ServeHTTP(postBadTokenRec, postBadTokenReq)
	if postBadTokenRec.Code != http.StatusForbidden {
		t.Errorf("POST with bad token expected 403, got %d", postBadTokenRec.Code)
	}

	// 4. POST with valid cookie and header token succeeds (200)
	postValidReq := httptest.NewRequest(http.MethodPost, "/api/data", strings.NewReader(`{}`))
	postValidReq.AddCookie(&http.Cookie{Name: "x-csrf-token", Value: cookieNonce})
	postValidReq.Header.Set("X-CSRF-Token", token)
	postValidRec := httptest.NewRecorder()
	protectedHandler.ServeHTTP(postValidRec, postValidReq)
	if postValidRec.Code != http.StatusOK {
		t.Errorf("POST with valid token expected 200, got %d", postValidRec.Code)
	}

	// 5. Worker call bypasses CSRF
	workerReq := httptest.NewRequest(http.MethodPost, "/api/data", strings.NewReader(`{}`))
	ctx := auth.WithWorkerCall(workerReq.Context(), true)
	workerReq = workerReq.WithContext(ctx)
	workerRec := httptest.NewRecorder()
	protectedHandler.ServeHTTP(workerRec, workerReq)
	if workerRec.Code != http.StatusOK {
		t.Errorf("worker call expected 200 without token, got %d", workerRec.Code)
	}

	// 6. Test mode bypasses CSRF
	testModeManager := NewCSRFManager(secret, "x-csrf-token", cookieCfg, true)
	testHandler := testModeManager.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	testReq := httptest.NewRequest(http.MethodPost, "/api/data", strings.NewReader(`{}`))
	testRec := httptest.NewRecorder()
	testHandler.ServeHTTP(testRec, testReq)
	if testRec.Code != http.StatusOK {
		t.Errorf("test mode expected 200 without token, got %d", testRec.Code)
	}
}

func TestCSRFManager_EmptySecret_CannotGenerate(t *testing.T) {
	cookieCfg := auth.CookieConfig{Path: "/"}
	manager := NewCSRFManager("", "x-csrf-token", cookieCfg, false)

	_, _, err := manager.GenerateToken()
	if err == nil {
		t.Fatal("expected error when generating CSRF token with empty secret, got nil")
	}
}
