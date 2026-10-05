package k8s

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	corev1 "github.com/aaradhychinche-alt/WhatBreaks/gen/go/wb/core/v1"
	"github.com/aaradhychinche-alt/WhatBreaks/internal/coreclient"
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

func spawnCoreServer(t *testing.T) (*coreclient.Client, func()) {
	t.Helper()
	repoRoot, err := findRepoRoot()
	if err != nil {
		t.Skipf("cannot find repo root: %v", err)
	}

	binPath := filepath.Join(repoRoot, "target", "debug", "wb-core-server")
	if _, err := os.Stat(binPath); err != nil {
		t.Skipf("wb-core-server binary not found at %s: %v", binPath, err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to allocate free port: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	cmd := exec.Command(binPath)
	cmd.Env = append(os.Environ(), "WB_CORE_GRPC_ADDR="+addr)
	var logs bytes.Buffer
	cmd.Stdout = &logs
	cmd.Stderr = &logs

	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start wb-core-server: %v", err)
	}

	cleanup := func() {
		if cmd.Process != nil {
			_ = cmd.Process.Signal(os.Interrupt)
			time.Sleep(50 * time.Millisecond)
			_ = cmd.Process.Kill()
		}
	}

	// Wait for server to accept connections
	var client *coreclient.Client
	for i := 0; i < 20; i++ {
		time.Sleep(50 * time.Millisecond)
		connCtx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		c, err := coreclient.Connect(connCtx, addr)
		cancel()
		if err == nil {
			client = c
			break
		}
	}

	if client == nil {
		cleanup()
		t.Fatalf("timed out connecting to wb-core-server at %s, logs: %s", addr, logs.String())
	}

	return client, func() {
		_ = client.Close()
		cleanup()
	}
}

func TestCollector_CoreEngineIntegration(t *testing.T) {
	client, cleanup := spawnCoreServer(t)
	defer cleanup()

	// 1. Normalizer setup
	normalizer := NewNormalizer("cluster-prod", "ws-1", "k8s-collector")

	// 2. Service providing database endpoint (ClusterIP 10.96.100.50:5432)
	dbService := &Service{
		ObjectMeta: ObjectMeta{
			Name:      "postgres-service",
			Namespace: "production",
		},
		Spec: ServiceSpec{
			Type:      "ClusterIP",
			ClusterIP: "10.96.100.50",
			Ports: []ServicePort{
				{Name: "postgres", Port: 5432, Protocol: "TCP"},
			},
		},
	}

	// 3. Normalizer emits Service evidence (including RESOURCE_REFERENCE on 10.96.100.50:5432)
	svcEvidence := normalizer.NormalizeService(dbService)

	// 4. Pod that has an observed runtime connection to the database
	podSubject := BuildIdentity("cluster-prod", TypePod, "production", "api-backend")
	connData, _ := json.Marshal(map[string]any{
		"destination": "10.96.100.50",
		"port":        5432,
		"protocol":    "tcp",
	})
	podConnEvidence := &corev1.Evidence{
		Id:              GenerateEvidenceID(),
		Source:          BuildSource("k8s-collector"),
		ObservedAt:      time.Now().UTC().Format(time.RFC3339),
		ObservationType: ObservationRuntimeConnection,
		Subject:         podSubject,
		Data:            connData,
	}

	// Combine evidence batch
	allEvidence := append(svcEvidence, podConnEvidence)

	// 5. Submit evidence batch through Collector to Rust Core Engine
	collector, err := New(
		WithClusterID("cluster-prod"),
		WithWorkspaceID("ws-1"),
		WithClient(&RESTClient{}), // stub client since we're testing SubmitToCore
		WithCoreClient(client),
	)
	if err != nil {
		t.Fatalf("failed to create collector: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := collector.SubmitToCore(ctx, allEvidence)
	if err != nil {
		t.Fatalf("SubmitToCore failed: %v", err)
	}

	if len(resp.Results) == 0 {
		t.Fatalf("expected at least 1 discovery result from Core Engine, got 0")
	}

	// Verify that RuntimeConnectionRule in Rust discovered:
	// api-backend --DEPENDS_ON--> postgres-service!
	foundRelationship := false
	for _, res := range resp.Results {
		if rel := res.GetDiscovered(); rel != nil {
			if rel.Relationship.Kind == "DEPENDS_ON" &&
				rel.Relationship.Source.ProviderId == "cluster-prod/production/api-backend" &&
				rel.Relationship.Target.ProviderId == "cluster-prod/production/postgres-service" {
				foundRelationship = true
				if len(rel.SupportingEvidenceIds) != 2 {
					t.Errorf("expected 2 supporting evidence IDs, got %d", len(rel.SupportingEvidenceIds))
				}
			}
		}
	}

	if !foundRelationship {
		t.Fatalf("Core Engine failed to discover expected relationship. Results: %+v", resp.Results)
	}
}
