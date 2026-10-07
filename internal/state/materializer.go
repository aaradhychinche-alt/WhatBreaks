package state

import (
	"context"
	"fmt"
	"time"

	corev1 "github.com/aaradhychinche-alt/WhatBreaks/gen/go/wb/core/v1"
	"github.com/aaradhychinche-alt/WhatBreaks/internal/logging"
	"google.golang.org/grpc"
)

// CoreLoader defines the gRPC client contract required by Materializer.
type CoreLoader interface {
	LoadState(ctx context.Context, req *corev1.LoadStateRequest, opts ...grpc.CallOption) (*corev1.LoadStateResponse, error)
}

// Materializer loads authoritative state from Store and materializes it into the Rust Core Engine.
type Materializer struct {
	store  Store
	client CoreLoader
	logger logging.Logger
}

// NewMaterializer constructs a Materializer instance.
func NewMaterializer(store Store, client CoreLoader, logger logging.Logger) *Materializer {
	if logger == nil {
		logger = logging.NewJSONLogger(nil, logging.LevelInfo, "state-materializer")
	}
	return &Materializer{
		store:  store,
		client: client,
		logger: logger,
	}
}

// Materialize loads the full assembled state for workspaceID from the store and
// pushes it to the Rust Core Engine over gRPC.
func (m *Materializer) Materialize(ctx context.Context, workspaceID string) (*corev1.LoadStateResponse, error) {
	if m.client == nil {
		return nil, fmt.Errorf("materializer: core client is not configured")
	}

	start := time.Now()
	state, err := m.store.LoadAssembledState(ctx, workspaceID)
	if err != nil {
		m.logger.Error("Failed to load assembled state from store",
			"workspace_id", workspaceID,
			"error", err.Error(),
		)
		return nil, fmt.Errorf("materializer failed to load state: %w", err)
	}

	protoReq := &corev1.LoadStateRequest{
		WorkspaceId:   workspaceID,
		Relationships: make([]*corev1.Relationship, 0, len(state.Relationships)),
		Associations:  make([]*corev1.RelationshipEvidence, 0, len(state.Relationships)),
		Evidence:      make([]*corev1.Evidence, 0, len(state.Evidence)),
	}

	relCatMap := make(map[RelationshipKey]string, len(state.Relationships))
	// 1. Convert relationships
	for _, rel := range state.Relationships {
		relCatMap[rel.Key()] = rel.Category
		protoReq.Relationships = append(protoReq.Relationships, &corev1.Relationship{
			Source: &corev1.ResourceIdentity{
				Provider:     rel.Source.Provider,
				ResourceType: rel.Source.ResourceType,
				ProviderId:   rel.Source.ProviderID,
			},
			Target: &corev1.ResourceIdentity{
				Provider:     rel.Target.Provider,
				ResourceType: rel.Target.ResourceType,
				ProviderId:   rel.Target.ProviderID,
			},
			Kind:     rel.Kind,
			Category: rel.Category,
		})
	}

	// 2. Convert provenance associations
	for relKey, evIDs := range state.Provenance {
		protoReq.Associations = append(protoReq.Associations, &corev1.RelationshipEvidence{
			Relationship: &corev1.Relationship{
				Source: &corev1.ResourceIdentity{
					Provider:     relKey.Source.Provider,
					ResourceType: relKey.Source.ResourceType,
					ProviderId:   relKey.Source.ProviderID,
				},
				Target: &corev1.ResourceIdentity{
					Provider:     relKey.Target.Provider,
					ResourceType: relKey.Target.ResourceType,
					ProviderId:   relKey.Target.ProviderID,
				},
				Kind:     relKey.Kind,
				Category: relCatMap[relKey],
			},
			EvidenceIds: evIDs,
		})
	}

	// 3. Convert evidence
	for _, ev := range state.Evidence {
		protoReq.Evidence = append(protoReq.Evidence, &corev1.Evidence{
			Id: ev.ID,
			Source: &corev1.EvidenceSource{
				Provider:  ev.Source.Provider,
				Collector: ev.Source.Collector,
			},
			ObservedAt:      ev.ObservedAt.Format(time.RFC3339Nano),
			ObservationType: ev.ObservationType,
			Subject: &corev1.ResourceIdentity{
				Provider:     ev.Subject.Provider,
				ResourceType: ev.Subject.ResourceType,
				ProviderId:   ev.Subject.ProviderID,
			},
			Data: ev.Data,
		})
	}

	resp, err := m.client.LoadState(ctx, protoReq)
	duration := time.Since(start)

	if err != nil {
		m.logger.Error("Failed to materialize state to Core Engine",
			"workspace_id", workspaceID,
			"duration_ms", duration.Milliseconds(),
			"error", err.Error(),
		)
		return nil, fmt.Errorf("materializer failed to load state into core: %w", err)
	}

	m.logger.Info("Materialized state to Core Engine successfully",
		"workspace_id", workspaceID,
		"relationships_loaded", resp.RelationshipsLoaded,
		"evidence_loaded", resp.EvidenceLoaded,
		"provenance_links_loaded", resp.ProvenanceLinksLoaded,
		"duration_ms", duration.Milliseconds(),
	)

	return resp, nil
}
