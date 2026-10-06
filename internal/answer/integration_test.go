package answer_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aaradhychinche-alt/WhatBreaks/internal/answer"
	"github.com/aaradhychinche-alt/WhatBreaks/internal/coreclient"
	"github.com/aaradhychinche-alt/WhatBreaks/internal/logging"
)

var (
	fixtureBinOnce sync.Once
	fixtureBinPath string
	fixtureBinErr  error

	prodBinOnce sync.Once
	prodBinPath string
	prodBinErr  error
)

func findRepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("repository root not found")
		}
		dir = parent
	}
}

func ensureFixtureServer(repoRoot string) (string, error) {
	fixtureBinOnce.Do(func() {
		bin := filepath.Join(repoRoot, "target", "debug", "examples", "fixture_server")
		if _, err := os.Stat(bin); err == nil {
			fixtureBinPath = bin
			return
		}
		cmd := exec.Command("cargo", "build", "-p", "wb-core-server", "--example", "fixture_server")
		cmd.Dir = repoRoot
		out, err := cmd.CombinedOutput()
		if err != nil {
			fixtureBinErr = fmt.Errorf("failed to build fixture_server: %w (out: %s)", err, string(out))
			return
		}
		fixtureBinPath = bin
	})
	return fixtureBinPath, fixtureBinErr
}

func ensureProdServer(repoRoot string) (string, error) {
	prodBinOnce.Do(func() {
		bin := filepath.Join(repoRoot, "target", "debug", "wb-core-server")
		if _, err := os.Stat(bin); err == nil {
			prodBinPath = bin
			return
		}
		cmd := exec.Command("cargo", "build", "-p", "wb-core-server")
		cmd.Dir = repoRoot
		out, err := cmd.CombinedOutput()
		if err != nil {
			prodBinErr = fmt.Errorf("failed to build wb-core-server: %w (out: %s)", err, string(out))
			return
		}
		prodBinPath = bin
	})
	return prodBinPath, prodBinErr
}

func getFreeLocalAddr() (string, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	defer l.Close()
	return l.Addr().String(), nil
}

func startServerProcess(t *testing.T, binPath string) (*coreclient.Client, string) {
	t.Helper()

	addr, err := getFreeLocalAddr()
	if err != nil {
		t.Fatalf("failed to allocate free address: %v", err)
	}

	var logs bytes.Buffer
	cmd := exec.Command(binPath)
	cmd.Env = append(os.Environ(), "WB_CORE_GRPC_ADDR="+addr)
	cmd.Stdout = &logs
	cmd.Stderr = &logs

	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start server process (%s): %v", binPath, err)
	}

	exited := make(chan error, 1)
	go func() {
		exited <- cmd.Wait()
	}()

	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Signal(os.Interrupt)
			select {
			case <-exited:
			case <-time.After(2 * time.Second):
				_ = cmd.Process.Kill()
			}
		}
	})

	readyDeadline := time.Now().Add(5 * time.Second)
	ready := false
	for time.Now().Before(readyDeadline) {
		select {
		case exitErr := <-exited:
			t.Fatalf("server exited unexpectedly: %v (logs: %s)", exitErr, logs.String())
		default:
		}

		conn, dialErr := net.DialTimeout("tcp", addr, 50*time.Millisecond)
		if dialErr == nil {
			_ = conn.Close()
			ready = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if !ready {
		t.Fatalf("timed out waiting for server on %s (logs: %s)", addr, logs.String())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	client, err := coreclient.Connect(ctx, addr)
	if err != nil {
		t.Fatalf("failed to connect client to %s: %v", addr, err)
	}
	t.Cleanup(func() {
		_ = client.Close()
	})

	return client, addr
}

func startFixtureServer(t *testing.T) *coreclient.Client {
	t.Helper()
	repoRoot, err := findRepoRoot()
	if err != nil {
		t.Fatalf("could not find repo root: %v", err)
	}
	bin, err := ensureFixtureServer(repoRoot)
	if err != nil {
		t.Fatalf("could not ensure fixture server: %v", err)
	}
	client, _ := startServerProcess(t, bin)
	return client
}

func startProdServer(t *testing.T) *coreclient.Client {
	t.Helper()
	repoRoot, err := findRepoRoot()
	if err != nil {
		t.Fatalf("could not find repo root: %v", err)
	}
	bin, err := ensureProdServer(repoRoot)
	if err != nil {
		t.Fatalf("could not ensure prod server: %v", err)
	}
	client, _ := startServerProcess(t, bin)
	return client
}

// ---------------------------------------------------------------------------
// Test 21: Go API → Go AnswerService → Rust AnswerService
// ---------------------------------------------------------------------------
func TestIntegration_GoAPIToGoAnswerServiceToRustAnswerService(t *testing.T) {
	client := startProdServer(t)

	logger := logging.NewStandardLogger(nil, logging.LevelDebug)
	svc := answer.NewService(client, logger)
	handler := answer.NewHandler(svc, answer.WithLogger(logger))

	ts := httptest.NewServer(handler)
	defer ts.Close()

	payload := `{
		"target": {
			"provider": "kubernetes",
			"resource_type": "pod",
			"provider_id": "payments/payments-api"
		},
		"direction": "incoming",
		"max_depth": 3
	}`

	resp, err := http.Post(ts.URL, "application/json", strings.NewReader(payload))
	if err != nil {
		t.Fatalf("HTTP POST failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected HTTP 200 OK across Go API -> Go Service -> Rust boundary, got %d", resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// Test 22: real AnswerEngine execution
// ---------------------------------------------------------------------------
func TestIntegration_RealAnswerEngineExecution(t *testing.T) {
	client := startFixtureServer(t)

	logger := logging.NewStandardLogger(nil, logging.LevelDebug)
	svc := answer.NewService(client, logger)

	req := answer.ImpactRequest{
		Target: &answer.ResourceIdentity{
			Provider:     "kubernetes",
			ResourceType: "service",
			ProviderID:   "payments-api",
		},
		Direction: "incoming",
		MaxDepth:  5,
	}

	ans, err := svc.AnalyzeImpact(context.Background(), req)
	if err != nil {
		t.Fatalf("AnalyzeImpact failed on real AnswerEngine: %v", err)
	}

	// Real AnswerEngine executed: found 2 candidate impacted resources (checkout-service, frontend)
	if ans.Summary.ImpactedCount != 2 {
		t.Errorf("expected 2 impacted resources from real AnswerEngine execution, got %d", ans.Summary.ImpactedCount)
	}
	if ans.Summary.DirectCount != 1 {
		t.Errorf("expected 1 direct dependent, got %d", ans.Summary.DirectCount)
	}
	if ans.Summary.IndirectCount != 1 {
		t.Errorf("expected 1 indirect dependent, got %d", ans.Summary.IndirectCount)
	}
	if ans.Summary.MaxDepth != 2 {
		t.Errorf("expected max depth 2, got %d", ans.Summary.MaxDepth)
	}
}

// ---------------------------------------------------------------------------
// Test 23: actual ImpactAnswer returned through HTTP
// ---------------------------------------------------------------------------
func TestIntegration_ActualImpactAnswerReturnedThroughHTTP(t *testing.T) {
	client := startFixtureServer(t)

	logger := logging.NewStandardLogger(nil, logging.LevelDebug)
	svc := answer.NewService(client, logger)
	handler := answer.NewHandler(svc, answer.WithLogger(logger))

	ts := httptest.NewServer(handler)
	defer ts.Close()

	payload := `{
		"target": {
			"provider": "kubernetes",
			"resource_type": "service",
			"provider_id": "payments-api"
		},
		"direction": "incoming",
		"max_depth": 5
	}`

	resp, err := http.Post(ts.URL, "application/json", strings.NewReader(payload))
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()

	var ans answer.ImpactAnswer
	if err := json.NewDecoder(resp.Body).Decode(&ans); err != nil {
		t.Fatalf("failed to decode ImpactAnswer JSON: %v", err)
	}

	if ans.Target.ProviderID != "payments-api" {
		t.Errorf("expected target payments-api, got %s", ans.Target.ProviderID)
	}
	if ans.Summary.ImpactedCount != 2 {
		t.Errorf("expected 2 impacted resources, got %d", ans.Summary.ImpactedCount)
	}
	if len(ans.ImpactedResources) != 2 {
		t.Errorf("expected 2 impacted resource entries, got %d", len(ans.ImpactedResources))
	}
}

// ---------------------------------------------------------------------------
// Test 24: evidence/provenance survives the boundary
// ---------------------------------------------------------------------------
func TestIntegration_EvidenceProvenanceSurvivesBoundary(t *testing.T) {
	client := startFixtureServer(t)

	svc := answer.NewService(client, nil)
	ans, err := svc.AnalyzeImpact(context.Background(), answer.ImpactRequest{
		Target: &answer.ResourceIdentity{
			Provider:     "kubernetes",
			ResourceType: "service",
			ProviderID:   "payments-api",
		},
		Direction: "incoming",
		MaxDepth:  5,
	})
	if err != nil {
		t.Fatalf("AnalyzeImpact failed: %v", err)
	}

	// 1. Evidence catalog entry survived
	if len(ans.Evidence) != 1 {
		t.Fatalf("expected 1 evidence entry, got %d", len(ans.Evidence))
	}
	ev := ans.Evidence[0]
	if ev.Source.Provider != "kubernetes" || ev.Source.Collector != "k8s-runtime" {
		t.Errorf("unexpected evidence source: %+v", ev.Source)
	}
	if ev.ObservationType != "RUNTIME_CONNECTION" {
		t.Errorf("unexpected observation type: %s", ev.ObservationType)
	}

	// 2. Relationship provenance association survived
	foundSupportedRel := false
	for _, rel := range ans.Relationships {
		if rel.Relationship.Source.ProviderID == "checkout-service" && rel.Relationship.Target.ProviderID == "payments-api" {
			foundSupportedRel = true
			if len(rel.EvidenceIDs) != 1 || rel.EvidenceIDs[0] != ev.ID {
				t.Errorf("expected relationship to be linked to evidence ID %s, got %v", ev.ID, rel.EvidenceIDs)
			}
		}
	}
	if !foundSupportedRel {
		t.Error("expected supported relationship between checkout-service and payments-api")
	}
}

// ---------------------------------------------------------------------------
// Test 25: paths survive the boundary
// ---------------------------------------------------------------------------
func TestIntegration_PathsSurviveBoundary(t *testing.T) {
	client := startFixtureServer(t)

	svc := answer.NewService(client, nil)
	ans, err := svc.AnalyzeImpact(context.Background(), answer.ImpactRequest{
		Target: &answer.ResourceIdentity{
			Provider:     "kubernetes",
			ResourceType: "service",
			ProviderID:   "payments-api",
		},
		Direction: "incoming",
		MaxDepth:  5,
	})
	if err != nil {
		t.Fatalf("AnalyzeImpact failed: %v", err)
	}

	if len(ans.Paths) != 2 {
		t.Fatalf("expected 2 traversal paths, got %d", len(ans.Paths))
	}

	// Direct path: checkout-service -> payments-api
	path1 := ans.Paths[0]
	if len(path1.Resources) != 2 || path1.Resources[0].ProviderID != "checkout-service" || path1.Resources[1].ProviderID != "payments-api" {
		t.Errorf("unexpected direct path resources: %+v", path1.Resources)
	}

	// Indirect path: frontend -> checkout-service -> payments-api
	path2 := ans.Paths[1]
	if len(path2.Resources) != 3 || path2.Resources[0].ProviderID != "frontend" || path2.Resources[1].ProviderID != "checkout-service" || path2.Resources[2].ProviderID != "payments-api" {
		t.Errorf("unexpected indirect path resources: %+v", path2.Resources)
	}
}

// ---------------------------------------------------------------------------
// Test 26: Supported/Unknown state survives the boundary
// ---------------------------------------------------------------------------
func TestIntegration_SupportedUnknownStateSurvivesBoundary(t *testing.T) {
	client := startFixtureServer(t)

	svc := answer.NewService(client, nil)
	ans, err := svc.AnalyzeImpact(context.Background(), answer.ImpactRequest{
		Target: &answer.ResourceIdentity{
			Provider:     "kubernetes",
			ResourceType: "service",
			ProviderID:   "payments-api",
		},
		Direction: "incoming",
		MaxDepth:  5,
	})
	if err != nil {
		t.Fatalf("AnalyzeImpact failed: %v", err)
	}

	if len(ans.Relationships) != 2 {
		t.Fatalf("expected 2 relationships, got %d", len(ans.Relationships))
	}

	hasSupported := false
	hasUnknown := false

	for _, rel := range ans.Relationships {
		switch rel.State {
		case "supported":
			hasSupported = true
			if len(rel.EvidenceIDs) == 0 {
				t.Errorf("supported relationship has no evidence IDs: %+v", rel)
			}
		case "unknown":
			hasUnknown = true
			if len(rel.EvidenceIDs) != 0 {
				t.Errorf("unknown relationship unexpectedly has evidence IDs: %+v", rel)
			}
		default:
			t.Errorf("unexpected relationship state: %q", rel.State)
		}
	}

	if !hasSupported {
		t.Error("expected at least one 'supported' relationship across the boundary")
	}
	if !hasUnknown {
		t.Error("expected at least one 'unknown' relationship across the boundary")
	}
}
