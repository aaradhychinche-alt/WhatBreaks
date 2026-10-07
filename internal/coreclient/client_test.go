package coreclient_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "github.com/aaradhychinche-alt/WhatBreaks/gen/go/wb/core/v1"
	"github.com/aaradhychinche-alt/WhatBreaks/internal/coreclient"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ---------------------------------------------------------------------------
// Subprocess Server Management for Integration Testing
// ---------------------------------------------------------------------------

var (
	buildOnce sync.Once
	builtBin  string
	buildErr  error
)

// findRepoRoot searches parent directories for go.mod to locate the repo root.
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
			return "", fmt.Errorf("repository root containing go.mod not found")
		}
		dir = parent
	}
}

// ensureRustServerBinary ensures wb-core-server is compiled and returns its path.
func ensureRustServerBinary(repoRoot string) (string, error) {
	buildOnce.Do(func() {
		binPath := filepath.Join(repoRoot, "target", "debug", "wb-core-server")
		if _, err := os.Stat(binPath); err == nil {
			builtBin = binPath
			return
		}

		cmd := exec.Command("cargo", "build", "-p", "wb-core-server")
		cmd.Dir = repoRoot
		out, err := cmd.CombinedOutput()
		if err != nil {
			buildErr = fmt.Errorf("failed to build wb-core-server: %w (output: %s)", err, string(out))
			return
		}
		builtBin = binPath
	})
	return builtBin, buildErr
}

// getFreeLocalAddr binds to an ephemeral localhost port to allocate a free address.
func getFreeLocalAddr() (string, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	defer l.Close()
	return l.Addr().String(), nil
}

// startRustServer starts the real Rust wb-core-server as a standalone subprocess
// listening on a dynamic ephemeral port.
func startRustServer(t *testing.T) (*coreclient.Client, string) {
	t.Helper()

	repoRoot, err := findRepoRoot()
	if err != nil {
		t.Fatalf("could not find repo root: %v", err)
	}

	binPath, err := ensureRustServerBinary(repoRoot)
	if err != nil {
		t.Fatalf("could not ensure rust server binary: %v", err)
	}

	addr, err := getFreeLocalAddr()
	if err != nil {
		t.Fatalf("could not allocate free local addr: %v", err)
	}

	var logs bytes.Buffer
	cmd := exec.Command(binPath)
	cmd.Dir = repoRoot
	cmd.Env = append(os.Environ(), "WB_CORE_GRPC_ADDR="+addr)
	cmd.Stdout = &logs
	cmd.Stderr = &logs

	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start wb-core-server process: %v", err)
	}

	exited := make(chan error, 1)
	go func() {
		exited <- cmd.Wait()
	}()

	// Ensure clean process cleanup when the test completes
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

	// Poll readiness without fixed sleeps: wait for TCP connectivity, or fail if server crashes
	readyDeadline := time.Now().Add(5 * time.Second)
	ready := false
	for time.Now().Before(readyDeadline) {
		select {
		case exitErr := <-exited:
			t.Fatalf("wb-core-server exited unexpectedly: %v (logs: %s)", exitErr, logs.String())
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
		t.Fatalf("timed out waiting for wb-core-server on %s (logs: %s)", addr, logs.String())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	client, err := coreclient.Connect(ctx, addr)
	if err != nil {
		t.Fatalf("failed to connect to wb-core-server on %s: %v", addr, err)
	}
	t.Cleanup(func() {
		_ = client.Close()
	})

	return client, addr
}

// ---------------------------------------------------------------------------
// Protobuf Test Helpers
// ---------------------------------------------------------------------------

func k8sSource() *corev1.EvidenceSource {
	return &corev1.EvidenceSource{
		Provider:  "kubernetes",
		Collector: "k8s-runtime",
	}
}

func awsSource() *corev1.EvidenceSource {
	return &corev1.EvidenceSource{
		Provider:  "aws",
		Collector: "aws-network",
	}
}

func podSubject(name string) *corev1.ResourceIdentity {
	return &corev1.ResourceIdentity{
		Provider:     "kubernetes",
		ResourceType: "pod",
		ProviderId:   fmt.Sprintf("payments/%s", name),
	}
}

func dbSubject(name string) *corev1.ResourceIdentity {
	return &corev1.ResourceIdentity{
		Provider:     "aws",
		ResourceType: "rds",
		ProviderId:   fmt.Sprintf("arn:aws:rds:us-east-1:123:%s", name),
	}
}

func makeConnectionEvidence(id string, subject *corev1.ResourceIdentity, dest string, port int) *corev1.Evidence {
	data, _ := json.Marshal(map[string]interface{}{
		"destination": dest,
		"port":        port,
		"protocol":    "tcp",
	})
	return &corev1.Evidence{
		Id:              id,
		Source:          k8sSource(),
		ObservedAt:      "2026-10-04T12:00:00Z",
		ObservationType: "RUNTIME_CONNECTION",
		Subject:         subject,
		Data:            data,
	}
}

func makeMappingEvidence(id string, subject *corev1.ResourceIdentity, address string, port int) *corev1.Evidence {
	data, _ := json.Marshal(map[string]interface{}{
		"address": address,
		"port":    port,
	})
	return &corev1.Evidence{
		Id:              id,
		Source:          awsSource(),
		ObservedAt:      "2026-10-04T12:00:01Z",
		ObservationType: "RESOURCE_REFERENCE",
		Subject:         subject,
		Data:            data,
	}
}

// ---------------------------------------------------------------------------
// Test A — Discovered
// ---------------------------------------------------------------------------

func TestRunDiscovery_Discovered(t *testing.T) {
	client, _ := startRustServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	connID := "550e8400-e29b-41d4-a716-446655440001"
	mapID := "550e8400-e29b-41d4-a716-446655440002"

	conn := makeConnectionEvidence(connID, podSubject("payments-api"), "10.0.2.15", 5432)
	mapping := makeMappingEvidence(mapID, dbSubject("payments-db"), "10.0.2.15", 5432)

	resp, err := client.RunDiscoveryWithEvidence(ctx, []*corev1.Evidence{conn, mapping})
	if err != nil {
		t.Fatalf("RunDiscovery failed: %v", err)
	}

	if len(resp.GetResults()) != 1 {
		t.Fatalf("expected 1 result, got %d", len(resp.GetResults()))
	}

	result := resp.GetResults()[0]
	discovered := result.GetDiscovered()
	if discovered == nil {
		t.Fatalf("expected Discovered outcome, got outcome: %v", result.GetOutcome())
	}

	rel := discovered.GetRelationship()
	if rel == nil {
		t.Fatalf("expected non-nil Relationship in Discovered outcome")
	}

	// Verify source identity
	src := rel.GetSource()
	if src == nil || src.GetProvider() != "kubernetes" || src.GetResourceType() != "pod" || src.GetProviderId() != "payments/payments-api" {
		t.Errorf("unexpected source identity: %+v", src)
	}

	// Verify target identity
	tgt := rel.GetTarget()
	if tgt == nil || tgt.GetProvider() != "aws" || tgt.GetResourceType() != "rds" || tgt.GetProviderId() != "arn:aws:rds:us-east-1:123:payments-db" {
		t.Errorf("unexpected target identity: %+v", tgt)
	}

	// Verify relationship kind & category
	if rel.GetKind() != "DEPENDS_ON" {
		t.Errorf("expected kind DEPENDS_ON, got %q", rel.GetKind())
	}
	if rel.GetCategory() != "Dependency" {
		t.Errorf("expected category Dependency, got %q", rel.GetCategory())
	}

	// Verify supporting evidence IDs
	supportIDs := discovered.GetSupportingEvidenceIds()
	if len(supportIDs) != 2 {
		t.Errorf("expected 2 supporting evidence IDs, got %d (%v)", len(supportIDs), supportIDs)
	}

	hasConn := false
	hasMap := false
	for _, id := range supportIDs {
		if id == connID {
			hasConn = true
		}
		if id == mapID {
			hasMap = true
		}
	}
	if !hasConn || !hasMap {
		t.Errorf("missing expected supporting evidence IDs: conn=%v, map=%v; got %v", hasConn, hasMap, supportIDs)
	}
}

// ---------------------------------------------------------------------------
// Test B — Insufficient
// ---------------------------------------------------------------------------

func TestRunDiscovery_Insufficient(t *testing.T) {
	client, _ := startRustServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Endpoints do not match (10.0.2.15 vs 10.0.2.99)
	conn := makeConnectionEvidence("550e8400-e29b-41d4-a716-446655440010", podSubject("payments-api"), "10.0.2.15", 5432)
	mapping := makeMappingEvidence("550e8400-e29b-41d4-a716-446655440011", dbSubject("payments-db"), "10.0.2.99", 5432)

	resp, err := client.RunDiscoveryWithEvidence(ctx, []*corev1.Evidence{conn, mapping})
	if err != nil {
		t.Fatalf("RunDiscovery failed: %v", err)
	}

	if len(resp.GetResults()) != 1 {
		t.Fatalf("expected 1 result, got %d", len(resp.GetResults()))
	}

	if resp.GetResults()[0].GetInsufficient() == nil {
		t.Fatalf("expected Insufficient outcome, got %v", resp.GetResults()[0].GetOutcome())
	}
}

// ---------------------------------------------------------------------------
// Test C — Conflict
// ---------------------------------------------------------------------------

func TestRunDiscovery_Conflict(t *testing.T) {
	client, _ := startRustServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Two database mappings claim the identical endpoint
	conn := makeConnectionEvidence("550e8400-e29b-41d4-a716-446655440020", podSubject("payments-api"), "10.0.2.15", 5432)
	mapB := makeMappingEvidence("550e8400-e29b-41d4-a716-446655440021", dbSubject("database-b"), "10.0.2.15", 5432)
	mapC := makeMappingEvidence("550e8400-e29b-41d4-a716-446655440022", dbSubject("database-c"), "10.0.2.15", 5432)

	resp, err := client.RunDiscoveryWithEvidence(ctx, []*corev1.Evidence{conn, mapB, mapC})
	if err != nil {
		t.Fatalf("RunDiscovery failed: %v", err)
	}

	if len(resp.GetResults()) != 1 {
		t.Fatalf("expected 1 result, got %d", len(resp.GetResults()))
	}

	conflict := resp.GetResults()[0].GetConflict()
	if conflict == nil {
		t.Fatalf("expected Conflict outcome, got %v", resp.GetResults()[0].GetOutcome())
	}

	desc := conflict.GetDescription()
	if !strings.Contains(desc, "10.0.2.15") || !strings.Contains(desc, "5432") {
		t.Errorf("conflict description missing endpoint info: %q", desc)
	}
}

// ---------------------------------------------------------------------------
// Test D — Rust Validation Error (codes.InvalidArgument)
// ---------------------------------------------------------------------------

func TestRunDiscovery_ValidationError_MalformedUUID(t *testing.T) {
	client, _ := startRustServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	badEvidence := makeConnectionEvidence("not-a-valid-uuid", podSubject("payments-api"), "10.0.2.15", 5432)

	_, err := client.RunDiscoveryWithEvidence(ctx, []*corev1.Evidence{badEvidence})
	if err == nil {
		t.Fatal("expected error for malformed UUID, got nil")
	}

	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %T: %v", err, err)
	}

	if st.Code() != codes.InvalidArgument {
		t.Errorf("expected code InvalidArgument, got %v (message: %s)", st.Code(), st.Message())
	}
	if !strings.Contains(st.Message(), "malformed evidence id") {
		t.Errorf("unexpected error message: %s", st.Message())
	}
}

func TestRunDiscovery_ValidationError_MalformedTimestamp(t *testing.T) {
	client, _ := startRustServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	badEvidence := makeConnectionEvidence("550e8400-e29b-41d4-a716-446655440001", podSubject("payments-api"), "10.0.2.15", 5432)
	badEvidence.ObservedAt = "not-a-timestamp"

	_, err := client.RunDiscoveryWithEvidence(ctx, []*corev1.Evidence{badEvidence})
	if err == nil {
		t.Fatal("expected error for malformed timestamp, got nil")
	}

	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.InvalidArgument {
		t.Errorf("expected InvalidArgument, got code=%v, err=%v", st.Code(), err)
	}
	if !strings.Contains(st.Message(), "malformed observed_at timestamp") {
		t.Errorf("unexpected error message: %s", st.Message())
	}
}

func TestRunDiscovery_ValidationError_MalformedJSON(t *testing.T) {
	client, _ := startRustServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	badEvidence := makeConnectionEvidence("550e8400-e29b-41d4-a716-446655440001", podSubject("payments-api"), "10.0.2.15", 5432)
	badEvidence.Data = []byte("not-valid-json{{")

	_, err := client.RunDiscoveryWithEvidence(ctx, []*corev1.Evidence{badEvidence})
	if err == nil {
		t.Fatal("expected error for malformed JSON, got nil")
	}

	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.InvalidArgument {
		t.Errorf("expected InvalidArgument, got code=%v, err=%v", st.Code(), err)
	}
}

func TestRunDiscovery_ValidationError_MissingSource(t *testing.T) {
	client, _ := startRustServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	badEvidence := makeConnectionEvidence("550e8400-e29b-41d4-a716-446655440001", podSubject("payments-api"), "10.0.2.15", 5432)
	badEvidence.Source = nil

	_, err := client.RunDiscoveryWithEvidence(ctx, []*corev1.Evidence{badEvidence})
	if err == nil {
		t.Fatal("expected error for missing source, got nil")
	}

	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.InvalidArgument {
		t.Errorf("expected InvalidArgument, got code=%v, err=%v", st.Code(), err)
	}
}

// ---------------------------------------------------------------------------
// Client Edge Cases & Lifecycle Tests
// ---------------------------------------------------------------------------

func TestClient_NilRequest(t *testing.T) {
	client, _ := startRustServer(t)

	ctx := context.Background()
	_, err := client.RunDiscovery(ctx, nil)
	if err == nil {
		t.Fatal("expected error for nil request")
	}

	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.InvalidArgument {
		t.Errorf("expected InvalidArgument for nil request, got %v", err)
	}
}

func TestClient_AccessorsAndClose(t *testing.T) {
	client, _ := startRustServer(t)

	if client.Conn() == nil {
		t.Error("expected non-nil *grpc.ClientConn from client.Conn()")
	}
	if client.DiscoveryServiceClient() == nil {
		t.Error("expected non-nil DiscoveryServiceClient interface")
	}
	if client.AnswerServiceClient() == nil {
		t.Error("expected non-nil AnswerServiceClient interface")
	}

	if err := client.Close(); err != nil {
		t.Errorf("unexpected error on client.Close(): %v", err)
	}
}

func TestClient_AnalyzeImpact_NilRequest(t *testing.T) {
	client, _ := startRustServer(t)

	ctx := context.Background()
	_, err := client.AnalyzeImpact(ctx, nil)
	if err == nil {
		t.Fatal("expected error for nil request")
	}

	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.InvalidArgument {
		t.Errorf("expected InvalidArgument for nil request, got %v", err)
	}
}

func TestClient_AnalyzeImpact_EmptyGraphSuccess(t *testing.T) {
	client, _ := startRustServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req := &corev1.AnalyzeImpactRequest{
		Target:    podSubject("payments-api"),
		Direction: "incoming",
		MaxDepth:  5,
	}

	resp, err := client.AnalyzeImpact(ctx, req)
	if err != nil {
		t.Fatalf("AnalyzeImpact failed: %v", err)
	}

	if resp.GetTarget() == nil {
		t.Fatal("expected non-nil target in response")
	}
	if resp.GetTarget().GetProviderId() != "payments/payments-api" {
		t.Errorf("expected target payments/payments-api, got %s", resp.GetTarget().GetProviderId())
	}
	if resp.GetSummary() == nil {
		t.Fatal("expected non-nil summary in response")
	}
	if resp.GetSummary().GetImpactedCount() != 0 {
		t.Errorf("expected 0 impacted, got %d", resp.GetSummary().GetImpactedCount())
	}
}

func TestClient_LoadState_NilRequest(t *testing.T) {
	client, _ := startRustServer(t)

	ctx := context.Background()
	_, err := client.LoadState(ctx, nil)
	if err == nil {
		t.Fatal("expected error for nil request")
	}

	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.InvalidArgument {
		t.Errorf("expected InvalidArgument for nil request, got %v", err)
	}
}

func TestClient_LoadState_Roundtrip(t *testing.T) {
	client, _ := startRustServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req := &corev1.LoadStateRequest{
		WorkspaceId: "ws-test",
		Relationships: []*corev1.Relationship{
			{
				Source:   podSubject("orders"),
				Target:   podSubject("catalog"),
				Kind:     "DEPENDS_ON",
				Category: "Dependency",
			},
		},
	}

	resp, err := client.LoadState(ctx, req)
	if err != nil {
		t.Fatalf("LoadState failed: %v", err)
	}

	if resp.GetWorkspaceId() != "ws-test" {
		t.Errorf("expected workspace_id ws-test, got %s", resp.GetWorkspaceId())
	}
	if resp.GetRelationshipsLoaded() != 1 {
		t.Errorf("expected 1 relationship loaded, got %d", resp.GetRelationshipsLoaded())
	}
}
