package health

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aaradhychinche-alt/WhatBreaks/internal/logging"
)

type mockPinger struct {
	err error
}

func (m *mockPinger) Ping(ctx context.Context) error {
	return m.err
}

func TestAPIHealthHandler_RootRoute(t *testing.T) {
	handler := NewAPIHealthHandler(APIHealthConfig{})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on GET /, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/plain") {
		t.Errorf("expected text/plain Content-Type, got %q", ct)
	}
	if rec.Body.String() != "API running" {
		t.Errorf("expected body %q, got %q", "API running", rec.Body.String())
	}
}

func TestAPIHealthHandler_HealthSuccess(t *testing.T) {
	fixedTime := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	pinger := &mockPinger{err: nil}

	cfg := APIHealthConfig{
		Pinger:      pinger,
		Environment: "production",
		StartTime:   fixedTime.Add(-100 * time.Second),
		Now:         func() time.Time { return fixedTime },
		Uptime:      func() float64 { return 100.0 },
	}
	handler := NewAPIHealthHandler(cfg)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on GET /health, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("expected application/json Content-Type, got %q", ct)
	}

	var resp APIHealthResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode JSON response: %v", err)
	}

	if resp.Status != "healthy" {
		t.Errorf("expected status 'healthy', got %q", resp.Status)
	}
	if resp.Environment != "production" {
		t.Errorf("expected environment 'production', got %q", resp.Environment)
	}
	if resp.Uptime != 100.0 {
		t.Errorf("expected uptime 100.0, got %f", resp.Uptime)
	}
	if resp.Timestamp != fixedTime.Format(time.RFC3339Nano) {
		t.Errorf("expected timestamp %s, got %s", fixedTime.Format(time.RFC3339Nano), resp.Timestamp)
	}
	if resp.Error != "" {
		t.Errorf("expected empty error on success, got %q", resp.Error)
	}
}

func TestAPIHealthHandler_HealthFailureAndLogging(t *testing.T) {
	var logBuf bytes.Buffer
	logger := logging.NewStandardLogger(&logBuf, logging.LevelDebug)
	dbErr := errors.New("connection to postgres:5432 refused")
	pinger := &mockPinger{err: dbErr}

	cfg := APIHealthConfig{
		Pinger:      pinger,
		Logger:      logger,
		Environment: "staging",
	}
	handler := NewAPIHealthHandler(cfg)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 on database error, got %d", rec.Code)
	}

	var resp APIHealthResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode JSON response: %v", err)
	}

	if resp.Status != "unhealthy" {
		t.Errorf("expected status 'unhealthy', got %q", resp.Status)
	}
	if resp.Error != dbErr.Error() {
		t.Errorf("expected error %q, got %q", dbErr.Error(), resp.Error)
	}

	// Verify that failure was logged via logging package
	logOutput := logBuf.String()
	if !strings.Contains(logOutput, "Health check failed") {
		t.Errorf("expected 'Health check failed' in log output: %s", logOutput)
	}
	if !strings.Contains(logOutput, "connection to postgres:5432 refused") {
		t.Errorf("expected db error details in log output: %s", logOutput)
	}
}

func TestAPIHealthHandler_MethodAndPathRejection(t *testing.T) {
	handler := NewAPIHealthHandler(APIHealthConfig{})

	// POST /health should return 404
	reqPost := httptest.NewRequest(http.MethodPost, "/health", nil)
	recPost := httptest.NewRecorder()
	handler.ServeHTTP(recPost, reqPost)
	if recPost.Code != http.StatusNotFound {
		t.Errorf("expected 404 on POST /health, got %d", recPost.Code)
	}

	// Unknown path /metrics should return 404
	reqUnknown := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	recUnknown := httptest.NewRecorder()
	handler.ServeHTTP(recUnknown, reqUnknown)
	if recUnknown.Code != http.StatusNotFound {
		t.Errorf("expected 404 on GET /metrics, got %d", recUnknown.Code)
	}
}

func TestAPIHealthResponses_ResponseSafety(t *testing.T) {
	// Assert no credential leakage in API health response
	dbErr := errors.New("failed to connect to database on host db.internal:5432")
	pinger := &mockPinger{err: dbErr}

	var logBuf bytes.Buffer
	logger := logging.NewStandardLogger(&logBuf, logging.LevelDebug)

	handler := NewAPIHealthHandler(APIHealthConfig{
		Pinger:      pinger,
		Logger:      logger,
		Environment: "production",
	})

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	body := rec.Body.String()
	var parsed map[string]any
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		t.Fatalf("failed to decode json: %v", err)
	}

	// Check allowed fields
	for k := range parsed {
		if k != "status" && k != "timestamp" && k != "uptime" && k != "environment" && k != "error" {
			t.Errorf("unexpected field %q in API health response", k)
		}
	}
}
