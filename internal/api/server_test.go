package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aaradhychinche-alt/WhatBreaks/internal/config"
	"github.com/aaradhychinche-alt/WhatBreaks/internal/logging"
)

type mockPinger struct {
	err error
}

func (m *mockPinger) Ping(ctx context.Context) error {
	return m.err
}

func TestServer_HTTPIntegration_Endpoints(t *testing.T) {
	pinger := &mockPinger{}
	envMap := config.MapEnv(map[string]string{
		"PORT":           "0",
		"HOST":           "127.0.0.1",
		"NODE_ENV":       "development",
		"SESSION_SECRET": "test-session-secret-for-api-tests",
		"APP_URL":        "http://localhost:5173",
	})
	logger := logging.NewStandardLogger(nil, logging.LevelDebug)

	cfg := NewConfigFromEnv(envMap, logger)
	cfg.HealthPinger = pinger
	cfg.Port = 0 // Ephemeral port for test

	srv := NewServer(cfg)
	if err := srv.Listen(); err != nil {
		t.Fatalf("failed to start server: %v", err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	client := &http.Client{Timeout: 5 * time.Second}
	baseURL := "http://" + srv.Addr()

	// 1. Root route: GET / -> "API running"
	resRoot, err := client.Get(baseURL + "/")
	if err != nil {
		t.Fatalf("GET / failed: %v", err)
	}
	bodyRoot, _ := io.ReadAll(resRoot.Body)
	_ = resRoot.Body.Close()

	if resRoot.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resRoot.StatusCode)
	}
	if string(bodyRoot) != "API running" {
		t.Errorf("expected 'API running', got: %s", string(bodyRoot))
	}

	// 2. Health check healthy: GET /health -> status 200
	resHealth, err := client.Get(baseURL + "/health")
	if err != nil {
		t.Fatalf("GET /health failed: %v", err)
	}
	var healthBody map[string]any
	_ = json.NewDecoder(resHealth.Body).Decode(&healthBody)
	_ = resHealth.Body.Close()

	if resHealth.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resHealth.StatusCode)
	}
	if healthBody["status"] != "healthy" {
		t.Errorf("expected status healthy, got: %v", healthBody["status"])
	}

	// 3. Health check unhealthy: set pinger error -> status 503
	pinger.err = errors.New("database disconnected")
	resUnhealthy, err := client.Get(baseURL + "/health")
	if err != nil {
		t.Fatalf("GET /health failed: %v", err)
	}
	var unhBody map[string]any
	_ = json.NewDecoder(resUnhealthy.Body).Decode(&unhBody)
	_ = resUnhealthy.Body.Close()

	if resUnhealthy.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", resUnhealthy.StatusCode)
	}
	if unhBody["status"] != "unhealthy" {
		t.Errorf("expected status unhealthy, got: %v", unhBody["status"])
	}

	// 4. CSRF token retrieval: GET /api/csrf-token
	resCsrf, err := client.Get(baseURL + "/api/csrf-token")
	if err != nil {
		t.Fatalf("GET /api/csrf-token failed: %v", err)
	}
	var csrfBody map[string]string
	_ = json.NewDecoder(resCsrf.Body).Decode(&csrfBody)
	_ = resCsrf.Body.Close()

	if resCsrf.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resCsrf.StatusCode)
	}
	if csrfBody["csrfToken"] == "" {
		t.Errorf("expected non-empty csrfToken, got: %v", csrfBody)
	}

	// 5. 404 handler for unknown route
	res404, err := client.Get(baseURL + "/api/nonexistent-endpoint")
	if err != nil {
		t.Fatalf("GET /nonexistent failed: %v", err)
	}
	var body404 map[string]string
	_ = json.NewDecoder(res404.Body).Decode(&body404)
	_ = res404.Body.Close()

	if res404.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", res404.StatusCode)
	}
	if body404["code"] != "NOT_FOUND" {
		t.Errorf("expected NOT_FOUND, got: %v", body404)
	}
}

func TestServer_PanicRecovery(t *testing.T) {
	cfg := NewConfigFromEnv(nil, nil)
	srv := NewServer(cfg)

	// Mount a handler that panics
	srv.RegisterRoute("/panic", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("simulated fatal panic in handler")
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/panic", nil)

	srv.Handler().ServeHTTP(rec, req)

	res := rec.Result()
	defer res.Body.Close()

	if res.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected 500 Internal Server Error, got %d", res.StatusCode)
	}

	var body map[string]string
	_ = json.NewDecoder(res.Body).Decode(&body)
	if body["code"] != "INTERNAL_ERROR" {
		t.Errorf("expected INTERNAL_ERROR code, got: %v", body)
	}
}

func TestServer_GracefulShutdown(t *testing.T) {
	cfg := NewConfigFromEnv(nil, nil)
	cfg.Port = 0
	srv := NewServer(cfg)

	if err := srv.Listen(); err != nil {
		t.Fatalf("failed to listen: %v", err)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		t.Errorf("shutdown returned error: %v", err)
	}

	// Second shutdown call should be idempotent
	if err := srv.Shutdown(shutdownCtx); err != nil {
		t.Errorf("second shutdown returned error: %v", err)
	}
}
