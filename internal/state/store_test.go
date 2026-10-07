package state_test

import (
	"context"
	"testing"
	"time"

	"github.com/aaradhychinche-alt/WhatBreaks/internal/state"
)

func newTestMemoryStore() state.Store {
	return state.NewMemoryStore()
}

// ---------------------------------------------------------------------------
// RESOURCE TESTS
// ---------------------------------------------------------------------------

func TestStore_Resource_CreateRegister(t *testing.T) {
	s := newTestMemoryStore()
	ctx := context.Background()

	now := time.Now().UTC()
	id := state.ResourceIdentity{
		Provider:     "kubernetes",
		ResourceType: "service",
		ProviderID:   "payments-api",
	}

	err := s.SaveResources(ctx, "ws-1", []state.Resource{
		{
			Identity:        id,
			FirstObservedAt: now,
			LastObservedAt:  now,
		},
	})
	if err != nil {
		t.Fatalf("SaveResources failed: %v", err)
	}

	res, err := s.GetResource(ctx, "ws-1", id)
	if err != nil {
		t.Fatalf("GetResource failed: %v", err)
	}

	if res.Identity != id {
		t.Errorf("expected identity %+v, got %+v", id, res.Identity)
	}
	if !res.FirstObservedAt.Equal(now) {
		t.Errorf("expected first observed %v, got %v", now, res.FirstObservedAt)
	}
}

func TestStore_Resource_WorkspaceIsolationAndCollision(t *testing.T) {
	s := newTestMemoryStore()
	ctx := context.Background()

	// Identical provider, resource_type, provider_id in two different customer workspaces
	id := state.ResourceIdentity{
		Provider:     "kubernetes",
		ResourceType: "pod",
		ProviderID:   "payments/api-pod-1",
	}

	t1 := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)

	_ = s.SaveResources(ctx, "workspace-A", []state.Resource{
		{Identity: id, FirstObservedAt: t1, LastObservedAt: t1},
	})
	_ = s.SaveResources(ctx, "workspace-B", []state.Resource{
		{Identity: id, FirstObservedAt: t2, LastObservedAt: t2},
	})

	// Workspace A lookup
	resA, err := s.GetResource(ctx, "workspace-A", id)
	if err != nil {
		t.Fatalf("failed to get resource in workspace-A: %v", err)
	}
	if !resA.FirstObservedAt.Equal(t1) {
		t.Errorf("workspace-A resource contaminated: got %v, expected %v", resA.FirstObservedAt, t1)
	}

	// Workspace B lookup
	resB, err := s.GetResource(ctx, "workspace-B", id)
	if err != nil {
		t.Fatalf("failed to get resource in workspace-B: %v", err)
	}
	if !resB.FirstObservedAt.Equal(t2) {
		t.Errorf("workspace-B resource contaminated: got %v, expected %v", resB.FirstObservedAt, t2)
	}

	// Workspace C (empty) must return ErrResourceNotFound
	_, err = s.GetResource(ctx, "workspace-C", id)
	if err == nil {
		t.Fatal("expected ErrResourceNotFound in workspace-C")
	}
}

func TestStore_Resource_RepeatedObservations(t *testing.T) {
	s := newTestMemoryStore()
	ctx := context.Background()

	id := state.ResourceIdentity{
		Provider:     "aws",
		ResourceType: "rds",
		ProviderID:   "arn:aws:rds:us-east-1:123:db",
	}

	firstTime := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	secondTime := time.Date(2026, 10, 2, 15, 0, 0, 0, time.UTC)

	_ = s.SaveResources(ctx, "ws-1", []state.Resource{
		{Identity: id, FirstObservedAt: firstTime, LastObservedAt: firstTime},
	})

	// Repeated observation must preserve first_observed_at and update last_observed_at
	_ = s.SaveResources(ctx, "ws-1", []state.Resource{
		{Identity: id, FirstObservedAt: secondTime, LastObservedAt: secondTime},
	})

	res, err := s.GetResource(ctx, "ws-1", id)
	if err != nil {
		t.Fatalf("GetResource failed: %v", err)
	}

	if !res.FirstObservedAt.Equal(firstTime) {
		t.Errorf("first_observed_at was overwritten: expected %v, got %v", firstTime, res.FirstObservedAt)
	}
	if !res.LastObservedAt.Equal(secondTime) {
		t.Errorf("last_observed_at was not updated: expected %v, got %v", secondTime, res.LastObservedAt)
	}
}

// ---------------------------------------------------------------------------
// EVIDENCE TESTS
// ---------------------------------------------------------------------------

func TestStore_Evidence_ImmutablePersistence(t *testing.T) {
	s := newTestMemoryStore()
	ctx := context.Background()

	evID := "550e8400-e29b-41d4-a716-446655440001"
	t1 := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 10, 2, 10, 0, 0, 0, time.UTC)

	ev1 := state.Evidence{
		ID:              evID,
		Source:          state.EvidenceSource{Provider: "kubernetes", Collector: "k8s-runtime"},
		ObservedAt:      t1,
		ObservationType: "RUNTIME_CONNECTION",
		Subject:         state.ResourceIdentity{Provider: "kubernetes", ResourceType: "pod", ProviderID: "api"},
		Data:            []byte(`{"port": 8080}`),
	}

	if err := s.SaveEvidence(ctx, "ws-1", []state.Evidence{ev1}); err != nil {
		t.Fatalf("SaveEvidence failed: %v", err)
	}

	// Attempting to overwrite existing historical evidence
	ev2 := state.Evidence{
		ID:              evID,
		Source:          state.EvidenceSource{Provider: "kubernetes", Collector: "k8s-runtime"},
		ObservedAt:      t2,
		ObservationType: "RUNTIME_CONNECTION",
		Subject:         state.ResourceIdentity{Provider: "kubernetes", ResourceType: "pod", ProviderID: "api"},
		Data:            []byte(`{"port": 9999}`), // modified data
	}

	if err := s.SaveEvidence(ctx, "ws-1", []state.Evidence{ev2}); err != nil {
		t.Fatalf("SaveEvidence second call failed: %v", err)
	}

	evidenceList, err := s.ListEvidence(ctx, "ws-1")
	if err != nil {
		t.Fatalf("ListEvidence failed: %v", err)
	}

	if len(evidenceList) != 1 {
		t.Fatalf("expected exactly 1 evidence entry, got %d", len(evidenceList))
	}

	// Must preserve original immutable payload
	if string(evidenceList[0].Data) != `{"port": 8080}` {
		t.Errorf("historical evidence was mutated! got %s", string(evidenceList[0].Data))
	}
	if !evidenceList[0].ObservedAt.Equal(t1) {
		t.Errorf("historical timestamp was mutated! got %v", evidenceList[0].ObservedAt)
	}
}

// ---------------------------------------------------------------------------
// RELATIONSHIP TESTS
// ---------------------------------------------------------------------------

func TestStore_Relationship_CreationAndRepeatedHandling(t *testing.T) {
	s := newTestMemoryStore()
	ctx := context.Background()

	src := state.ResourceIdentity{Provider: "kubernetes", ResourceType: "service", ProviderID: "orders"}
	tgt := state.ResourceIdentity{Provider: "kubernetes", ResourceType: "service", ProviderID: "catalog"}

	t1 := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 10, 3, 11, 0, 0, 0, time.UTC)

	rel1 := state.Relationship{
		Source:          src,
		Target:          tgt,
		Kind:            "DEPENDS_ON",
		Category:        "Dependency",
		FirstObservedAt: t1,
		LastObservedAt:  t1,
	}

	if err := s.SaveRelationships(ctx, "ws-1", []state.Relationship{rel1}); err != nil {
		t.Fatalf("SaveRelationships failed: %v", err)
	}

	// Re-observe later
	rel2 := state.Relationship{
		Source:          src,
		Target:          tgt,
		Kind:            "DEPENDS_ON",
		Category:        "Dependency",
		FirstObservedAt: t2,
		LastObservedAt:  t2,
	}
	if err := s.SaveRelationships(ctx, "ws-1", []state.Relationship{rel2}); err != nil {
		t.Fatalf("SaveRelationships re-observation failed: %v", err)
	}

	rels, err := s.ListRelationships(ctx, "ws-1")
	if err != nil {
		t.Fatalf("ListRelationships failed: %v", err)
	}

	if len(rels) != 1 {
		t.Fatalf("expected exactly 1 relationship, got %d", len(rels))
	}
	if !rels[0].FirstObservedAt.Equal(t1) {
		t.Errorf("expected first observed %v, got %v", t1, rels[0].FirstObservedAt)
	}
	if !rels[0].LastObservedAt.Equal(t2) {
		t.Errorf("expected last observed %v, got %v", t2, rels[0].LastObservedAt)
	}
}

// ---------------------------------------------------------------------------
// PROVENANCE TESTS
// ---------------------------------------------------------------------------

func TestStore_Provenance_ManyToManyAndIdempotency(t *testing.T) {
	s := newTestMemoryStore()
	ctx := context.Background()

	rel1 := state.RelationshipKey{
		Source: state.ResourceIdentity{Provider: "k8s", ResourceType: "pod", ProviderID: "a"},
		Target: state.ResourceIdentity{Provider: "k8s", ResourceType: "pod", ProviderID: "b"},
		Kind:   "DEPENDS_ON",
	}
	rel2 := state.RelationshipKey{
		Source: state.ResourceIdentity{Provider: "k8s", ResourceType: "pod", ProviderID: "a"},
		Target: state.ResourceIdentity{Provider: "k8s", ResourceType: "pod", ProviderID: "c"},
		Kind:   "DEPENDS_ON",
	}

	ev1 := "550e8400-e29b-41d4-a716-446655440001"
	ev2 := "550e8400-e29b-41d4-a716-446655440002"

	// 1. Rel1 supported by ev1 and ev2 (one relationship, multiple evidence)
	// 2. ev1 supports rel1 and rel2 (one evidence, multiple relationships)
	assocs := []state.ProvenanceAssociation{
		{Relationship: rel1, EvidenceID: ev1},
		{Relationship: rel1, EvidenceID: ev2},
		{Relationship: rel2, EvidenceID: ev1},
		// Duplicate (must be idempotent)
		{Relationship: rel1, EvidenceID: ev1},
	}

	if err := s.SaveProvenance(ctx, "ws-1", assocs); err != nil {
		t.Fatalf("SaveProvenance failed: %v", err)
	}

	// Check provenance for rel1
	evsForRel1, err := s.GetProvenanceForRelationship(ctx, "ws-1", rel1)
	if err != nil {
		t.Fatalf("GetProvenanceForRelationship failed: %v", err)
	}
	if len(evsForRel1) != 2 {
		t.Errorf("expected 2 evidence IDs for rel1, got %d (%v)", len(evsForRel1), evsForRel1)
	}

	// Check relationships for ev1
	relsForEv1, err := s.GetRelationshipsForEvidence(ctx, "ws-1", ev1)
	if err != nil {
		t.Fatalf("GetRelationshipsForEvidence failed: %v", err)
	}
	if len(relsForEv1) != 2 {
		t.Errorf("expected 2 relationships supported by ev1, got %d (%v)", len(relsForEv1), relsForEv1)
	}
}
