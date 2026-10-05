package api

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aaradhychinche-alt/WhatBreaks/internal/auth"
)

func TestRateLimiter_BelowAndAboveLimit(t *testing.T) {
	window := time.Minute
	maxRequests := 3

	limiter := NewRateLimiter(window, maxRequests, false, nil)
	handler := limiter.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))

	// Request 1: OK
	req1 := httptest.NewRequest(http.MethodGet, "/resource", nil)
	req1.RemoteAddr = "192.168.1.100:12345"
	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("request 1 failed: %d", rec1.Code)
	}
	if rec1.Header().Get("RateLimit-Remaining") != "2" {
		t.Errorf("expected remaining 2, got %q", rec1.Header().Get("RateLimit-Remaining"))
	}

	// Request 2: OK
	req2 := httptest.NewRequest(http.MethodGet, "/resource", nil)
	req2.RemoteAddr = "192.168.1.100:12345"
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("request 2 failed: %d", rec2.Code)
	}
	if rec2.Header().Get("RateLimit-Remaining") != "1" {
		t.Errorf("expected remaining 1, got %q", rec2.Header().Get("RateLimit-Remaining"))
	}

	// Request 3: OK (reaches max)
	req3 := httptest.NewRequest(http.MethodGet, "/resource", nil)
	req3.RemoteAddr = "192.168.1.100:12345"
	rec3 := httptest.NewRecorder()
	handler.ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusOK {
		t.Fatalf("request 3 failed: %d", rec3.Code)
	}
	if rec3.Header().Get("RateLimit-Remaining") != "0" {
		t.Errorf("expected remaining 0, got %q", rec3.Header().Get("RateLimit-Remaining"))
	}

	// Request 4: Exceeded -> 429 Too Many Requests
	req4 := httptest.NewRequest(http.MethodGet, "/resource", nil)
	req4.RemoteAddr = "192.168.1.100:12345"
	rec4 := httptest.NewRecorder()
	handler.ServeHTTP(rec4, req4)
	if rec4.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 Too Many Requests, got %d", rec4.Code)
	}
	if rec4.Header().Get("RateLimit-Remaining") != "0" {
		t.Errorf("expected remaining 0 on 429, got %q", rec4.Header().Get("RateLimit-Remaining"))
	}

	// Different IP: Still OK
	reqDiff := httptest.NewRequest(http.MethodGet, "/resource", nil)
	reqDiff.RemoteAddr = "10.0.0.1:54321"
	recDiff := httptest.NewRecorder()
	handler.ServeHTTP(recDiff, reqDiff)
	if recDiff.Code != http.StatusOK {
		t.Fatalf("different IP should succeed, got %d", recDiff.Code)
	}
}

func TestRateLimiter_WindowReset(t *testing.T) {
	window := 100 * time.Millisecond
	maxRequests := 1

	mockNow := time.Now()
	limiter := NewRateLimiter(window, maxRequests, false, nil)
	limiter.SetNow(func() time.Time {
		return mockNow
	})

	handler := limiter.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Request 1: OK
	req1 := httptest.NewRequest(http.MethodGet, "/test", nil)
	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("request 1 failed: %d", rec1.Code)
	}

	// Request 2: Blocked
	req2 := httptest.NewRequest(http.MethodGet, "/test", nil)
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusTooManyRequests {
		t.Fatalf("request 2 expected 429, got %d", rec2.Code)
	}

	// Advance time past window
	mockNow = mockNow.Add(150 * time.Millisecond)

	// Request 3: OK after reset
	req3 := httptest.NewRequest(http.MethodGet, "/test", nil)
	rec3 := httptest.NewRecorder()
	handler.ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusOK {
		t.Fatalf("request 3 expected 200 after window reset, got %d", rec3.Code)
	}
}

func TestRateLimiter_Exemptions(t *testing.T) {
	limiter := NewRateLimiter(time.Minute, 1, false, nil)
	handler := limiter.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// 1. Health routes are exempt
	for _, path := range []string{"/", "/health", "/healthz", "/readyz"} {
		for i := 0; i < 5; i++ {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("path %q request %d should be exempt, got %d", path, i, rec.Code)
			}
		}
	}

	// 2. CSRF and session endpoints are exempt
	for _, path := range []string{"/api/session", "/api/csrf-token"} {
		for i := 0; i < 5; i++ {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("path %q request %d should be exempt, got %d", path, i, rec.Code)
			}
		}
	}

	// 3. Authenticated API user is exempt from global limiter
	for i := 0; i < 5; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/profile", nil)
		ctx := auth.WithUser(req.Context(), &auth.User{ID: "usr-123", Role: "member"})
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("authenticated api request %d should be exempt, got %d", i, rec.Code)
		}
	}
}

func TestRateLimiter_Concurrency(t *testing.T) {
	limiter := NewRateLimiter(time.Minute, 10, false, nil)
	handler := limiter.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	const callers = 30
	var wg sync.WaitGroup
	var okCount, rateLimitedCount atomic.Int32

	wg.Add(callers)
	for i := 0; i < callers; i++ {
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodGet, "/concurrent", nil)
			req.RemoteAddr = "192.168.1.50:5000"
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code == http.StatusOK {
				okCount.Add(1)
			} else if rec.Code == http.StatusTooManyRequests {
				rateLimitedCount.Add(1)
			}
		}()
	}

	wg.Wait()

	if okCount.Load() != 10 {
		t.Errorf("expected exactly 10 OK responses, got %d", okCount.Load())
	}
	if rateLimitedCount.Load() != 20 {
		t.Errorf("expected exactly 20 rate-limited responses, got %d", rateLimitedCount.Load())
	}
}
