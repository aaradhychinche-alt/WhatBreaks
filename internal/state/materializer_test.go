package state_test

import (
	"context"
	"errors"
	"testing"
	"time"

	corev1 "github.com/aaradhychinche-alt/WhatBreaks/gen/go/wb/core/v1"
	"github.com/aaradhychinche-alt/WhatBreaks/internal/state"
	"google.golang.org/grpc"
)

type mockCoreLoader struct {
	lastReq   *corev1.LoadStateRequest
	resp      *corev1.LoadStateResponse
	err       error
	callCount int
}

func (m *mockCoreLoader) LoadState(ctx context.Context, req *corev1.LoadStateRequest, opts ...grpc.CallOption) (*corev1.LoadStateResponse, error) {
	m.callCount++
	m.lastReq = req
	if m.err != nil {
		return nil, m.err
	}
	if m.resp != nil {
		return m.resp, nil
	}
	return &corev1.LoadStateResponse{
		WorkspaceId:           req.WorkspaceId,
		RelationshipsLoaded:   uint32(len(req.Relationships)),
		EvidenceLoaded:        uint32(len(req.Evidence)),
		ProvenanceLinksLoaded: uint32(len(req.Associations)),
	}, nil
}

func TestMaterializer_Materialize_Success(t *testing.T) {
	ctx := context.Background()
	store := state.NewMemoryStore()
	workspaceID := "ws-mat-test-1"

	// Setup data in store
	res1 := state.Resource{
		WorkspaceID: workspaceID,
		Identity: state.ResourceIdentity{
			Provider:     "kubernetes",
			ResourceType: "service",
			ProviderID:   "frontend",
		},
	}
	res2 := state.Resource{
		WorkspaceID: workspaceID,
		Identity: state.ResourceIdentity{
			Provider:     "kubernetes",
			ResourceType: "service",
			ProviderID:   "backend",
		},
	}
	if err := store.SaveResources(ctx, workspaceID, []state.Resource{res1, res2}); err != nil {
		t.Fatalf("failed to save resources: %v", err)
	}

	evID := "11111111-1111-1111-1111-111111111111"
	ev := state.Evidence{
		WorkspaceID: workspaceID,
		ID:          evID,
		Source: state.EvidenceSource{
			Provider:  "kubernetes",
			Collector: "k8s-informer",
		},
		ObservedAt:      time.Now().UTC(),
		ObservationType: "CONFIG_REFERENCE",
		Subject:         res1.Identity,
		Data:            []byte(`{"spec": "clusterIP"}`),
	}
	if err := store.SaveEvidence(ctx, workspaceID, []state.Evidence{ev}); err != nil {
		t.Fatalf("failed to save evidence: %v", err)
	}

	rel := state.Relationship{
		WorkspaceID:     workspaceID,
		Source:          res1.Identity,
		Target:          res2.Identity,
		Category:        "network",
		Kind:            "routes_to",
		FirstObservedAt: time.Now().UTC(),
		LastObservedAt:  time.Now().UTC(),
	}
	if err := store.SaveRelationships(ctx, workspaceID, []state.Relationship{rel}); err != nil {
		t.Fatalf("failed to save relationships: %v", err)
	}

	prov := state.ProvenanceAssociation{
		WorkspaceID:  workspaceID,
		Relationship: rel.Key(),
		EvidenceID:   evID,
	}
	if err := store.SaveProvenance(ctx, workspaceID, []state.ProvenanceAssociation{prov}); err != nil {
		t.Fatalf("failed to save provenance: %v", err)
	}

	loader := &mockCoreLoader{}
	mat := state.NewMaterializer(store, loader, nil)

	res, err := mat.Materialize(ctx, workspaceID)
	if err != nil {
		t.Fatalf("Materialize failed: %v", err)
	}

	if res.RelationshipsLoaded != 1 {
		t.Errorf("expected 1 relationship loaded, got %d", res.RelationshipsLoaded)
	}
	if res.EvidenceLoaded != 1 {
		t.Errorf("expected 1 evidence loaded, got %d", res.EvidenceLoaded)
	}
	if res.ProvenanceLinksLoaded != 1 {
		t.Errorf("expected 1 provenance entry loaded, got %d", res.ProvenanceLinksLoaded)
	}

	// Verify request sent to loader
	req := loader.lastReq
	if req == nil {
		t.Fatalf("expected lastReq to be non-nil")
	}
	if req.WorkspaceId != workspaceID {
		t.Errorf("expected workspace ID %q, got %q", workspaceID, req.WorkspaceId)
	}
	if len(req.Relationships) != 1 {
		t.Errorf("expected 1 proto relationship, got %d", len(req.Relationships))
	}
	if req.Relationships[0].Source.ProviderId != "frontend" {
		t.Errorf("unexpected relationship source: %v", req.Relationships[0].Source)
	}
	if len(req.Evidence) != 1 {
		t.Errorf("expected 1 proto evidence, got %d", len(req.Evidence))
	}
	if req.Evidence[0].Id != evID {
		t.Errorf("expected proto evidence ID %s, got %s", evID, req.Evidence[0].Id)
	}
	if len(req.Associations) != 1 {
		t.Errorf("expected 1 proto association, got %d", len(req.Associations))
	}
}

func TestMaterializer_Errors(t *testing.T) {
	ctx := context.Background()

	t.Run("nil loader", func(t *testing.T) {
		mat := state.NewMaterializer(state.NewMemoryStore(), nil, nil)
		_, err := mat.Materialize(ctx, "ws-1")
		if err == nil {
			t.Fatalf("expected error when loader is nil")
		}
	})

	t.Run("loader returns error", func(t *testing.T) {
		loader := &mockCoreLoader{err: errors.New("rpc connection refused")}
		mat := state.NewMaterializer(state.NewMemoryStore(), loader, nil)
		_, err := mat.Materialize(ctx, "ws-1")
		if err == nil {
			t.Fatalf("expected error when loader fails")
		}
	})
}
