package state_test

import (
	"context"
	"fmt"
	"sync"
	"testing"

	corev1 "github.com/aaradhychinche-alt/WhatBreaks/gen/go/wb/core/v1"
	"github.com/aaradhychinche-alt/WhatBreaks/internal/state"
	"google.golang.org/grpc"
)

type mockDiscoveryRunner struct {
	resp *corev1.RunDiscoveryResponse
	err  error
}

func (m *mockDiscoveryRunner) RunDiscovery(ctx context.Context, req *corev1.RunDiscoveryRequest, opts ...grpc.CallOption) (*corev1.RunDiscoveryResponse, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.resp, nil
}

func protoPod(name string) *corev1.ResourceIdentity {
	return &corev1.ResourceIdentity{
		Provider:     "kubernetes",
		ResourceType: "pod",
		ProviderId:   fmt.Sprintf("default/%s", name),
	}
}

func protoEvidence(id string, subject *corev1.ResourceIdentity) *corev1.Evidence {
	return &corev1.Evidence{
		Id: id,
		Source: &corev1.EvidenceSource{
			Provider:  "kubernetes",
			Collector: "k8s-runtime",
		},
		ObservedAt:      "2026-10-06T12:00:00Z",
		ObservationType: "RUNTIME_CONNECTION",
		Subject:         subject,
		Data:            []byte(`{"endpoint": "10.0.0.1:8080"}`),
	}
}

// ---------------------------------------------------------------------------
// DISCOVERY OUTCOME TESTS (Discovered, Insufficient, Conflict, Invalid)
// ---------------------------------------------------------------------------

func TestAssembler_DiscoveryOutcomes(t *testing.T) {
	ev1 := protoEvidence("550e8400-e29b-41d4-a716-446655440001", protoPod("payments-api"))
	ev2 := protoEvidence("550e8400-e29b-41d4-a716-446655440002", protoPod("orders-api"))

	mockDiscovery := &mockDiscoveryRunner{
		resp: &corev1.RunDiscoveryResponse{
			Results: []*corev1.DiscoveryResult{
				// 1. Discovered
				{
					Outcome: &corev1.DiscoveryResult_Discovered{
						Discovered: &corev1.DiscoveredRelationship{
							Relationship: &corev1.Relationship{
								Source:   protoPod("orders-api"),
								Target:   protoPod("payments-api"),
								Kind:     "DEPENDS_ON",
								Category: "Dependency",
							},
							SupportingEvidenceIds: []string{ev1.Id, ev2.Id},
						},
					},
				},
				// 2. Insufficient
				{
					Outcome: &corev1.DiscoveryResult_Insufficient{
						Insufficient: &corev1.Insufficient{},
					},
				},
				// 3. Conflict
				{
					Outcome: &corev1.DiscoveryResult_Conflict{
						Conflict: &corev1.Conflict{
							Description: "Conflicting database endpoint mappings detected",
						},
					},
				},
				// 4. Invalid
				{
					Outcome: &corev1.DiscoveryResult_Invalid{
						Invalid: &corev1.Invalid{
							Description: "Malformed data payload in observation",
						},
					},
				},
			},
		},
	}

	store := state.NewMemoryStore()
	assembler := state.NewStateAssembler(store, mockDiscovery, nil, nil)
	ctx := context.Background()

	res, err := assembler.Ingest(ctx, state.ObservationBatch{
		WorkspaceID: "ws-test",
		Evidence:    []*corev1.Evidence{ev1, ev2},
	})
	if err != nil {
		t.Fatalf("Ingest failed: %v", err)
	}

	// Verify outcome tallies
	if res.DiscoveredCount != 1 {
		t.Errorf("expected 1 discovered, got %d", res.DiscoveredCount)
	}
	if res.InsufficientCount != 1 {
		t.Errorf("expected 1 insufficient, got %d", res.InsufficientCount)
	}
	if res.ConflictCount != 1 {
		t.Errorf("expected 1 conflict, got %d", res.ConflictCount)
	}
	if res.InvalidCount != 1 {
		t.Errorf("expected 1 invalid, got %d", res.InvalidCount)
	}
	if res.ProvenanceLinksAdded != 2 {
		t.Errorf("expected 2 provenance links, got %d", res.ProvenanceLinksAdded)
	}

	// Verify relationship in store
	rels, err := store.ListRelationships(ctx, "ws-test")
	if err != nil {
		t.Fatalf("ListRelationships failed: %v", err)
	}
	if len(rels) != 1 {
		t.Fatalf("expected exactly 1 relationship in store, got %d", len(rels))
	}
	if rels[0].Source.ProviderID != "default/orders-api" || rels[0].Target.ProviderID != "default/payments-api" {
		t.Errorf("unexpected relationship in store: %+v", rels[0])
	}

	// Verify endpoints were automatically registered as resources
	resources, err := store.ListResources(ctx, "ws-test")
	if err != nil {
		t.Fatalf("ListResources failed: %v", err)
	}
	if len(resources) != 2 {
		t.Errorf("expected 2 resources in store, got %d", len(resources))
	}

	// Verify provenance links in store
	relKey := rels[0].Key()
	evIDs, err := store.GetProvenanceForRelationship(ctx, "ws-test", relKey)
	if err != nil {
		t.Fatalf("GetProvenanceForRelationship failed: %v", err)
	}
	if len(evIDs) != 2 {
		t.Errorf("expected 2 evidence IDs in provenance store, got %d", len(evIDs))
	}
}

// ---------------------------------------------------------------------------
// REPEATED INGESTION & IDEMPOTENCY
// ---------------------------------------------------------------------------

func TestAssembler_RepeatedIngestion(t *testing.T) {
	ev1 := protoEvidence("550e8400-e29b-41d4-a716-446655440010", protoPod("service-a"))
	ev2 := protoEvidence("550e8400-e29b-41d4-a716-446655440011", protoPod("service-b"))

	mockDiscovery := &mockDiscoveryRunner{
		resp: &corev1.RunDiscoveryResponse{
			Results: []*corev1.DiscoveryResult{
				{
					Outcome: &corev1.DiscoveryResult_Discovered{
						Discovered: &corev1.DiscoveredRelationship{
							Relationship: &corev1.Relationship{
								Source:   protoPod("service-a"),
								Target:   protoPod("service-b"),
								Kind:     "DEPENDS_ON",
								Category: "Dependency",
							},
							SupportingEvidenceIds: []string{ev1.Id},
						},
					},
				},
			},
		},
	}

	store := state.NewMemoryStore()
	assembler := state.NewStateAssembler(store, mockDiscovery, nil, nil)
	ctx := context.Background()

	batch := state.ObservationBatch{
		WorkspaceID: "ws-repeat",
		Evidence:    []*corev1.Evidence{ev1, ev2},
	}

	// Ingest once
	_, err := assembler.Ingest(ctx, batch)
	if err != nil {
		t.Fatalf("first ingestion failed: %v", err)
	}

	// Ingest twice (repeated observations)
	_, err = assembler.Ingest(ctx, batch)
	if err != nil {
		t.Fatalf("second ingestion failed: %v", err)
	}

	// Should still have exactly 1 relationship and 2 resources
	rels, _ := store.ListRelationships(ctx, "ws-repeat")
	if len(rels) != 1 {
		t.Errorf("expected 1 relationship after repeated ingestion, got %d", len(rels))
	}

	evs, _ := store.ListEvidence(ctx, "ws-repeat")
	if len(evs) != 2 {
		t.Errorf("expected 2 evidence records after repeated ingestion, got %d", len(evs))
	}

	prov, _ := store.GetProvenanceForRelationship(ctx, "ws-repeat", rels[0].Key())
	if len(prov) != 1 {
		t.Errorf("expected 1 provenance link after repeated ingestion, got %d", len(prov))
	}
}

// ---------------------------------------------------------------------------
// CONCURRENT INGESTION SAFETY
// ---------------------------------------------------------------------------

func TestAssembler_ConcurrentIngestion(t *testing.T) {
	store := state.NewMemoryStore()
	mockDiscovery := &mockDiscoveryRunner{
		resp: &corev1.RunDiscoveryResponse{},
	}
	assembler := state.NewStateAssembler(store, mockDiscovery, nil, nil)
	ctx := context.Background()

	var wg sync.WaitGroup
	errCh := make(chan error, 10)

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			evID := fmt.Sprintf("550e8400-e29b-41d4-a716-4466554400%02d", workerID)
			podName := fmt.Sprintf("worker-pod-%d", workerID)
			batch := state.ObservationBatch{
				WorkspaceID: "ws-concurrent",
				Evidence:    []*corev1.Evidence{protoEvidence(evID, protoPod(podName))},
			}
			if _, err := assembler.Ingest(ctx, batch); err != nil {
				errCh <- err
			}
		}(i)
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Fatalf("concurrent ingestion error: %v", err)
	}

	resources, err := store.ListResources(ctx, "ws-concurrent")
	if err != nil {
		t.Fatalf("ListResources failed: %v", err)
	}
	if len(resources) != 10 {
		t.Errorf("expected 10 resources from concurrent ingestion, got %d", len(resources))
	}
}
