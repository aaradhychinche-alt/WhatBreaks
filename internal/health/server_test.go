package health

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aaradhychinche-alt/WhatBreaks/internal/logging"
)

type mockPort struct {
	alive bool
	ready bool
}

func (m *mockPort) IsAlive() bool { return m.alive }
func (m *mockPort) IsReady() bool { return m.ready }

func TestControllerHealthHandler_Endpoints(t *testing.T) {
	state := NewState()
	handler := ControllerHealthHandler(state)

	// Case 1: Initially healthy=true, ready=false
	reqHealth := httptest.NewRequest(http.MethodGet, PathHealthz, nil)
	recHealth := httptest.NewRecorder()
	handler.ServeHTTP(recHealth, reqHealth)

	if recHealth.Code != http.StatusOK {
		t.Fatalf("expected 200 on /healthz, got %d", recHealth.Code)
	}
	if ct := recHealth.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Errorf("expected Content-Type application/json; charset=utf-8, got %q", ct)
	}
	var respHealth ProbeResponse
	if err := json.Unmarshal(recHealth.Body.Bytes(), &respHealth); err != nil {
		t.Fatalf("failed to decode JSON response: %v", err)
	}
	if respHealth.Status != StatusOk {
		t.Errorf("expected status %q, got %q", StatusOk, respHealth.Status)
	}

	reqReady := httptest.NewRequest(http.MethodGet, PathReadyz, nil)
	recReady := httptest.NewRecorder()
	handler.ServeHTTP(recReady, reqReady)

	if recReady.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 on /readyz when not ready, got %d", recReady.Code)
	}
	var respReady ProbeResponse
	if err := json.Unmarshal(recReady.Body.Bytes(), &respReady); err != nil {
		t.Fatalf("failed to decode JSON response: %v", err)
	}
	if respReady.Status != StatusNotReady {
		t.Errorf("expected status %q, got %q", StatusNotReady, respReady.Status)
	}

	// Case 2: Transition to ready=true
	state.SetReady(true)
	recReady2 := httptest.NewRecorder()
	handler.ServeHTTP(recReady2, reqReady)

	if recReady2.Code != http.StatusOK {
		t.Fatalf("expected 200 on /readyz when ready, got %d", recReady2.Code)
	}
	var respReady2 ProbeResponse
	if err := json.Unmarshal(recReady2.Body.Bytes(), &respReady2); err != nil {
		t.Fatalf("failed to decode JSON response: %v", err)
	}
	if respReady2.Status != StatusReady {
		t.Errorf("expected status %q, got %q", StatusReady, respReady2.Status)
	}

	// Case 3: Transition to healthy=false (e.g. process failure)
	state.SetHealthy(false)
	recHealth2 := httptest.NewRecorder()
	handler.ServeHTTP(recHealth2, reqHealth)

	if recHealth2.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 on /healthz when unhealthy, got %d", recHealth2.Code)
	}
	var respHealth2 ProbeResponse
	if err := json.Unmarshal(recHealth2.Body.Bytes(), &respHealth2); err != nil {
		t.Fatalf("failed to decode JSON response: %v", err)
	}
	if respHealth2.Status != StatusUnavailable {
		t.Errorf("expected status %q, got %q", StatusUnavailable, respHealth2.Status)
	}
}

func TestControllerHealthHandler_MethodAndPathRejection(t *testing.T) {
	state := NewState()
	handler := ControllerHealthHandler(state)

	// In kubernetes/controller/health-server.js:
	// Any method != GET returns 404 with {"status":"not_found"}
	methods := []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch, http.MethodHead}
	for _, m := range methods {
		req := httptest.NewRequest(m, PathHealthz, nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusNotFound {
			t.Errorf("expected 404 on %s, got %d", m, rec.Code)
		}
		var resp ProbeResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("failed to decode JSON: %v", err)
		}
		if resp.Status != StatusNotFound {
			t.Errorf("expected status not_found on %s, got %q", m, resp.Status)
		}
	}

	// Unknown paths return 404 with {"status":"not_found"}
	unknownPaths := []string{"/", "/unknown", "/health", "/livez", "/healthz/extra"}
	for _, p := range unknownPaths {
		req := httptest.NewRequest(http.MethodGet, p, nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusNotFound {
			t.Errorf("expected 404 on path %s, got %d", p, rec.Code)
		}
		var resp ProbeResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("failed to decode JSON: %v", err)
		}
		if resp.Status != StatusNotFound {
			t.Errorf("expected status not_found on path %s, got %q", p, resp.Status)
		}
	}
}

func TestControllerLifecycleChecker_Integration(t *testing.T) {
	client := &mockPort{alive: true, ready: true}
	reporter := &mockPort{alive: true, ready: true}
	phase := "starting"
	accepting := false

	checker := &ControllerLifecycleChecker{
		PhaseFn:       func() string { return phase },
		AcceptingWork: func() bool { return accepting },
		ClientPort:    client,
		ReporterPort:  reporter,
	}

	handler := ControllerHealthHandler(checker)

	// Phase: starting -> healthz unavailable (503), readyz not ready (503)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, PathHealthz, nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 during starting phase on /healthz, got %d", rec.Code)
	}

	// Phase: running, but acceptingWork is false -> healthz 200 ok, readyz 503 not_ready
	phase = "running"
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, PathHealthz, nil))
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 during running phase on /healthz, got %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, PathReadyz, nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 when not accepting work on /readyz, got %d", rec.Code)
	}

	// Phase: running and acceptingWork=true -> both 200
	accepting = true
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, PathReadyz, nil))
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 when running and accepting on /readyz, got %d", rec.Code)
	}

	// Dependent port failure (client alive=false) -> healthz 503, readyz 503
	client.alive = false
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, PathHealthz, nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 when client port not alive on /healthz, got %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, PathReadyz, nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 when client port not alive on /readyz, got %d", rec.Code)
	}

	// Restore alive, but client ready=false -> healthz 200, readyz 503
	client.alive = true
	client.ready = false
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, PathHealthz, nil))
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 on /healthz when alive, got %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, PathReadyz, nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 on /readyz when client port not ready, got %d", rec.Code)
	}
}

func TestServer_LifecycleAndHTTP(t *testing.T) {
	state := NewState()
	state.SetHealthy(true)
	state.SetReady(true)

	var logBuf bytes.Buffer
	logger := logging.NewStandardLogger(&logBuf, logging.LevelDebug)

	// Use port 0 to bind to an available ephemeral port
	server := NewServer("127.0.0.1", 0, state, logger)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := server.Start(ctx); err != nil {
		t.Fatalf("failed to start server: %v", err)
	}
	defer func() { _ = server.Close() }()

	addr := server.Addr()
	if addr == nil {
		t.Fatalf("expected non-nil server address")
	}
	port := server.Port()
	if port <= 0 {
		t.Fatalf("expected positive bound port, got %d", port)
	}

	// Make real HTTP requests against the running listener
	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	client := &http.Client{Timeout: 2 * time.Second}

	// Test GET /healthz
	resp, err := client.Get(baseURL + PathHealthz)
	if err != nil {
		t.Fatalf("GET /healthz failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 OK, got %d", resp.StatusCode)
	}

	// Test GET /readyz
	respReady, err := client.Get(baseURL + PathReadyz)
	if err != nil {
		t.Fatalf("GET /readyz failed: %v", err)
	}
	defer respReady.Body.Close()
	if respReady.StatusCode != http.StatusOK {
		t.Errorf("expected 200 OK, got %d", respReady.StatusCode)
	}

	// Test GET /unknown (404)
	resp404, err := client.Get(baseURL + "/not-found")
	if err != nil {
		t.Fatalf("GET /not-found failed: %v", err)
	}
	defer resp404.Body.Close()
	if resp404.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 Not Found, got %d", resp404.StatusCode)
	}

	// Test graceful shutdown
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer shutdownCancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		t.Errorf("server shutdown failed: %v", err)
	}
}

func TestServer_PortAlreadyInUse(t *testing.T) {
	// Allocate a real port with a raw listener
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to bind test listener: %v", err)
	}
	defer ln.Close()

	port := ln.Addr().(*net.TCPAddr).Port

	server := NewServer("127.0.0.1", port, NewState(), nil)
	err = server.Start(context.Background())
	if err == nil {
		_ = server.Close()
		t.Fatalf("expected error binding to already-in-use port %d", port)
	}
}

func TestHealthResponses_NoSecretsOrSensitiveData(t *testing.T) {
	state := NewState()
	handler := ControllerHealthHandler(state)

	for _, path := range []string{PathHealthz, PathReadyz, "/unknown"} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))

		body := rec.Body.String()

		// Verify response has only 'status' field and no metadata or config leakage
		var parsed map[string]any
		if err := json.Unmarshal([]byte(body), &parsed); err != nil {
			t.Fatalf("failed to parse JSON from %s: %v", path, err)
		}

		if len(parsed) != 1 {
			t.Errorf("expected exactly 1 field (status) in response for %s, got %d: %+v", path, len(parsed), parsed)
		}
		if _, ok := parsed["status"]; !ok {
			t.Errorf("missing 'status' field in response for %s", path)
		}

		for _, secretKeyword := range []string{"password", "token", "secret", "database", "key", "config"} {
			if strings.Contains(strings.ToLower(body), secretKeyword) {
				t.Errorf("security violation: sensitive keyword %q found in response for %s: %s", secretKeyword, path, body)
			}
		}
	}
}
