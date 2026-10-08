package state

import (
	"context"
	"errors"

	corev1 "github.com/aaradhychinche-alt/WhatBreaks/gen/go/wb/core/v1"
	"github.com/aaradhychinche-alt/WhatBreaks/internal/logging"
	"google.golang.org/grpc"
)

var (
	// ErrNilBatch is returned when an observation batch is nil.
	ErrNilBatch = errors.New("state assembler: observation batch cannot be nil")

	// ErrEmptyWorkspace is returned when workspace ID is missing.
	ErrEmptyWorkspace = errors.New("state assembler: workspace_id cannot be empty")
)

// DiscoveryRunner defines the gRPC transport contract required to invoke Rust DiscoveryEngine.
type DiscoveryRunner interface {
	RunDiscovery(ctx context.Context, req *corev1.RunDiscoveryRequest, opts ...grpc.CallOption) (*corev1.RunDiscoveryResponse, error)
}

// IngestionResult summarizes the outcome of processing an observation batch.
type IngestionResult struct {
	WorkspaceID          string `json:"workspace_id"`
	EvidenceCount        int    `json:"evidence_count"`
	ResourcesRegistered  int    `json:"resources_registered"`
	DiscoveredCount      int    `json:"discovered_count"`
	InsufficientCount    int    `json:"insufficient_count"`
	ConflictCount        int    `json:"conflict_count"`
	InvalidCount         int    `json:"invalid_count"`
	ProvenanceLinksAdded int    `json:"provenance_links_added"`
}

// StateAssembler coordinates ingestion of normalized collector observations, resource registration,
// discovery invocation, relationship/provenance durability, and materialization into Rust Core.
// It implements Reconciler and provides backward compatibility for Ingest/IngestAndMaterialize.
type StateAssembler struct {
	reconciler   Reconciler
	store        Store
	discovery    DiscoveryRunner
	materializer *Materializer
	logger       logging.Logger
}

// NewStateAssembler constructs a StateAssembler instance.
func NewStateAssembler(
	store Store,
	discovery DiscoveryRunner,
	materializer *Materializer,
	logger logging.Logger,
) *StateAssembler {
	if logger == nil {
		logger = logging.NewJSONLogger(nil, logging.LevelInfo, "state-assembler")
	}
	reconciler := NewReconciler(store, discovery, materializer, logger)
	return &StateAssembler{
		reconciler:   reconciler,
		store:        store,
		discovery:    discovery,
		materializer: materializer,
		logger:       logger,
	}
}

// Ingest processes a batch of normalized evidence observations for workspaceID using the underlying Reconciler.
func (a *StateAssembler) Ingest(ctx context.Context, batch ObservationBatch) (*IngestionResult, error) {
	recRes, err := a.reconciler.Reconcile(ctx, batch)
	if err != nil {
		return nil, err
	}
	return &IngestionResult{
		WorkspaceID:          recRes.WorkspaceID,
		EvidenceCount:        recRes.EvidenceCount,
		ResourcesRegistered:  recRes.ResourcesCreated + recRes.ResourcesUpdated,
		DiscoveredCount:      recRes.DiscoveredCount,
		InsufficientCount:    recRes.InsufficientCount,
		ConflictCount:        recRes.ConflictsCount,
		InvalidCount:         recRes.InvalidCount,
		ProvenanceLinksAdded: recRes.ProvenanceLinksAdded,
	}, nil
}

// IngestAndMaterialize ingests an observation batch and refreshes the Rust Core Engine state.
func (a *StateAssembler) IngestAndMaterialize(ctx context.Context, batch ObservationBatch) (*IngestionResult, error) {
	recRes, err := a.reconciler.ReconcileAndMaterialize(ctx, batch)
	if err != nil {
		return nil, err
	}
	return &IngestionResult{
		WorkspaceID:          recRes.WorkspaceID,
		EvidenceCount:        recRes.EvidenceCount,
		ResourcesRegistered:  recRes.ResourcesCreated + recRes.ResourcesUpdated,
		DiscoveredCount:      recRes.DiscoveredCount,
		InsufficientCount:    recRes.InsufficientCount,
		ConflictCount:        recRes.ConflictsCount,
		InvalidCount:         recRes.InvalidCount,
		ProvenanceLinksAdded: recRes.ProvenanceLinksAdded,
	}, nil
}

// Reconcile processes a batch of normalized observations using the Reconciler interface.
func (a *StateAssembler) Reconcile(ctx context.Context, batch ObservationBatch) (*ReconciliationResult, error) {
	return a.reconciler.Reconcile(ctx, batch)
}

// ReconcileAndMaterialize processes a batch and refreshes Rust Core state using the Reconciler interface.
func (a *StateAssembler) ReconcileAndMaterialize(ctx context.Context, batch ObservationBatch) (*ReconciliationResult, error) {
	return a.reconciler.ReconcileAndMaterialize(ctx, batch)
}
