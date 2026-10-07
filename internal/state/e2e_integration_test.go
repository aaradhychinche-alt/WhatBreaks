package state_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	corev1 "github.com/aaradhychinche-alt/WhatBreaks/gen/go/wb/core/v1"
	"github.com/aaradhychinche-alt/WhatBreaks/internal/answer"
	"github.com/aaradhychinche-alt/WhatBreaks/internal/coreclient"
	"github.com/aaradhychinche-alt/WhatBreaks/internal/logging"
	"github.com/aaradhychinche-alt/WhatBreaks/internal/state"
)

var (
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

func startProdServer(t *testing.T) *coreclient.Client {
	t.Helper()
	repoRoot, err := findRepoRoot()
	if err != nil {
		t.Fatalf("could not find repo root: %v", err)
	}
	binPath, err := ensureProdServer(repoRoot)
	if err != nil {
		t.Fatalf("could not ensure prod server: %v", err)
	}

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

	return client
}

func makeConnectionEvidence(id string, subject *corev1.ResourceIdentity, dest string, port int) *corev1.Evidence {
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
		ObservedAt:      "2026-10-06T12:00:00Z",
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
		Id: id,
		Source: &corev1.EvidenceSource{
			Provider:  "aws",
			Collector: "aws-network",
		},
		ObservedAt:      "2026-10-06T12:00:01Z",
		ObservationType: "RESOURCE_REFERENCE",
		Subject:         subject,
		Data:            data,
	}
}

// ---------------------------------------------------------------------------
// End-to-End Pipeline Verification Test
//
// Proves the complete pipeline without any fake or hardcoded production data:
// Collector Observation
//
//	-> State Assembly
//	-> Persistent Store
//	-> DiscoveryEngine execution
//	-> Materializer
//	-> Rust Core Engine (Graph, ProvenanceStore, Evidence[])
//	-> AnswerEngine execution
//	-> Explainable ImpactAnswer with multi-hop paths, supported relationships, evidence, facts
//	-> Multi-tenant isolation between Workspace A and Workspace B
//
// ---------------------------------------------------------------------------
func TestEndToEnd_CollectorObservationToAnswerEngine(t *testing.T) {
	client := startProdServer(t)

	ctx := context.Background()
	logger := logging.NewStandardLogger(nil, logging.LevelDebug)
	store := state.NewMemoryStore()
	materializer := state.NewMaterializer(store, client, logger)

	// StateAssembler configured with real Rust client for Discovery & Materialization
	assembler := state.NewStateAssembler(store, client, materializer, logger)

	workspaceA := "tenant-workspace-alpha"
	workspaceB := "tenant-workspace-beta"

	// Define multi-hop topology:
	// frontend -> checkout-service -> payments-db
	frontendSubj := &corev1.ResourceIdentity{
		Provider:     "kubernetes",
		ResourceType: "pod",
		ProviderId:   "payments/frontend",
	}
	checkoutSubj := &corev1.ResourceIdentity{
		Provider:     "kubernetes",
		ResourceType: "pod",
		ProviderId:   "payments/checkout-service",
	}
	databaseSubj := &corev1.ResourceIdentity{
		Provider:     "aws",
		ResourceType: "rds",
		ProviderId:   "arn:aws:rds:us-east-1:123:payments-db",
	}

	// Hop 1 evidence: frontend connects to checkout-service (10.0.1.10:8080)
	conn1ID := "550e8400-e29b-41d4-a716-446655440001"
	map1ID := "550e8400-e29b-41d4-a716-446655440002"
	conn1 := makeConnectionEvidence(conn1ID, frontendSubj, "10.0.1.10", 8080)
	map1 := makeMappingEvidence(map1ID, checkoutSubj, "10.0.1.10", 8080)

	// Hop 2 evidence: checkout-service connects to payments-db (10.0.2.15:5432)
	conn2ID := "550e8400-e29b-41d4-a716-446655440003"
	map2ID := "550e8400-e29b-41d4-a716-446655440004"
	conn2 := makeConnectionEvidence(conn2ID, checkoutSubj, "10.0.2.15", 5432)
	map2 := makeMappingEvidence(map2ID, databaseSubj, "10.0.2.15", 5432)

	batch := state.ObservationBatch{
		WorkspaceID: workspaceA,
		Evidence:    []*corev1.Evidence{conn1, map1, conn2, map2},
	}

	// 1. Ingest & Materialize into Workspace A
	ingestRes, err := assembler.IngestAndMaterialize(ctx, batch)
	if err != nil {
		t.Fatalf("IngestAndMaterialize failed for Workspace A: %v", err)
	}

	if ingestRes.EvidenceCount != 4 {
		t.Errorf("expected 4 evidence records ingested, got %d", ingestRes.EvidenceCount)
	}
	if ingestRes.DiscoveredCount != 2 {
		t.Errorf("expected 2 relationships discovered by Rust engine, got %d", ingestRes.DiscoveredCount)
	}
	if ingestRes.ProvenanceLinksAdded != 4 {
		t.Errorf("expected 4 provenance links added, got %d", ingestRes.ProvenanceLinksAdded)
	}

	// 2. Query AnswerEngine for Workspace A via Go AnswerService
	answerSvc := answer.NewService(client, logger)
	ansA, err := answerSvc.AnalyzeImpact(ctx, answer.ImpactRequest{
		WorkspaceID: workspaceA,
		Target: &answer.ResourceIdentity{
			Provider:     "aws",
			ResourceType: "rds",
			ProviderID:   "arn:aws:rds:us-east-1:123:payments-db",
		},
		Direction: "incoming",
		MaxDepth:  5,
	})
	if err != nil {
		t.Fatalf("AnalyzeImpact failed on Workspace A: %v", err)
	}

	// Verify Target
	if ansA.Target.ProviderID != "arn:aws:rds:us-east-1:123:payments-db" {
		t.Errorf("expected target payments-db, got %s", ansA.Target.ProviderID)
	}

	// Verify Summary (found checkout-service and frontend!)
	if ansA.Summary.ImpactedCount != 2 {
		t.Errorf("expected 2 impacted resources, got %d", ansA.Summary.ImpactedCount)
	}
	if ansA.Summary.DirectCount != 1 {
		t.Errorf("expected 1 direct dependent (checkout-service), got %d", ansA.Summary.DirectCount)
	}
	if ansA.Summary.IndirectCount != 1 {
		t.Errorf("expected 1 indirect dependent (frontend), got %d", ansA.Summary.IndirectCount)
	}
	if ansA.Summary.MaxDepth != 2 {
		t.Errorf("expected max depth 2, got %d", ansA.Summary.MaxDepth)
	}

	// Verify Traversal Paths (2 paths: direct and 2-hop indirect)
	if len(ansA.Paths) != 2 {
		t.Fatalf("expected 2 traversal paths in Workspace A, got %d", len(ansA.Paths))
	}
	// Direct path: checkout-service -> payments-db
	if len(ansA.Paths[0].Resources) != 2 ||
		ansA.Paths[0].Resources[0].ProviderID != "payments/checkout-service" ||
		ansA.Paths[0].Resources[1].ProviderID != "arn:aws:rds:us-east-1:123:payments-db" {
		t.Errorf("unexpected direct path: %+v", ansA.Paths[0].Resources)
	}
	// Indirect path: frontend -> checkout-service -> payments-db
	if len(ansA.Paths[1].Resources) != 3 ||
		ansA.Paths[1].Resources[0].ProviderID != "payments/frontend" ||
		ansA.Paths[1].Resources[1].ProviderID != "payments/checkout-service" ||
		ansA.Paths[1].Resources[2].ProviderID != "arn:aws:rds:us-east-1:123:payments-db" {
		t.Errorf("unexpected indirect path: %+v", ansA.Paths[1].Resources)
	}

	// Verify Relationships and Supported State with Provenance Evidence
	if len(ansA.Relationships) != 2 {
		t.Fatalf("expected 2 relationships, got %d", len(ansA.Relationships))
	}
	for _, rel := range ansA.Relationships {
		if rel.State != "supported" {
			t.Errorf("expected relationship state 'supported', got %q", rel.State)
		}
		if len(rel.EvidenceIDs) != 2 {
			t.Errorf("expected relationship to have 2 linked evidence IDs, got %v", rel.EvidenceIDs)
		}
	}

	// Verify Evidence Catalog
	if len(ansA.Evidence) != 4 {
		t.Fatalf("expected 4 evidence entries in ImpactAnswer, got %d", len(ansA.Evidence))
	}

	// Verify Explanation Facts
	if len(ansA.ExplanationFacts) == 0 {
		t.Errorf("expected non-empty explanation facts for explainability")
	}

	// -----------------------------------------------------------------------
	// 3. Multi-Tenant Isolation Verification
	//
	// Querying the EXACT SAME target in Workspace B MUST yield 0 impacted
	// resources and zero leakage from Workspace A.
	// -----------------------------------------------------------------------
	ansB, err := answerSvc.AnalyzeImpact(ctx, answer.ImpactRequest{
		WorkspaceID: workspaceB,
		Target: &answer.ResourceIdentity{
			Provider:     "aws",
			ResourceType: "rds",
			ProviderID:   "arn:aws:rds:us-east-1:123:payments-db",
		},
		Direction: "incoming",
		MaxDepth:  5,
	})
	if err != nil {
		t.Fatalf("AnalyzeImpact failed on Workspace B: %v", err)
	}

	if ansB.Summary.ImpactedCount != 0 {
		t.Errorf("expected Workspace B to have 0 impacted resources (cross-tenant leakage!), got %d", ansB.Summary.ImpactedCount)
	}
	if len(ansB.Paths) != 0 {
		t.Errorf("expected Workspace B to have 0 paths, got %d", len(ansB.Paths))
	}
	if len(ansB.Relationships) != 0 {
		t.Errorf("expected Workspace B to have 0 relationships, got %d", len(ansB.Relationships))
	}
	if len(ansB.Evidence) != 0 {
		t.Errorf("expected Workspace B to have 0 evidence, got %d", len(ansB.Evidence))
	}
}
