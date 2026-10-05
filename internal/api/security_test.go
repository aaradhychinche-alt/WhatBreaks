package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSecurityHeaders(t *testing.T) {
	appURL := "https://dashboard.whatbreaks.dev"
	handler := SecurityHeaders(appURL)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	res := rec.Result()
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", res.StatusCode)
	}

	h := res.Header

	// 1. CSP
	csp := h.Get("Content-Security-Policy")
	if csp == "" {
		t.Fatal("missing Content-Security-Policy header")
	}
	expectedDirectives := []string{
		"default-src 'self'",
		"style-src 'self' 'unsafe-inline'",
		"script-src 'self'",
		"img-src 'self' data: https:",
		"connect-src 'self' https://dashboard.whatbreaks.dev",
		"font-src 'self'",
		"object-src 'none'",
		"media-src 'self'",
		"frame-src 'none'",
		"base-uri 'self'",
		"form-action 'self'",
		"frame-ancestors 'none'",
	}
	for _, dir := range expectedDirectives {
		if !strings.Contains(csp, dir) {
			t.Errorf("CSP missing directive %q; got: %s", dir, csp)
		}
	}

	// 2. HSTS
	hsts := h.Get("Strict-Transport-Security")
	if hsts != "max-age=31536000; includeSubDomains; preload" {
		t.Errorf("unexpected Strict-Transport-Security: %q", hsts)
	}

	// 3. X-Content-Type-Options
	if nosniff := h.Get("X-Content-Type-Options"); nosniff != "nosniff" {
		t.Errorf("expected X-Content-Type-Options: nosniff, got %q", nosniff)
	}

	// 4. X-Frame-Options
	if xfo := h.Get("X-Frame-Options"); xfo != "DENY" {
		t.Errorf("expected X-Frame-Options: DENY, got %q", xfo)
	}

	// 5. X-XSS-Protection
	if xss := h.Get("X-XSS-Protection"); xss != "0" {
		t.Errorf("expected X-XSS-Protection: 0, got %q", xss)
	}

	// 6. Referrer-Policy
	if ref := h.Get("Referrer-Policy"); ref != "strict-origin-when-cross-origin" {
		t.Errorf("expected Referrer-Policy: strict-origin-when-cross-origin, got %q", ref)
	}

	// 7. Cross-Origin-Resource-Policy
	if corp := h.Get("Cross-Origin-Resource-Policy"); corp != "cross-origin" {
		t.Errorf("expected Cross-Origin-Resource-Policy: cross-origin, got %q", corp)
	}
}

func TestSecurityHeaders_DefaultAppURL(t *testing.T) {
	handler := SecurityHeaders("")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	csp := rec.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, DefaultAppURL) {
		t.Errorf("expected default AppURL in CSP connect-src, got: %s", csp)
	}
}
