package acceptance_test

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
// Rust Server Subprocess Harness
// ---------------------------------------------------------------------------

var (
	buildOnce sync.Once
	serverBin string
	buildErr  error
)

// findRepoRoot locates the root directory containing go.mod.
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

// ensureBinary builds wb-core-server if not already compiled.
func ensureBinary(repoRoot string) (string, error) {
	buildOnce.Do(func() {
		binPath := filepath.Join(repoRoot, "target", "debug", "wb-core-server")
		if _, err := os.Stat(binPath); err == nil {
			serverBin = binPath
			return
		}
		cmd := exec.Command("cargo", "build", "-p", "wb-core-server")
		cmd.Dir = repoRoot
		out, err := cmd.CombinedOutput()
		if err != nil {
			buildErr = fmt.Errorf("failed to build wb-core-server: %w\n%s", err, string(out))
			return
		}
		serverBin = binPath
	})
	return serverBin, buildErr
}

// allocateFreeAddr finds an available TCP port on localhost.
func allocateFreeAddr() (string, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	defer l.Close()
	return l.Addr().String(), nil
}

// ManagedServer represents a running wb-core-server subprocess.
type ManagedServer struct {
	Addr     string
	repoRoot string
	binPath  string
	cmd      *exec.Cmd
	logs     *bytes.Buffer
	exited   chan error
}

// startManagedServer spawns a new Rust server on the specified address.
func startManagedServer(t *testing.T, addr string) *ManagedServer {
	t.Helper()
	repoRoot, err := findRepoRoot()
	if err != nil {
		t.Fatalf("failed to find repo root: %v", err)
	}

	binPath, err := ensureBinary(repoRoot)
	if err != nil {
		t.Fatalf("failed to ensure binary: %v", err)
	}

	ms := &ManagedServer{
		Addr:     addr,
		repoRoot: repoRoot,
		binPath:  binPath,
		logs:     &bytes.Buffer{},
		exited:   make(chan error, 1),
	}

	ms.cmd = exec.Command(binPath)
	ms.cmd.Dir = repoRoot
	ms.cmd.Env = append(os.Environ(), "WB_CORE_GRPC_ADDR="+addr)
	ms.cmd.Stdout = ms.logs
	ms.cmd.Stderr = ms.logs

	if err := ms.cmd.Start(); err != nil {
		t.Fatalf("failed to start server subprocess: %v", err)
	}

	go func() {
		ms.exited <- ms.cmd.Wait()
	}()

	// Poll readiness: wait for TCP connectivity without fixed arbitrary sleep
	deadline := time.Now().Add(5 * time.Second)
	ready := false
	for time.Now().Before(deadline) {
		select {
		case exitErr := <-ms.exited:
			t.Fatalf("wb-core-server exited unexpectedly: %v (logs: %s)", exitErr, ms.logs.String())
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
		t.Fatalf("timed out waiting for wb-core-server to listen on %s (logs: %s)", addr, ms.logs.String())
	}

	return ms
}

// Stop cleanly terminates the server subprocess.
func (ms *ManagedServer) Stop() {
	if ms.cmd != nil && ms.cmd.Process != nil {
		_ = ms.cmd.Process.Signal(os.Interrupt)
		select {
		case <-ms.exited:
		case <-time.After(2 * time.Second):
			_ = ms.cmd.Process.Kill()
		}
	}
}

// spawnServer sets up a managed server and registers deterministic cleanup.
func spawnServer(t *testing.T) (*coreclient.Client, *ManagedServer) {
	t.Helper()
	addr, err := allocateFreeAddr()
	if err != nil {
		t.Fatalf("failed to allocate free address: %v", err)
	}

	ms := startManagedServer(t, addr)
	t.Cleanup(func() {
		ms.Stop()
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	client, err := coreclient.Connect(ctx, addr)
	if err != nil {
		t.Fatalf("failed to connect Go client to server at %s: %v", addr, err)
	}
	t.Cleanup(func() {
		_ = client.Close()
	})

	return client, ms
}

// ---------------------------------------------------------------------------
// Evidence Builders
// ---------------------------------------------------------------------------

func makeConnEvidence(id, podName, dest string, port int) *corev1.Evidence {
	data, _ := json.Marshal(map[string]interface{}{
		"destination": dest,
		"port":        port,
		"protocol":    "tcp",
	})
	return &corev1.Evidence{
		Id: id,
		Source: &corev1.EvidenceSource{
			Provider:  "kubernetes",
			Collector: "k8s-runtime",
		},
		ObservedAt:      "2026-10-04T12:00:00Z",
		ObservationType: "RUNTIME_CONNECTION",
		Subject: &corev1.ResourceIdentity{
			Provider:     "kubernetes",
			ResourceType: "pod",
			ProviderId:   "payments/" + podName,
		},
		Data: data,
	}
}

func makeMappingEvidence(id, dbName, address string, port int) *corev1.Evidence {
	data, _ := json.Marshal(map[string]interface{}{
		"address": address,
		"port":    port,
	})
	return &corev1.Evidence{
		Id: id,
		Source: &corev1.EvidenceSource{
			Provider:  "aws",
			Collector: "aws-network",
		},
		ObservedAt:      "2026-10-04T12:00:01Z",
		ObservationType: "RESOURCE_REFERENCE",
		Subject: &corev1.ResourceIdentity{
			Provider:     "aws",
			ResourceType: "rds",
			ProviderId:   fmt.Sprintf("arn:aws:rds:us-east-1:123:%s", dbName),
		},
		Data: data,
	}
}

// ===========================================================================
// TEST A — COMPLETE DISCOVERY FLOW
// ===========================================================================

func TestAcceptance_A_CompleteDiscoveryFlow(t *testing.T) {
	client, _ := spawnServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	connID := "550e8400-e29b-41d4-a716-446655440001"
	mapID := "550e8400-e29b-41d4-a716-446655440002"

	conn := makeConnEvidence(connID, "payments-api", "10.0.2.15", 5432)
	mapping := makeMappingEvidence(mapID, "payments-db", "10.0.2.15", 5432)

	resp, err := client.RunDiscoveryWithEvidence(ctx, []*corev1.Evidence{conn, mapping})
	if err != nil {
		t.Fatalf("RunDiscovery RPC failed: %v", err)
	}

	if len(resp.GetResults()) != 1 {
		t.Fatalf("expected 1 result, got %d", len(resp.GetResults()))
	}

	result := resp.GetResults()[0]
	discovered := result.GetDiscovered()
	if discovered == nil {
		t.Fatalf("expected outcome to be Discovered, got: %v", result.GetOutcome())
	}

	rel := discovered.GetRelationship()
	if rel == nil {
		t.Fatalf("expected non-nil Relationship")
	}

	// Verify complete relationship details
	src := rel.GetSource()
	if src.GetProvider() != "kubernetes" || src.GetResourceType() != "pod" || src.GetProviderId() != "payments/payments-api" {
		t.Errorf("source mismatch: got %+v", src)
	}

	tgt := rel.GetTarget()
	if tgt.GetProvider() != "aws" || tgt.GetResourceType() != "rds" || tgt.GetProviderId() != "arn:aws:rds:us-east-1:123:payments-db" {
		t.Errorf("target mismatch: got %+v", tgt)
	}

	if rel.GetKind() != "DEPENDS_ON" {
		t.Errorf("kind mismatch: expected DEPENDS_ON, got %s", rel.GetKind())
	}
	if rel.GetCategory() != "Dependency" {
		t.Errorf("category mismatch: expected Dependency, got %s", rel.GetCategory())
	}

	// Verify supporting evidence IDs
	ids := discovered.GetSupportingEvidenceIds()
	if len(ids) != 2 {
		t.Fatalf("expected 2 supporting evidence IDs, got %d", len(ids))
	}
	hasConn := false
	hasMap := false
	for _, id := range ids {
		if id == connID {
			hasConn = true
		}
		if id == mapID {
			hasMap = true
		}
	}
	if !hasConn || !hasMap {
		t.Errorf("expected IDs [%s, %s], got %v", connID, mapID, ids)
	}
}

// ===========================================================================
// TEST B — ALL DISCOVERY OUTCOMES (Discovered, Insufficient, Conflict, Invalid)
// ===========================================================================

func TestAcceptance_B_AllDiscoveryOutcomes(t *testing.T) {
	client, _ := spawnServer(t)
	ctx := context.Background()

	// 1. Discovered
	t.Run("Discovered", func(t *testing.T) {
		conn := makeConnEvidence("550e8400-e29b-41d4-a716-446655440010", "svc-a", "10.0.1.10", 8080)
		mapping := makeMappingEvidence("550e8400-e29b-41d4-a716-446655440011", "svc-b", "10.0.1.10", 8080)

		resp, err := client.RunDiscoveryWithEvidence(ctx, []*corev1.Evidence{conn, mapping})
		if err != nil {
			t.Fatalf("unexpected RPC error: %v", err)
		}
		if len(resp.GetResults()) != 1 || resp.GetResults()[0].GetDiscovered() == nil {
			t.Fatalf("expected Discovered outcome, got %v", resp.GetResults())
		}
	})

	// 2. Insufficient
	t.Run("Insufficient", func(t *testing.T) {
		conn := makeConnEvidence("550e8400-e29b-41d4-a716-446655440012", "svc-a", "10.0.1.10", 8080)
		mapping := makeMappingEvidence("550e8400-e29b-41d4-a716-446655440013", "svc-b", "10.0.1.99", 8080) // mismatch

		resp, err := client.RunDiscoveryWithEvidence(ctx, []*corev1.Evidence{conn, mapping})
		if err != nil {
			t.Fatalf("unexpected RPC error: %v", err)
		}
		if len(resp.GetResults()) != 1 || resp.GetResults()[0].GetInsufficient() == nil {
			t.Fatalf("expected Insufficient outcome, got %v", resp.GetResults())
		}
	})

	// 3. Conflict
	t.Run("Conflict", func(t *testing.T) {
		conn := makeConnEvidence("550e8400-e29b-41d4-a716-446655440014", "svc-a", "10.0.1.10", 8080)
		map1 := makeMappingEvidence("550e8400-e29b-41d4-a716-446655440015", "db-1", "10.0.1.10", 8080)
		map2 := makeMappingEvidence("550e8400-e29b-41d4-a716-446655440016", "db-2", "10.0.1.10", 8080) // conflict

		resp, err := client.RunDiscoveryWithEvidence(ctx, []*corev1.Evidence{conn, map1, map2})
		if err != nil {
			t.Fatalf("unexpected RPC error: %v", err)
		}
		if len(resp.GetResults()) != 1 {
			t.Fatalf("expected 1 result, got %d", len(resp.GetResults()))
		}
		conflict := resp.GetResults()[0].GetConflict()
		if conflict == nil {
			t.Fatalf("expected Conflict outcome, got %v", resp.GetResults()[0].GetOutcome())
		}
		if !strings.Contains(conflict.GetDescription(), "10.0.1.10") {
			t.Errorf("conflict description missing endpoint: %s", conflict.GetDescription())
		}
	})

	// 4. Domain Invalid vs Transport InvalidArgument
	t.Run("DomainInvalid_vs_TransportInvalidArgument", func(t *testing.T) {
		// LEVEL 1: Domain Invalid (Valid proto transport, but connection data missing "destination" field required by rule)
		// DiscoveryEngine produces DiscoveryResult::Invalid, returned as successful RPC with outcome = invalid
		dataWithoutDest, _ := json.Marshal(map[string]interface{}{
			"port": 5432, // missing "destination"
		})
		domainInvalidEvidence := &corev1.Evidence{
			Id: "550e8400-e29b-41d4-a716-446655440017",
			Source: &corev1.EvidenceSource{
				Provider:  "kubernetes",
				Collector: "k8s-runtime",
			},
			ObservedAt:      "2026-10-04T12:00:00Z",
			ObservationType: "RUNTIME_CONNECTION",
			Subject: &corev1.ResourceIdentity{
				Provider:     "kubernetes",
				ResourceType: "pod",
				ProviderId:   "payments/api",
			},
			Data: dataWithoutDest,
		}

		resp, err := client.RunDiscoveryWithEvidence(ctx, []*corev1.Evidence{domainInvalidEvidence})
		if err != nil {
			t.Fatalf("domain invalid should return successful RPC response, but got error: %v", err)
		}
		if len(resp.GetResults()) != 1 {
			t.Fatalf("expected 1 result, got %d", len(resp.GetResults()))
		}
		invalidOutcome := resp.GetResults()[0].GetInvalid()
		if invalidOutcome == nil {
			t.Fatalf("expected outcome = Invalid, got: %v", resp.GetResults()[0].GetOutcome())
		}
		if invalidOutcome.GetDescription() == "" {
			t.Errorf("expected non-empty description for domain invalid outcome")
		}

		// LEVEL 2: Transport InvalidArgument (Malformed UUID, not a valid proto message)
		// gRPC layer returns tonic::Status::invalid_argument error
		transportInvalidEvidence := makeConnEvidence("not-a-valid-uuid", "api", "10.0.1.10", 5432)
		_, transportErr := client.RunDiscoveryWithEvidence(ctx, []*corev1.Evidence{transportInvalidEvidence})
		if transportErr == nil {
			t.Fatal("expected transport error for malformed UUID, got nil")
		}
		if status.Code(transportErr) != codes.InvalidArgument {
			t.Errorf("expected codes.InvalidArgument, got %v", status.Code(transportErr))
		}
	})
}

// ===========================================================================
// TEST C — MULTIPLE REQUESTS ON ONE CONNECTION (Statelessness)
// ===========================================================================

func TestAcceptance_C_MultipleRequestsOneConnection(t *testing.T) {
	client, _ := spawnServer(t)
	ctx := context.Background()

	// Request 1: Discovered
	req1 := []*corev1.Evidence{
		makeConnEvidence("550e8400-e29b-41d4-a716-446655440021", "api-1", "10.0.2.1", 5432),
		makeMappingEvidence("550e8400-e29b-41d4-a716-446655440022", "db-1", "10.0.2.1", 5432),
	}
	resp1, err1 := client.RunDiscoveryWithEvidence(ctx, req1)
	if err1 != nil || len(resp1.GetResults()) != 1 || resp1.GetResults()[0].GetDiscovered() == nil {
		t.Fatalf("req1 failed: err=%v, resp=%v", err1, resp1)
	}

	// Request 2: Insufficient (different endpoint, must NOT see evidence from req1)
	req2 := []*corev1.Evidence{
		makeConnEvidence("550e8400-e29b-41d4-a716-446655440023", "api-2", "10.0.2.2", 5432),
		makeMappingEvidence("550e8400-e29b-41d4-a716-446655440024", "db-2", "10.0.2.99", 5432),
	}
	resp2, err2 := client.RunDiscoveryWithEvidence(ctx, req2)
	if err2 != nil || len(resp2.GetResults()) != 1 || resp2.GetResults()[0].GetInsufficient() == nil {
		t.Fatalf("req2 failed: err=%v, resp=%v", err2, resp2)
	}

	// Request 3: Conflict
	req3 := []*corev1.Evidence{
		makeConnEvidence("550e8400-e29b-41d4-a716-446655440025", "api-3", "10.0.2.3", 5432),
		makeMappingEvidence("550e8400-e29b-41d4-a716-446655440026", "db-3a", "10.0.2.3", 5432),
		makeMappingEvidence("550e8400-e29b-41d4-a716-446655440027", "db-3b", "10.0.2.3", 5432),
	}
	resp3, err3 := client.RunDiscoveryWithEvidence(ctx, req3)
	if err3 != nil || len(resp3.GetResults()) != 1 || resp3.GetResults()[0].GetConflict() == nil {
		t.Fatalf("req3 failed: err=%v, resp=%v", err3, resp3)
	}

	// Request 4: Another Discovered to prove independence
	req4 := []*corev1.Evidence{
		makeConnEvidence("550e8400-e29b-41d4-a716-446655440028", "api-4", "10.0.2.4", 5432),
		makeMappingEvidence("550e8400-e29b-41d4-a716-446655440029", "db-4", "10.0.2.4", 5432),
	}
	resp4, err4 := client.RunDiscoveryWithEvidence(ctx, req4)
	if err4 != nil || len(resp4.GetResults()) != 1 || resp4.GetResults()[0].GetDiscovered() == nil {
		t.Fatalf("req4 failed: err=%v, resp=%v", err4, resp4)
	}

	// Results must strictly describe req4 resources
	dr4 := resp4.GetResults()[0].GetDiscovered()
	if dr4.GetRelationship().GetSource().GetProviderId() != "payments/api-4" {
		t.Errorf("req4 source leaked previous state: %v", dr4.GetRelationship().GetSource())
	}
}

// ===========================================================================
// TEST D — MULTIPLE EVIDENCE RECORDS (Batch processing)
// ===========================================================================

func TestAcceptance_D_MultipleEvidenceRecords(t *testing.T) {
	client, _ := spawnServer(t)
	ctx := context.Background()

	// Batch of 4 evidence records describing two disjoint connections
	connA := makeConnEvidence("550e8400-e29b-41d4-a716-446655440031", "auth-api", "10.0.3.10", 6379)
	mapA := makeMappingEvidence("550e8400-e29b-41d4-a716-446655440032", "auth-cache", "10.0.3.10", 6379)

	connB := makeConnEvidence("550e8400-e29b-41d4-a716-446655440033", "order-api", "10.0.3.20", 3306)
	mapB := makeMappingEvidence("550e8400-e29b-41d4-a716-446655440034", "order-db", "10.0.3.20", 3306)

	resp, err := client.RunDiscoveryWithEvidence(ctx, []*corev1.Evidence{connA, mapA, connB, mapB})
	if err != nil {
		t.Fatalf("batch RunDiscovery failed: %v", err)
	}

	if len(resp.GetResults()) != 2 {
		t.Fatalf("expected 2 discovered relationships in batch, got %d", len(resp.GetResults()))
	}

	// Verify both relationships are discovered independently
	foundAuth := false
	foundOrder := false
	for _, res := range resp.GetResults() {
		disc := res.GetDiscovered()
		if disc == nil {
			t.Fatalf("expected discovered result, got %v", res)
		}
		rel := disc.GetRelationship()
		if rel.GetSource().GetProviderId() == "payments/auth-api" {
			foundAuth = true
			if rel.GetTarget().GetProviderId() != "arn:aws:rds:us-east-1:123:auth-cache" {
				t.Errorf("unexpected target for auth-api: %v", rel.GetTarget())
			}
			if len(disc.GetSupportingEvidenceIds()) != 2 {
				t.Errorf("unexpected supporting evidence count for auth-api: %v", disc.GetSupportingEvidenceIds())
			}
		}
		if rel.GetSource().GetProviderId() == "payments/order-api" {
			foundOrder = true
			if rel.GetTarget().GetProviderId() != "arn:aws:rds:us-east-1:123:order-db" {
				t.Errorf("unexpected target for order-api: %v", rel.GetTarget())
			}
			if len(disc.GetSupportingEvidenceIds()) != 2 {
				t.Errorf("unexpected supporting evidence count for order-api: %v", disc.GetSupportingEvidenceIds())
			}
		}
	}

	if !foundAuth || !foundOrder {
		t.Errorf("batch discovery missing results: foundAuth=%v, foundOrder=%v", foundAuth, foundOrder)
	}
}

// ===========================================================================
// TEST E — CONTEXT DEADLINE / CANCELLATION
// ===========================================================================

func TestAcceptance_E_ContextDeadlineAndCancellation(t *testing.T) {
	client, _ := spawnServer(t)

	conn := makeConnEvidence("550e8400-e29b-41d4-a716-446655440041", "api", "10.0.4.1", 5432)
	req := &corev1.RunDiscoveryRequest{Evidence: []*corev1.Evidence{conn}}

	// 1. Canceled context
	t.Run("CanceledContext", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel() // Cancel immediately

		_, err := client.RunDiscovery(ctx, req)
		if err == nil {
			t.Fatal("expected error on canceled context, got nil")
		}
		c := status.Code(err)
		if c != codes.Canceled && err != context.Canceled {
			t.Errorf("expected Canceled code or error, got: code=%v, err=%v", c, err)
		}
	})

	// 2. Expired deadline
	t.Run("ExpiredDeadline", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Nanosecond)
		defer cancel()
		time.Sleep(2 * time.Millisecond) // Ensure deadline has passed

		_, err := client.RunDiscovery(ctx, req)
		if err == nil {
			t.Fatal("expected error on expired deadline, got nil")
		}
		c := status.Code(err)
		if c != codes.DeadlineExceeded && err != context.DeadlineExceeded {
			t.Errorf("expected DeadlineExceeded code or error, got: code=%v, err=%v", c, err)
		}
	})
}

// ===========================================================================
// TEST F — SERVER UNAVAILABLE
// ===========================================================================

func TestAcceptance_F_ServerUnavailable(t *testing.T) {
	client, ms := spawnServer(t)

	// Verify server is operational first
	ctx := context.Background()
	conn := makeConnEvidence("550e8400-e29b-41d4-a716-446655440051", "api", "10.0.5.1", 5432)
	_, err := client.RunDiscoveryWithEvidence(ctx, []*corev1.Evidence{conn})
	if err != nil {
		t.Fatalf("pre-check RPC failed: %v", err)
	}

	// Now stop the server subprocess
	ms.Stop()

	// Wait briefly for socket closure to register
	time.Sleep(100 * time.Millisecond)

	// Attempt RPC against stopped server
	callCtx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	_, errAfterStop := client.RunDiscoveryWithEvidence(callCtx, []*corev1.Evidence{conn})
	if errAfterStop == nil {
		t.Fatal("expected transport error after stopping server, got nil")
	}

	c := status.Code(errAfterStop)
	if c != codes.Unavailable && c != codes.DeadlineExceeded {
		t.Errorf("expected Unavailable or DeadlineExceeded status code, got: %v (%v)", c, errAfterStop)
	}
}

// ===========================================================================
// TEST G — SERVER RESTART (Understanding Reconnection Behavior)
// ===========================================================================

func TestAcceptance_G_ServerRestart(t *testing.T) {
	addr, err := allocateFreeAddr()
	if err != nil {
		t.Fatalf("failed to allocate address: %v", err)
	}

	// 1. Start server on target address
	ms1 := startManagedServer(t, addr)

	ctx := context.Background()
	conn := makeConnEvidence("550e8400-e29b-41d4-a716-446655440061", "api", "10.0.6.1", 5432)
	mapping := makeMappingEvidence("550e8400-e29b-41d4-a716-446655440062", "db", "10.0.6.1", 5432)

	client1, err := coreclient.Connect(ctx, addr)
	if err != nil {
		t.Fatalf("connect failed: %v", err)
	}
	defer client1.Close()

	// 2. Initial RPC succeeds
	resp1, err := client1.RunDiscoveryWithEvidence(ctx, []*corev1.Evidence{conn, mapping})
	if err != nil || len(resp1.GetResults()) != 1 || resp1.GetResults()[0].GetDiscovered() == nil {
		t.Fatalf("initial RPC failed: %v", err)
	}

	// 3. Stop server
	ms1.Stop()

	// 4. Restart server on the identical target address
	ms2 := startManagedServer(t, addr)
	defer ms2.Stop()

	// 5. Connect new client or call RPC across restart
	client2, err := coreclient.Connect(ctx, addr)
	if err != nil {
		t.Fatalf("reconnect to restarted server failed: %v", err)
	}
	defer client2.Close()

	resp2, err := client2.RunDiscoveryWithEvidence(ctx, []*corev1.Evidence{conn, mapping})
	if err != nil {
		t.Fatalf("RPC after server restart failed: %v", err)
	}

	if len(resp2.GetResults()) != 1 || resp2.GetResults()[0].GetDiscovered() == nil {
		t.Fatalf("unexpected response after restart: %v", resp2)
	}
}

// ===========================================================================
// TEST H — DATA FIDELITY (Complex JSON, Unicode, Formats)
// ===========================================================================

func TestAcceptance_H_DataFidelity(t *testing.T) {
	client, _ := spawnServer(t)
	ctx := context.Background()

	evidenceID := "c0a80101-0000-4000-a000-000000000001"
	mapID := "c0a80101-0000-4000-a000-000000000002"

	// Complex nested JSON payload with unicode, numbers, boolean, nested objects and arrays
	complexData, err := json.Marshal(map[string]interface{}{
		"destination": "10.0.7.15",
		"port":        5432,
		"protocol":    "tcp",
		"metadata": map[string]interface{}{
			"cluster":    "prod-ap-south-1",
			"tier":       "payment-gateway",
			"tags":       []string{"production", "high-throughput", "pci-dss"},
			"active":     true,
			"unicode_id": "サービス-東京",
			"count":      42,
		},
	})
	if err != nil {
		t.Fatalf("failed to marshal complex JSON: %v", err)
	}

	conn := &corev1.Evidence{
		Id: evidenceID,
		Source: &corev1.EvidenceSource{
			Provider:  "kubernetes",
			Collector: "k8s-runtime",
		},
		ObservedAt:      "2026-10-04T12:34:56.789Z", // RFC 3339 with milliseconds
		ObservationType: "RUNTIME_CONNECTION",
		Subject: &corev1.ResourceIdentity{
			Provider:     "kubernetes",
			ResourceType: "pod",
			ProviderId:   "payments/prod-checkout-service-v2_0.1",
		},
		Data: complexData,
	}

	mapping := makeMappingEvidence(mapID, "rds-cluster-01", "10.0.7.15", 5432)

	resp, err := client.RunDiscoveryWithEvidence(ctx, []*corev1.Evidence{conn, mapping})
	if err != nil {
		t.Fatalf("data fidelity RunDiscovery failed: %v", err)
	}

	if len(resp.GetResults()) != 1 {
		t.Fatalf("expected 1 result, got %d", len(resp.GetResults()))
	}

	dr := resp.GetResults()[0].GetDiscovered()
	if dr == nil {
		t.Fatalf("expected Discovered result, got %v", resp.GetResults()[0].GetOutcome())
	}

	// Verify exact ID preservation
	rel := dr.GetRelationship()
	if rel.GetSource().GetProviderId() != "payments/prod-checkout-service-v2_0.1" {
		t.Errorf("subject provider_id fidelity lost: %s", rel.GetSource().GetProviderId())
	}

	supportIDs := dr.GetSupportingEvidenceIds()
	if len(supportIDs) != 2 || supportIDs[0] != evidenceID && supportIDs[1] != evidenceID {
		t.Errorf("evidence ID fidelity lost: expected %s in %v", evidenceID, supportIDs)
	}
}

// ===========================================================================
// TEST I — ERROR FIDELITY (Validation and Status Mapping)
// ===========================================================================

func TestAcceptance_I_ErrorFidelity(t *testing.T) {
	client, _ := spawnServer(t)
	ctx := context.Background()

	testCases := []struct {
		name           string
		mutator        func(*corev1.Evidence)
		expectedCode   codes.Code
		expectedSubstr string
	}{
		{
			name: "MalformedUUID",
			mutator: func(e *corev1.Evidence) {
				e.Id = "invalid-uuid-123"
			},
			expectedCode:   codes.InvalidArgument,
			expectedSubstr: "malformed evidence id",
		},
		{
			name: "MalformedTimestamp",
			mutator: func(e *corev1.Evidence) {
				e.ObservedAt = "yesterday at 3pm"
			},
			expectedCode:   codes.InvalidArgument,
			expectedSubstr: "malformed observed_at timestamp",
		},
		{
			name: "MalformedJSONBytes",
			mutator: func(e *corev1.Evidence) {
				e.Data = []byte(`{"destination": "10.0.0.1", "port": `) // unclosed JSON
			},
			expectedCode:   codes.InvalidArgument,
			expectedSubstr: "malformed evidence data JSON",
		},
		{
			name: "EmptyDataPayload",
			mutator: func(e *corev1.Evidence) {
				e.Data = []byte{}
			},
			expectedCode:   codes.InvalidArgument,
			expectedSubstr: "evidence.data cannot be empty",
		},
		{
			name: "MissingSource",
			mutator: func(e *corev1.Evidence) {
				e.Source = nil
			},
			expectedCode:   codes.InvalidArgument,
			expectedSubstr: "missing required field: evidence.source",
		},
		{
			name: "MissingSubject",
			mutator: func(e *corev1.Evidence) {
				e.Subject = nil
			},
			expectedCode:   codes.InvalidArgument,
			expectedSubstr: "missing required field: evidence.subject",
		},
		{
			name: "EmptyObservationType",
			mutator: func(e *corev1.Evidence) {
				e.ObservationType = "   "
			},
			expectedCode:   codes.InvalidArgument,
			expectedSubstr: "evidence.observation_type cannot be empty",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ev := makeConnEvidence("550e8400-e29b-41d4-a716-446655440081", "api", "10.0.8.1", 5432)
			tc.mutator(ev)

			_, err := client.RunDiscoveryWithEvidence(ctx, []*corev1.Evidence{ev})
			if err == nil {
				t.Fatalf("expected error for case %s, got nil", tc.name)
			}

			st, ok := status.FromError(err)
			if !ok {
				t.Fatalf("expected gRPC status error for case %s, got %v", tc.name, err)
			}

			if st.Code() != tc.expectedCode {
				t.Errorf("code mismatch for %s: expected %v, got %v", tc.name, tc.expectedCode, st.Code())
			}

			if !strings.Contains(st.Message(), tc.expectedSubstr) {
				t.Errorf("message mismatch for %s: expected substring %q in message %q", tc.name, tc.expectedSubstr, st.Message())
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Change-Aware Impact Analysis Boundary Test
// ---------------------------------------------------------------------------

func TestBoundary_AnalyzeImpact_ChangeAware(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	addr, err := allocateFreeAddr()
	if err != nil {
		t.Fatalf("failed to allocate free address: %v", err)
	}

	server := startManagedServer(t, addr)
	defer server.Stop()

	client, err := coreclient.Connect(ctx, addr)
	if err != nil {
		t.Fatalf("failed to connect to server: %v", err)
	}
	defer client.Close()

	workspaceID := "boundary-change-test"

	// Graph topology:
	// client-x --CALLS--> service-a
	// service-b --DEPENDS_ON--> service-a
	// service-c --DEPENDS_ON--> service-b
	// owner-dep --OWNS--> service-a
	rel1 := &corev1.Relationship{
		Source:   &corev1.ResourceIdentity{Provider: "kubernetes", ResourceType: "service", ProviderId: "client-x"},
		Target:   &corev1.ResourceIdentity{Provider: "kubernetes", ResourceType: "service", ProviderId: "service-a"},
		Kind:     "CALLS",
		Category: "Invocation",
	}
	rel2 := &corev1.Relationship{
		Source:   &corev1.ResourceIdentity{Provider: "kubernetes", ResourceType: "service", ProviderId: "service-b"},
		Target:   &corev1.ResourceIdentity{Provider: "kubernetes", ResourceType: "service", ProviderId: "service-a"},
		Kind:     "DEPENDS_ON",
		Category: "Dependency",
	}
	rel3 := &corev1.Relationship{
		Source:   &corev1.ResourceIdentity{Provider: "kubernetes", ResourceType: "service", ProviderId: "service-c"},
		Target:   &corev1.ResourceIdentity{Provider: "kubernetes", ResourceType: "service", ProviderId: "service-b"},
		Kind:     "DEPENDS_ON",
		Category: "Dependency",
	}
	rel4 := &corev1.Relationship{
		Source:   &corev1.ResourceIdentity{Provider: "kubernetes", ResourceType: "deployment", ProviderId: "owner-dep"},
		Target:   &corev1.ResourceIdentity{Provider: "kubernetes", ResourceType: "service", ProviderId: "service-a"},
		Kind:     "OWNS",
		Category: "Ownership",
	}

	_, err = client.LoadState(ctx, &corev1.LoadStateRequest{
		WorkspaceId:   workspaceID,
		Relationships: []*corev1.Relationship{rel1, rel2, rel3, rel4},
	})
	if err != nil {
		t.Fatalf("LoadState failed: %v", err)
	}

	target := &corev1.ResourceIdentity{
		Provider:     "kubernetes",
		ResourceType: "service",
		ProviderId:   "service-a",
	}

	// 1. DELETE: reaches client-x (depth 1), service-b (depth 1), service-c (depth 2)
	// owner-dep (OWNS) must NOT be traversed.
	respDel, err := client.AnalyzeImpact(ctx, &corev1.AnalyzeImpactRequest{
		Target:      target,
		Direction:   "incoming",
		MaxDepth:    5,
		WorkspaceId: workspaceID,
		ProposedChange: &corev1.ProposedChange{
			ChangeType: corev1.ChangeType_CHANGE_TYPE_DELETE,
			Details:    "deleting service-a",
		},
	})
	if err != nil {
		t.Fatalf("DELETE failed: %v", err)
	}
	if respDel.Summary.ImpactedCount != 3 {
		t.Errorf("DELETE expected 3 impacted resources, got %d", respDel.Summary.ImpactedCount)
	}
	if respDel.ChangeAssessment == nil {
		t.Fatal("DELETE expected non-nil ChangeAssessment")
	}
	if respDel.ChangeAssessment.ChangeType != corev1.ChangeType_CHANGE_TYPE_DELETE {
		t.Errorf("expected ChangeType DELETE, got %v", respDel.ChangeAssessment.ChangeType)
	}
	for _, ir := range respDel.ImpactedResources {
		if ir.Resource.ProviderId == "owner-dep" {
			t.Fatalf("OWNS boundary violated: owner-dep found in DELETE impact")
		}
	}

	// 2. UPDATE: reaches client-x (depth 1) and service-b (depth 1); service-c (depth 2) is suppressed
	respUpd, err := client.AnalyzeImpact(ctx, &corev1.AnalyzeImpactRequest{
		Target:      target,
		Direction:   "incoming",
		MaxDepth:    5,
		WorkspaceId: workspaceID,
		ProposedChange: &corev1.ProposedChange{
			ChangeType: corev1.ChangeType_CHANGE_TYPE_UPDATE,
			Details:    "updating service-a spec",
		},
	})
	if err != nil {
		t.Fatalf("UPDATE failed: %v", err)
	}
	if respUpd.Summary.ImpactedCount != 2 {
		t.Errorf("UPDATE expected 2 impacted resources (depth 1 only), got %d", respUpd.Summary.ImpactedCount)
	}
	for _, ir := range respUpd.ImpactedResources {
		if ir.Depth != 1 {
			t.Errorf("UPDATE expected depth 1 only, got depth %d for %s", ir.Depth, ir.Resource.ProviderId)
		}
	}

	// 3. SCALE: reaches ONLY client-x (CALLS edge); service-b (DEPENDS_ON) suppressed
	respScale, err := client.AnalyzeImpact(ctx, &corev1.AnalyzeImpactRequest{
		Target:      target,
		Direction:   "incoming",
		MaxDepth:    5,
		WorkspaceId: workspaceID,
		ProposedChange: &corev1.ProposedChange{
			ChangeType: corev1.ChangeType_CHANGE_TYPE_SCALE,
			Details:    "scaling service-a replicas",
		},
	})
	if err != nil {
		t.Fatalf("SCALE failed: %v", err)
	}
	if respScale.Summary.ImpactedCount != 1 {
		t.Errorf("SCALE expected 1 impacted resource (CALLS only), got %d", respScale.Summary.ImpactedCount)
	}
	if len(respScale.ImpactedResources) > 0 && respScale.ImpactedResources[0].Resource.ProviderId != "client-x" {
		t.Errorf("SCALE expected client-x, got %s", respScale.ImpactedResources[0].Resource.ProviderId)
	}

	// 4. REPLACE: reaches client-x (depth 1) and service-b (depth 1); service-c (depth 2) suppressed
	respRepl, err := client.AnalyzeImpact(ctx, &corev1.AnalyzeImpactRequest{
		Target:      target,
		Direction:   "incoming",
		MaxDepth:    5,
		WorkspaceId: workspaceID,
		ProposedChange: &corev1.ProposedChange{
			ChangeType: corev1.ChangeType_CHANGE_TYPE_REPLACE,
			Details:    "recreating instance",
		},
	})
	if err != nil {
		t.Fatalf("REPLACE failed: %v", err)
	}
	if respRepl.Summary.ImpactedCount != 2 {
		t.Errorf("REPLACE expected 2 impacted resources, got %d", respRepl.Summary.ImpactedCount)
	}

	// 5. Unspecified ChangeType: rejected with InvalidArgument
	_, err = client.AnalyzeImpact(ctx, &corev1.AnalyzeImpactRequest{
		Target:      target,
		Direction:   "incoming",
		MaxDepth:    5,
		WorkspaceId: workspaceID,
		ProposedChange: &corev1.ProposedChange{
			ChangeType: corev1.ChangeType_CHANGE_TYPE_UNSPECIFIED,
		},
	})
	if err == nil {
		t.Fatalf("expected error for unspecified ChangeType, got nil")
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.InvalidArgument {
		t.Errorf("expected InvalidArgument for unspecified ChangeType, got %v", err)
	}

	// 6. Unsupported ChangeType value: rejected with InvalidArgument
	_, err = client.AnalyzeImpact(ctx, &corev1.AnalyzeImpactRequest{
		Target:      target,
		Direction:   "incoming",
		MaxDepth:    5,
		WorkspaceId: workspaceID,
		ProposedChange: &corev1.ProposedChange{
			ChangeType: 999,
		},
	})
	if err == nil {
		t.Fatalf("expected error for unsupported ChangeType, got nil")
	}
	st, ok = status.FromError(err)
	if !ok || st.Code() != codes.InvalidArgument {
		t.Errorf("expected InvalidArgument for unsupported ChangeType, got %v", err)
	}

	// 7. Legacy request (proposed_change: nil) preserves legacy multi-hop and empty impact_type
	respLegacy, err := client.AnalyzeImpact(ctx, &corev1.AnalyzeImpactRequest{
		Target:      target,
		Direction:   "incoming",
		MaxDepth:    5,
		WorkspaceId: workspaceID,
	})
	if err != nil {
		t.Fatalf("legacy request failed: %v", err)
	}
	if respLegacy.Summary.ImpactedCount != 3 {
		t.Errorf("legacy request expected 3 impacted resources, got %d", respLegacy.Summary.ImpactedCount)
	}
	if respLegacy.ChangeAssessment != nil {
		t.Errorf("legacy request expected nil ChangeAssessment, got %+v", respLegacy.ChangeAssessment)
	}
	for _, ir := range respLegacy.ImpactedResources {
		if ir.ImpactType != "" {
			t.Errorf("legacy request expected empty ImpactType, got %q", ir.ImpactType)
		}
	}

	// 8. SCALE on service-b (has only incoming DEPENDS_ON from service-c, no CALLS) -> 0 impacts + explicit limitation
	targetB := &corev1.ResourceIdentity{
		Provider:     "kubernetes",
		ResourceType: "service",
		ProviderId:   "service-b",
	}
	respScaleB, err := client.AnalyzeImpact(ctx, &corev1.AnalyzeImpactRequest{
		Target:      targetB,
		Direction:   "incoming",
		MaxDepth:    5,
		WorkspaceId: workspaceID,
		ProposedChange: &corev1.ProposedChange{
			ChangeType: corev1.ChangeType_CHANGE_TYPE_SCALE,
		},
	})
	if err != nil {
		t.Fatalf("SCALE on service-b failed: %v", err)
	}
	if respScaleB.Summary.ImpactedCount != 0 {
		t.Errorf("SCALE on service-b expected 0 impacts, got %d", respScaleB.Summary.ImpactedCount)
	}
	if respScaleB.ChangeAssessment == nil {
		t.Fatal("expected non-nil ChangeAssessment for SCALE on service-b")
	}
	hasDepExplanation := false
	for _, lim := range respScaleB.ChangeAssessment.Limitations {
		if strings.Contains(lim, "declarative dependencies (e.g. DEPENDS_ON)") && strings.Contains(lim, "no runtime CALLS relationships were observed") {
			hasDepExplanation = true
			break
		}
	}
	if !hasDepExplanation {
		t.Errorf("expected limitation explaining DEPENDS_ON exclusion, got: %+v", respScaleB.ChangeAssessment.Limitations)
	}
}
