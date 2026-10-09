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

// ---------------------------------------------------------------------------
// Change-Aware Impact Analysis Integration Test: DELETE vs UPDATE vs SCALE
// ---------------------------------------------------------------------------
func TestIntegration_ChangeAwareEndToEnd_DeleteVsUpdateVsScale(t *testing.T) {
	client := startFixtureServer(t)

	svc := answer.NewService(client, nil)
	h := answer.NewHandler(svc)

	target := map[string]interface{}{
		"provider":      "kubernetes",
		"resource_type": "service",
		"provider_id":   "payments-api",
	}

	// 1. Proposed Change: DELETE -> multi-hop reaching checkout-service and frontend
	deletePayload, _ := json.Marshal(map[string]interface{}{
		"target":    target,
		"direction": "incoming",
		"max_depth": 5,
		"proposed_change": map[string]interface{}{
			"change_type": "DELETE",
			"details":     "deleting payments-api deployment",
		},
	})

	reqDelete := httptest.NewRequest(http.MethodPost, "/api/v1/impact", bytes.NewReader(deletePayload))
	rrDelete := httptest.NewRecorder()
	h.ServeHTTP(rrDelete, reqDelete)

	if rrDelete.Code != http.StatusOK {
		t.Fatalf("DELETE expected 200 OK, got %d: %s", rrDelete.Code, rrDelete.Body.String())
	}

	var ansDelete answer.ImpactAnswer
	if err := json.Unmarshal(rrDelete.Body.Bytes(), &ansDelete); err != nil {
		t.Fatalf("failed to decode DELETE response: %v", err)
	}

	if ansDelete.Summary.ImpactedCount != 2 {
		t.Errorf("DELETE expected 2 impacted resources, got %d", ansDelete.Summary.ImpactedCount)
	}
	if ansDelete.ChangeAssessment == nil {
		t.Fatal("DELETE expected non-nil ChangeAssessment")
	}
	if ansDelete.ChangeAssessment.ChangeType != answer.ChangeTypeDelete {
		t.Errorf("expected ChangeType DELETE, got %s", ansDelete.ChangeAssessment.ChangeType)
	}
	if ansDelete.ChangeAssessment.ImpactNature != "POTENTIAL_IMPACT" {
		t.Errorf("expected POTENTIAL_IMPACT, got %s", ansDelete.ChangeAssessment.ImpactNature)
	}
	// Check direct vs indirect classifications
	if ansDelete.ImpactedResources[0].ImpactType != "DIRECT" {
		t.Errorf("expected first impacted resource DIRECT, got %s", ansDelete.ImpactedResources[0].ImpactType)
	}
	if ansDelete.ImpactedResources[1].ImpactType != "INDIRECT" {
		t.Errorf("expected second impacted resource INDIRECT, got %s", ansDelete.ImpactedResources[1].ImpactType)
	}

	// 2. Proposed Change: UPDATE -> depth 1 only (checkout-service only)
	updatePayload, _ := json.Marshal(map[string]interface{}{
		"target":    target,
		"direction": "incoming",
		"max_depth": 5,
		"proposed_change": map[string]interface{}{
			"change_type": "UPDATE",
			"details":     "updating service port",
		},
	})

	reqUpdate := httptest.NewRequest(http.MethodPost, "/api/v1/impact", bytes.NewReader(updatePayload))
	rrUpdate := httptest.NewRecorder()
	h.ServeHTTP(rrUpdate, reqUpdate)

	if rrUpdate.Code != http.StatusOK {
		t.Fatalf("UPDATE expected 200 OK, got %d: %s", rrUpdate.Code, rrUpdate.Body.String())
	}

	var ansUpdate answer.ImpactAnswer
	if err := json.Unmarshal(rrUpdate.Body.Bytes(), &ansUpdate); err != nil {
		t.Fatalf("failed to decode UPDATE response: %v", err)
	}

	if ansUpdate.Summary.ImpactedCount != 1 {
		t.Errorf("UPDATE expected 1 impacted resource (depth 1 only), got %d", ansUpdate.Summary.ImpactedCount)
	}
	if ansUpdate.ImpactedResources[0].Resource.ProviderID != "checkout-service" {
		t.Errorf("UPDATE expected checkout-service, got %s", ansUpdate.ImpactedResources[0].Resource.ProviderID)
	}
	if ansUpdate.ImpactedResources[0].ImpactType != "DIRECT" {
		t.Errorf("expected DIRECT impact_type, got %s", ansUpdate.ImpactedResources[0].ImpactType)
	}

	// 3. Proposed Change: SCALE -> 0 impacted because relationships are DEPENDS_ON, not CALLS
	scalePayload, _ := json.Marshal(map[string]interface{}{
		"target":    target,
		"direction": "incoming",
		"max_depth": 5,
		"proposed_change": map[string]interface{}{
			"change_type": "SCALE",
			"details":     "scaling down replicas",
		},
	})

	reqScale := httptest.NewRequest(http.MethodPost, "/api/v1/impact", bytes.NewReader(scalePayload))
	rrScale := httptest.NewRecorder()
	h.ServeHTTP(rrScale, reqScale)

	if rrScale.Code != http.StatusOK {
		t.Fatalf("SCALE expected 200 OK, got %d: %s", rrScale.Code, rrScale.Body.String())
	}

	var ansScale answer.ImpactAnswer
	if err := json.Unmarshal(rrScale.Body.Bytes(), &ansScale); err != nil {
		t.Fatalf("failed to decode SCALE response: %v", err)
	}

	if ansScale.Summary.ImpactedCount != 0 {
		t.Errorf("SCALE expected 0 impacted resources (DEPENDS_ON excluded), got %d", ansScale.Summary.ImpactedCount)
	}
	if ansScale.ChangeAssessment == nil {
		t.Fatal("SCALE expected non-nil ChangeAssessment")
	}
	if ansScale.ChangeAssessment.ChangeType != answer.ChangeTypeScale {
		t.Errorf("expected ChangeType SCALE, got %s", ansScale.ChangeAssessment.ChangeType)
	}
	if ansScale.ChangeAssessment.ImpactNature != "POTENTIAL_IMPACT" {
		t.Errorf("expected POTENTIAL_IMPACT, got %s", ansScale.ChangeAssessment.ImpactNature)
	}
	hasDepExplanation := false
	for _, lim := range ansScale.ChangeAssessment.Limitations {
		if strings.Contains(lim, "declarative dependencies (e.g. DEPENDS_ON)") && strings.Contains(lim, "no runtime CALLS relationships were observed") {
			hasDepExplanation = true
			break
		}
	}
	if !hasDepExplanation {
		t.Errorf("SCALE limitations must explain DEPENDS_ON exclusion, got: %+v", ansScale.ChangeAssessment.Limitations)
	}

	// 4. Proposed Change: REPLACE -> depth 1 only (checkout-service only), frontend suppressed
	replacePayload, _ := json.Marshal(map[string]interface{}{
		"target":    target,
		"direction": "incoming",
		"max_depth": 5,
		"proposed_change": map[string]interface{}{
			"change_type": "REPLACE",
			"details":     "rolling replacement of payments-api deployment",
		},
	})

	reqReplace := httptest.NewRequest(http.MethodPost, "/api/v1/impact", bytes.NewReader(replacePayload))
	rrReplace := httptest.NewRecorder()
	h.ServeHTTP(rrReplace, reqReplace)

	if rrReplace.Code != http.StatusOK {
		t.Fatalf("REPLACE expected 200 OK, got %d: %s", rrReplace.Code, rrReplace.Body.String())
	}

	var ansReplace answer.ImpactAnswer
	if err := json.Unmarshal(rrReplace.Body.Bytes(), &ansReplace); err != nil {
		t.Fatalf("failed to decode REPLACE response: %v", err)
	}

	if ansReplace.Summary.ImpactedCount != 1 {
		t.Errorf("REPLACE expected 1 impacted resource (depth 1 only), got %d", ansReplace.Summary.ImpactedCount)
	}
	if ansReplace.ImpactedResources[0].Resource.ProviderID != "checkout-service" {
		t.Errorf("REPLACE expected checkout-service, got %s", ansReplace.ImpactedResources[0].Resource.ProviderID)
	}
	if ansReplace.ImpactedResources[0].ImpactType != "DIRECT" {
		t.Errorf("expected DIRECT impact_type, got %s", ansReplace.ImpactedResources[0].ImpactType)
	}
	if !strings.Contains(ansReplace.ImpactedResources[0].ImpactReason, "transient rollover or reconnection") {
		t.Errorf("expected rollover explanation, got %q", ansReplace.ImpactedResources[0].ImpactReason)
	}
	if ansReplace.ChangeAssessment == nil || ansReplace.ChangeAssessment.ChangeType != answer.ChangeTypeReplace {
		t.Errorf("expected ChangeAssessment REPLACE, got %+v", ansReplace.ChangeAssessment)
	}
	if ansReplace.ChangeAssessment.ImpactNature != "POTENTIAL_IMPACT" {
		t.Errorf("expected POTENTIAL_IMPACT, got %s", ansReplace.ChangeAssessment.ImpactNature)
	}
}

// ---------------------------------------------------------------------------
// Backward Compatibility End-to-End Tests: omitted, null, and empty proposed_change
// ---------------------------------------------------------------------------
func TestIntegration_ChangeAware_BackwardCompatibilityEndToEnd(t *testing.T) {
	client := startFixtureServer(t)
	svc := answer.NewService(client, nil)
	h := answer.NewHandler(svc)

	target := map[string]interface{}{
		"provider":      "kubernetes",
		"resource_type": "service",
		"provider_id":   "payments-api",
	}

	cases := []struct {
		name    string
		payload []byte
	}{
		{
			name: "proposed_change omitted",
			payload: func() []byte {
				b, _ := json.Marshal(map[string]interface{}{
					"target":    target,
					"direction": "incoming",
					"max_depth": 5,
				})
				return b
			}(),
		},
		{
			name: "proposed_change null",
			payload: func() []byte {
				b, _ := json.Marshal(map[string]interface{}{
					"target":          target,
					"direction":       "incoming",
					"max_depth":       5,
					"proposed_change": nil,
				})
				return b
			}(),
		},
		{
			name: "proposed_change empty object",
			payload: func() []byte {
				b, _ := json.Marshal(map[string]interface{}{
					"target":          target,
					"direction":       "incoming",
					"max_depth":       5,
					"proposed_change": map[string]interface{}{},
				})
				return b
			}(),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/v1/impact", bytes.NewReader(tc.payload))
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, req)

			if rr.Code != http.StatusOK {
				t.Fatalf("expected 200 OK, got %d: %s", rr.Code, rr.Body.String())
			}

			var ans answer.ImpactAnswer
			if err := json.Unmarshal(rr.Body.Bytes(), &ans); err != nil {
				t.Fatalf("failed to decode response: %v", err)
			}

			// Legacy analysis finds both checkout-service and frontend (depth 1 and 2)
			if ans.Summary.ImpactedCount != 2 {
				t.Errorf("expected 2 impacted resources in legacy analysis, got %d", ans.Summary.ImpactedCount)
			}
			if ans.ChangeAssessment != nil {
				t.Errorf("legacy response must not include ChangeAssessment, got %+v", ans.ChangeAssessment)
			}
			for i, ir := range ans.ImpactedResources {
				if ir.ImpactType != "" {
					t.Errorf("resource %d expected empty ImpactType in legacy mode, got %q", i, ir.ImpactType)
				}
				if ir.ImpactReason != "" {
					t.Errorf("resource %d expected empty ImpactReason in legacy mode, got %q", i, ir.ImpactReason)
				}
			}

			// Raw JSON check: omitempty must suppress change_assessment and impact_type
			bodyStr := rr.Body.String()
			if strings.Contains(bodyStr, `"change_assessment"`) {
				t.Errorf("raw JSON must not contain change_assessment in legacy mode: %s", bodyStr)
			}
			if strings.Contains(bodyStr, `"impact_type"`) {
				t.Errorf("raw JSON must not contain impact_type in legacy mode: %s", bodyStr)
			}
		})
	}
}
