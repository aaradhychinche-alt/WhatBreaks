package state_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	corev1 "github.com/aaradhychinche-alt/WhatBreaks/gen/go/wb/core/v1"
	"github.com/aaradhychinche-alt/WhatBreaks/internal/state"
)

// Helper to construct normalized proto evidence
func newTestEvidence(id, provider, resType, providerID, obsType, data string, observedAt time.Time) *corev1.Evidence {
	return &corev1.Evidence{
		Id: id,
		Source: &corev1.EvidenceSource{
			Provider:  provider,
			Collector: "test-collector",
		},
		ObservedAt:      observedAt.Format(time.RFC3339Nano),
		ObservationType: obsType,
		Subject: &corev1.ResourceIdentity{
			Provider:     provider,
			ResourceType: resType,
			ProviderId:   providerID,
		},
		Data: []byte(data),
	}
}

// ---------------------------------------------------------------------------
// 1. NEW RESOURCE OBSERVATION
// ---------------------------------------------------------------------------

func TestReconciler_NewResource(t *testing.T) {
	store := state.NewMemoryStore()
	reconciler := state.NewReconciler(store, nil, nil, nil)
	ctx := context.Background()

	t0 := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	ev := newTestEvidence("ev-001", "kubernetes", "pod", "default/order-service-pod", "CONTROL_PLANE_OBJECT", `{"status":"running"}`, t0)

	batch := state.ObservationBatch{
		WorkspaceID: "ws-new-resource",
		Evidence:    []*corev1.Evidence{ev},
	}

	res, err := reconciler.Reconcile(ctx, batch)
	if err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	if res.ResourcesCreated != 1 {
		t.Errorf("expected 1 resource created, got %d", res.ResourcesCreated)
	}
	if res.ResourcesUpdated != 0 {
		t.Errorf("expected 0 resources updated, got %d", res.ResourcesUpdated)
	}
	if res.ResourcesTotal != 1 {
		t.Errorf("expected 1 total resource, got %d", res.ResourcesTotal)
	}

	// Verify store entry
	storedRes, err := store.GetResource(ctx, "ws-new-resource", state.ResourceIdentity{
		Provider:     "kubernetes",
		ResourceType: "pod",
		ProviderID:   "default/order-service-pod",
	})
	if err != nil {
		t.Fatalf("GetResource failed: %v", err)
	}

	if !storedRes.FirstObservedAt.Equal(t0) {
		t.Errorf("expected FirstObservedAt=%v, got %v", t0, storedRes.FirstObservedAt)
	}
	if !storedRes.LastObservedAt.Equal(t0) {
		t.Errorf("expected LastObservedAt=%v, got %v", t0, storedRes.LastObservedAt)
	}
}

// ---------------------------------------------------------------------------
// 2. REPEATED OBSERVATION & IDEMPOTENCY
// ---------------------------------------------------------------------------

func TestReconciler_RepeatedObservationIdempotency(t *testing.T) {
	store := state.NewMemoryStore()
	reconciler := state.NewReconciler(store, nil, nil, nil)
	ctx := context.Background()

	t0 := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	ev := newTestEvidence("ev-002", "kubernetes", "pod", "default/payment-api", "CONTROL_PLANE_OBJECT", `{"status":"running"}`, t0)

	batch := state.ObservationBatch{
		WorkspaceID: "ws-idempotency",
		Evidence:    []*corev1.Evidence{ev},
	}

	// First observation
	res1, err := reconciler.Reconcile(ctx, batch)
	if err != nil {
		t.Fatalf("first Reconcile failed: %v", err)
	}
	if res1.ResourcesCreated != 1 || res1.ResourcesUpdated != 0 {
		t.Errorf("unexpected first result: %+v", res1)
	}

	// Second repeated observation (exact same batch)
	res2, err := reconciler.Reconcile(ctx, batch)
	if err != nil {
		t.Fatalf("second Reconcile failed: %v", err)
	}
	if res2.ResourcesCreated != 0 {
		t.Errorf("expected 0 resources created on duplicate, got %d", res2.ResourcesCreated)
	}
	if res2.ResourcesUpdated != 1 {
		t.Errorf("expected 1 resource updated on duplicate, got %d", res2.ResourcesUpdated)
	}
	if res2.ResourcesTotal != 1 {
		t.Errorf("expected 1 total resource, got %d", res2.ResourcesTotal)
	}

	// Verify only 1 resource remains in store
	all, err := store.ListResources(ctx, "ws-idempotency")
	if err != nil {
		t.Fatalf("ListResources failed: %v", err)
	}
	if len(all) != 1 {
		t.Errorf("expected exactly 1 resource in store, got %d", len(all))
	}
}

// ---------------------------------------------------------------------------
// 3. RESOURCE UPDATE & PRESERVED IDENTITY
// ---------------------------------------------------------------------------

func TestReconciler_ResourceUpdate(t *testing.T) {
	store := state.NewMemoryStore()
	reconciler := state.NewReconciler(store, nil, nil, nil)
	ctx := context.Background()

	t0 := time.Date(2026, 10, 8, 10, 0, 0, 0, time.UTC)
	t1 := time.Date(2026, 10, 8, 11, 30, 0, 0, time.UTC)

	evInitial := newTestEvidence("ev-003a", "aws", "rds", "arn:aws:rds:db-1", "CONTROL_PLANE_OBJECT", `{"status":"available"}`, t0)
	evLater := newTestEvidence("ev-003b", "aws", "rds", "arn:aws:rds:db-1", "CONTROL_PLANE_OBJECT", `{"status":"available"}`, t1)

	// Step 1: initial observation
	if _, err := reconciler.Reconcile(ctx, state.ObservationBatch{
		WorkspaceID: "ws-update",
		Evidence:    []*corev1.Evidence{evInitial},
	}); err != nil {
		t.Fatalf("initial Reconcile failed: %v", err)
	}

	// Step 2: later observation of the same resource
	resLater, err := reconciler.Reconcile(ctx, state.ObservationBatch{
		WorkspaceID: "ws-update",
		Evidence:    []*corev1.Evidence{evLater},
	})
	if err != nil {
		t.Fatalf("later Reconcile failed: %v", err)
	}

	if resLater.ResourcesCreated != 0 {
		t.Errorf("expected 0 resources created, got %d", resLater.ResourcesCreated)
	}
	if resLater.ResourcesUpdated != 1 {
		t.Errorf("expected 1 resource updated, got %d", resLater.ResourcesUpdated)
	}

	stored, err := store.GetResource(ctx, "ws-update", state.ResourceIdentity{
		Provider:     "aws",
		ResourceType: "rds",
		ProviderID:   "arn:aws:rds:db-1",
	})
	if err != nil {
		t.Fatalf("GetResource failed: %v", err)
	}

	// FirstObservedAt must be preserved; LastObservedAt must advance
	if !stored.FirstObservedAt.Equal(t0) {
		t.Errorf("expected FirstObservedAt=%v, got %v", t0, stored.FirstObservedAt)
	}
	if !stored.LastObservedAt.Equal(t1) {
		t.Errorf("expected LastObservedAt=%v, got %v", t1, stored.LastObservedAt)
	}
}

// ---------------------------------------------------------------------------
// 4. IMMUTABLE EVIDENCE GUARANTEE
// ---------------------------------------------------------------------------

func TestReconciler_ImmutableEvidence(t *testing.T) {
	store := state.NewMemoryStore()
	reconciler := state.NewReconciler(store, nil, nil, nil)
	ctx := context.Background()

	t0 := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	originalEv := newTestEvidence("ev-immutable-1", "kubernetes", "pod", "default/auth-pod", "RUNTIME_CONNECTION", `{"endpoint":"10.0.0.1:8080"}`, t0)
	tamperedEv := newTestEvidence("ev-immutable-1", "kubernetes", "pod", "default/auth-pod", "RUNTIME_CONNECTION", `{"endpoint":"10.0.0.99:9999"}`, t0.Add(time.Hour))

	// Ingest original
	if _, err := reconciler.Reconcile(ctx, state.ObservationBatch{
		WorkspaceID: "ws-immutable",
		Evidence:    []*corev1.Evidence{originalEv},
	}); err != nil {
		t.Fatalf("first Reconcile failed: %v", err)
	}

	// Ingest tampered evidence with same ID
	if _, err := reconciler.Reconcile(ctx, state.ObservationBatch{
		WorkspaceID: "ws-immutable",
		Evidence:    []*corev1.Evidence{tamperedEv},
	}); err != nil {
		t.Fatalf("second Reconcile failed: %v", err)
	}

	evList, err := store.ListEvidence(ctx, "ws-immutable")
	if err != nil {
		t.Fatalf("ListEvidence failed: %v", err)
	}
	if len(evList) != 1 {
		t.Fatalf("expected exactly 1 evidence record, got %d", len(evList))
	}

	// The historical record MUST NOT have been overwritten
	expectedData := `{"endpoint":"10.0.0.1:8080"}`
	if string(evList[0].Data) != expectedData {
		t.Errorf("evidence record was mutated! expected %s, got %s", expectedData, string(evList[0].Data))
	}
}

// ---------------------------------------------------------------------------
// 5. RELATIONSHIP RECONCILIATION & PROVENANCE
// ---------------------------------------------------------------------------

func TestReconciler_RelationshipReconciliation(t *testing.T) {
	t0 := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	ev1 := newTestEvidence("ev-rel-1", "kubernetes", "pod", "default/frontend", "RUNTIME_CONNECTION", `{"dest":"backend"}`, t0)
	ev2 := newTestEvidence("ev-rel-2", "kubernetes", "pod", "default/backend", "RUNTIME_CONNECTION", `{"port":8080}`, t0)

	mockDisc := &mockDiscoveryRunner{
		resp: &corev1.RunDiscoveryResponse{
			Results: []*corev1.DiscoveryResult{
				{
					Outcome: &corev1.DiscoveryResult_Discovered{
						Discovered: &corev1.DiscoveredRelationship{
							Relationship: &corev1.Relationship{
								Source: &corev1.ResourceIdentity{
									Provider:     "kubernetes",
									ResourceType: "pod",
									ProviderId:   "default/frontend",
								},
								Target: &corev1.ResourceIdentity{
									Provider:     "kubernetes",
									ResourceType: "pod",
									ProviderId:   "default/backend",
								},
								Kind:     "DEPENDS_ON",
								Category: "Dependency",
							},
							SupportingEvidenceIds: []string{ev1.Id, ev2.Id},
						},
					},
				},
			},
		},
	}

	store := state.NewMemoryStore()
	reconciler := state.NewReconciler(store, mockDisc, nil, nil)
	ctx := context.Background()

	res, err := reconciler.Reconcile(ctx, state.ObservationBatch{
		WorkspaceID: "ws-rel",
		Evidence:    []*corev1.Evidence{ev1, ev2},
	})
	if err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	if res.RelationshipsCreated != 1 {
		t.Errorf("expected 1 relationship created, got %d", res.RelationshipsCreated)
	}
	if res.RelationshipsUpdated != 0 {
		t.Errorf("expected 0 relationships updated, got %d", res.RelationshipsUpdated)
	}
	if res.ProvenanceLinksAdded != 2 {
		t.Errorf("expected 2 provenance links, got %d", res.ProvenanceLinksAdded)
	}

	rels, err := store.ListRelationships(ctx, "ws-rel")
	if err != nil {
		t.Fatalf("ListRelationships failed: %v", err)
	}
	if len(rels) != 1 {
		t.Fatalf("expected 1 relationship, got %d", len(rels))
	}

	relKey := rels[0].Key()
	evIDs, err := store.GetProvenanceForRelationship(ctx, "ws-rel", relKey)
	if err != nil {
		t.Fatalf("GetProvenanceForRelationship failed: %v", err)
	}
	if len(evIDs) != 2 {
		t.Errorf("expected 2 supporting evidence IDs, got %d", len(evIDs))
	}
}

// ---------------------------------------------------------------------------
// 6. REPEATED RELATIONSHIP & MULTI-EVIDENCE PROVENANCE
// ---------------------------------------------------------------------------

func TestReconciler_RepeatedRelationshipAndEvidence(t *testing.T) {
	t0 := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	t1 := t0.Add(10 * time.Minute)

	ev1 := newTestEvidence("ev-multi-1", "kubernetes", "pod", "default/checkout", "RUNTIME_CONNECTION", `{"call":"inventory"}`, t0)
	ev2 := newTestEvidence("ev-multi-2", "kubernetes", "pod", "default/checkout", "RUNTIME_CONNECTION", `{"call":"inventory-retry"}`, t1)

	relProto := &corev1.Relationship{
		Source: &corev1.ResourceIdentity{
			Provider:     "kubernetes",
			ResourceType: "pod",
			ProviderId:   "default/checkout",
		},
		Target: &corev1.ResourceIdentity{
			Provider:     "kubernetes",
			ResourceType: "pod",
			ProviderId:   "default/inventory",
		},
		Kind:     "DEPENDS_ON",
		Category: "Dependency",
	}

	mockDisc1 := &mockDiscoveryRunner{
		resp: &corev1.RunDiscoveryResponse{
			Results: []*corev1.DiscoveryResult{
				{
					Outcome: &corev1.DiscoveryResult_Discovered{
						Discovered: &corev1.DiscoveredRelationship{
							Relationship:          relProto,
							SupportingEvidenceIds: []string{ev1.Id},
						},
					},
				},
			},
		},
	}

	store := state.NewMemoryStore()
	reconciler1 := state.NewReconciler(store, mockDisc1, nil, nil)
	ctx := context.Background()

	// Initial observation batch
	res1, err := reconciler1.Reconcile(ctx, state.ObservationBatch{
		WorkspaceID: "ws-multi-ev",
		Evidence:    []*corev1.Evidence{ev1},
	})
	if err != nil {
		t.Fatalf("initial Reconcile failed: %v", err)
	}
	if res1.RelationshipsCreated != 1 || res1.RelationshipsUpdated != 0 {
		t.Errorf("unexpected initial result: %+v", res1)
	}

	// Second observation batch with NEW evidence confirming the same relationship
	mockDisc2 := &mockDiscoveryRunner{
		resp: &corev1.RunDiscoveryResponse{
			Results: []*corev1.DiscoveryResult{
				{
					Outcome: &corev1.DiscoveryResult_Discovered{
						Discovered: &corev1.DiscoveredRelationship{
							Relationship:          relProto,
							SupportingEvidenceIds: []string{ev2.Id},
						},
					},
				},
			},
		},
	}
	reconciler2 := state.NewReconciler(store, mockDisc2, nil, nil)

	res2, err := reconciler2.Reconcile(ctx, state.ObservationBatch{
		WorkspaceID: "ws-multi-ev",
		Evidence:    []*corev1.Evidence{ev2},
	})
	if err != nil {
		t.Fatalf("second Reconcile failed: %v", err)
	}

	// Should update existing relationship, not create a duplicate
	if res2.RelationshipsCreated != 0 {
		t.Errorf("expected 0 relationships created, got %d", res2.RelationshipsCreated)
	}
	if res2.RelationshipsUpdated != 1 {
		t.Errorf("expected 1 relationship updated, got %d", res2.RelationshipsUpdated)
	}

	// Verify total relationships count remains 1
	rels, _ := store.ListRelationships(ctx, "ws-multi-ev")
	if len(rels) != 1 {
		t.Fatalf("expected 1 relationship in store, got %d", len(rels))
	}

	// Provenance should now have BOTH evidence IDs
	prov, err := store.GetProvenanceForRelationship(ctx, "ws-multi-ev", rels[0].Key())
	if err != nil {
		t.Fatalf("GetProvenanceForRelationship failed: %v", err)
	}
	if len(prov) != 2 {
		t.Fatalf("expected 2 provenance links (ev-multi-1, ev-multi-2), got %d: %v", len(prov), prov)
	}
	if prov[0] != "ev-multi-1" || prov[1] != "ev-multi-2" {
		t.Errorf("unexpected provenance links: %v", prov)
	}
}

// ---------------------------------------------------------------------------
// 7. WORKSPACE ISOLATION
// ---------------------------------------------------------------------------

func TestReconciler_WorkspaceIsolation(t *testing.T) {
	store := state.NewMemoryStore()
	reconciler := state.NewReconciler(store, nil, nil, nil)
	ctx := context.Background()

	t0 := time.Now().UTC()
	evA := newTestEvidence("ev-ws-a", "kubernetes", "pod", "default/app", "CONTROL_PLANE_OBJECT", `{}`, t0)
	evB := newTestEvidence("ev-ws-b", "kubernetes", "pod", "default/app", "CONTROL_PLANE_OBJECT", `{}`, t0)

	// Reconcile identical resource name in workspace A
	if _, err := reconciler.Reconcile(ctx, state.ObservationBatch{
		WorkspaceID: "workspace-alpha",
		Evidence:    []*corev1.Evidence{evA},
	}); err != nil {
		t.Fatalf("Reconcile workspace-alpha failed: %v", err)
	}

	// Reconcile identical resource name in workspace B
	if _, err := reconciler.Reconcile(ctx, state.ObservationBatch{
		WorkspaceID: "workspace-beta",
		Evidence:    []*corev1.Evidence{evB},
	}); err != nil {
		t.Fatalf("Reconcile workspace-beta failed: %v", err)
	}

	// Inspect workspace-alpha
	resAlpha, err := store.ListResources(ctx, "workspace-alpha")
	if err != nil {
		t.Fatalf("ListResources workspace-alpha failed: %v", err)
	}
	if len(resAlpha) != 1 || resAlpha[0].WorkspaceID != "workspace-alpha" {
		t.Errorf("unexpected workspace-alpha resources: %+v", resAlpha)
	}

	// Inspect workspace-beta
	resBeta, err := store.ListResources(ctx, "workspace-beta")
	if err != nil {
		t.Fatalf("ListResources workspace-beta failed: %v", err)
	}
	if len(resBeta) != 1 || resBeta[0].WorkspaceID != "workspace-beta" {
		t.Errorf("unexpected workspace-beta resources: %+v", resBeta)
	}
}

// ---------------------------------------------------------------------------
// 8. MISSING OBSERVATION MUST NOT DELETE STATE
// ---------------------------------------------------------------------------

func TestReconciler_MissingObservationDoesNotDeleteState(t *testing.T) {
	store := state.NewMemoryStore()
	reconciler := state.NewReconciler(store, nil, nil, nil)
	ctx := context.Background()

	t0 := time.Now().UTC()
	pod1 := newTestEvidence("ev-gap-1", "kubernetes", "pod", "default/pod-1", "CONTROL_PLANE_OBJECT", `{}`, t0)
	pod2 := newTestEvidence("ev-gap-2", "kubernetes", "pod", "default/pod-2", "CONTROL_PLANE_OBJECT", `{}`, t0)
	pod3 := newTestEvidence("ev-gap-3", "kubernetes", "pod", "default/pod-3", "CONTROL_PLANE_OBJECT", `{}`, t0)

	// Initial sweep: all 3 pods observed
	_, err := reconciler.Reconcile(ctx, state.ObservationBatch{
		WorkspaceID: "ws-gap",
		Evidence:    []*corev1.Evidence{pod1, pod2, pod3},
	})
	if err != nil {
		t.Fatalf("initial Reconcile failed: %v", err)
	}

	allRes, _ := store.ListResources(ctx, "ws-gap")
	if len(allRes) != 3 {
		t.Fatalf("expected 3 resources after initial sweep, got %d", len(allRes))
	}

	// Partial sweep: collector temporarily only observes pod-1 (e.g. namespace filter, partial failure)
	t1 := t0.Add(5 * time.Minute)
	pod1Later := newTestEvidence("ev-gap-1b", "kubernetes", "pod", "default/pod-1", "CONTROL_PLANE_OBJECT", `{}`, t1)
	resPartial, err := reconciler.Reconcile(ctx, state.ObservationBatch{
		WorkspaceID: "ws-gap",
		Evidence:    []*corev1.Evidence{pod1Later},
	})
	if err != nil {
		t.Fatalf("partial Reconcile failed: %v", err)
	}

	if resPartial.ResourcesUpdated != 1 {
		t.Errorf("expected 1 resource updated, got %d", resPartial.ResourcesUpdated)
	}
	if resPartial.ResourcesTotal != 3 {
		t.Errorf("expected 3 total resources preserved, got %d", resPartial.ResourcesTotal)
	}

	// CRITICAL INVARIANT: pod-2 and pod-3 must NOT be deleted
	allAfterPartial, _ := store.ListResources(ctx, "ws-gap")
	if len(allAfterPartial) != 3 {
		t.Fatalf("CRITICAL REGRESSION: unobserved resources were deleted! expected 3, got %d", len(allAfterPartial))
	}

	// Empty sweep: collector returns 0 observations
	resEmpty, err := reconciler.Reconcile(ctx, state.ObservationBatch{
		WorkspaceID: "ws-gap",
		Evidence:    []*corev1.Evidence{},
	})
	if err != nil {
		t.Fatalf("empty Reconcile failed: %v", err)
	}
	if resEmpty.ResourcesTotal != 3 {
		t.Errorf("expected 3 total resources after empty batch, got %d", resEmpty.ResourcesTotal)
	}

	allAfterEmpty, _ := store.ListResources(ctx, "ws-gap")
	if len(allAfterEmpty) != 3 {
		t.Fatalf("CRITICAL REGRESSION: empty batch caused resource deletion! expected 3, got %d", len(allAfterEmpty))
	}
}

// ---------------------------------------------------------------------------
// 9. EXPLICIT DELETION SIGNAL & INVENTORY BOUNDARY SEMANTICS
// ---------------------------------------------------------------------------

func TestReconciler_ScopeBoundaryPreservesState(t *testing.T) {
	store := state.NewMemoryStore()
	reconciler := state.NewReconciler(store, nil, nil, nil)
	ctx := context.Background()

	t0 := time.Now().UTC()
	pod1 := newTestEvidence("ev-scope-1", "kubernetes", "pod", "default/service-a", "CONTROL_PLANE_OBJECT", `{}`, t0)
	pod2 := newTestEvidence("ev-scope-2", "kubernetes", "pod", "kube-system/coredns", "CONTROL_PLANE_OBJECT", `{}`, t0)

	// Ingest across namespaces
	_, err := reconciler.Reconcile(ctx, state.ObservationBatch{
		WorkspaceID: "ws-scope",
		Evidence:    []*corev1.Evidence{pod1, pod2},
	})
	if err != nil {
		t.Fatalf("initial Reconcile failed: %v", err)
	}

	// Next batch with explicit ScopeBoundary defining only the "default" namespace
	pod1Updated := newTestEvidence("ev-scope-1b", "kubernetes", "pod", "default/service-a", "CONTROL_PLANE_OBJECT", `{}`, t0.Add(time.Minute))
	_, err = reconciler.Reconcile(ctx, state.ObservationBatch{
		WorkspaceID: "ws-scope",
		Evidence:    []*corev1.Evidence{pod1Updated},
		Scope: &state.ScopeBoundary{
			Provider:  "kubernetes",
			ScopeType: "namespace",
			ScopeID:   "default",
			Complete:  true,
		},
	})
	if err != nil {
		t.Fatalf("scoped Reconcile failed: %v", err)
	}

	// In v1: No automatic deletion occurs even with ScopeBoundary. coredns must still exist.
	storedCoreDNS, err := store.GetResource(ctx, "ws-scope", state.ResourceIdentity{
		Provider:     "kubernetes",
		ResourceType: "pod",
		ProviderID:   "kube-system/coredns",
	})
	if err != nil {
		t.Fatalf("kube-system/coredns was unexpectedly deleted: %v", err)
	}
	if storedCoreDNS == nil {
		t.Fatalf("kube-system/coredns should remain present in state store")
	}
}

// ---------------------------------------------------------------------------
// 10. CONCURRENT RECONCILIATION SAFETY
// ---------------------------------------------------------------------------

func TestReconciler_ConcurrentReconciliation(t *testing.T) {
	store := state.NewMemoryStore()
	reconciler := state.NewReconciler(store, nil, nil, nil)
	ctx := context.Background()

	const numWorkers = 20
	var wg sync.WaitGroup
	errCh := make(chan error, numWorkers)

	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			evID := fmt.Sprintf("ev-conc-%03d", workerID)
			podID := fmt.Sprintf("default/worker-pod-%d", workerID%5) // multiple workers hit same pod identity

			batch := state.ObservationBatch{
				WorkspaceID: "ws-concurrent-rec",
				Evidence: []*corev1.Evidence{
					newTestEvidence(evID, "kubernetes", "pod", podID, "CONTROL_PLANE_OBJECT", `{}`, time.Now().UTC()),
				},
			}

			if _, err := reconciler.Reconcile(ctx, batch); err != nil {
				errCh <- err
			}
		}(i)
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Fatalf("concurrent Reconcile failed: %v", err)
	}

	// Verify all 5 distinct pods are registered
	resources, err := store.ListResources(ctx, "ws-concurrent-rec")
	if err != nil {
		t.Fatalf("ListResources failed: %v", err)
	}
	if len(resources) != 5 {
		t.Errorf("expected 5 distinct resources after concurrent execution, got %d", len(resources))
	}

	// Verify all 20 evidence records were stored immutably
	evidence, err := store.ListEvidence(ctx, "ws-concurrent-rec")
	if err != nil {
		t.Fatalf("ListEvidence failed: %v", err)
	}
	if len(evidence) != numWorkers {
		t.Errorf("expected %d evidence records, got %d", numWorkers, len(evidence))
	}
}
