package state

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

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
type StateAssembler struct {
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
	return &StateAssembler{
		store:        store,
		discovery:    discovery,
		materializer: materializer,
		logger:       logger,
	}
}

// Ingest processes a batch of normalized evidence observations for workspaceID.
func (a *StateAssembler) Ingest(ctx context.Context, batch ObservationBatch) (*IngestionResult, error) {
	ws := strings.TrimSpace(batch.WorkspaceID)
	if ws == "" {
		return nil, ErrEmptyWorkspace
	}
	if len(batch.Evidence) == 0 {
		return &IngestionResult{WorkspaceID: ws}, nil
	}

	start := time.Now()
	result := &IngestionResult{
		WorkspaceID:   ws,
		EvidenceCount: len(batch.Evidence),
	}

	// 1. Validate & Transform Evidence + Extract Subject Resources
	domainEvidence := make([]Evidence, 0, len(batch.Evidence))
	resourcesMap := make(map[string]Resource)

	for _, protoEv := range batch.Evidence {
		if protoEv == nil {
			continue
		}
		if protoEv.Id == "" {
			return nil, fmt.Errorf("state assembler: evidence missing required id")
		}
		if protoEv.Subject == nil || protoEv.Subject.Provider == "" || protoEv.Subject.ResourceType == "" || protoEv.Subject.ProviderId == "" {
			return nil, fmt.Errorf("state assembler: evidence %s missing required subject identity", protoEv.Id)
		}
		if protoEv.Source == nil || protoEv.Source.Provider == "" || protoEv.Source.Collector == "" {
			return nil, fmt.Errorf("state assembler: evidence %s missing required source", protoEv.Id)
		}

		observedAt, err := time.Parse(time.RFC3339Nano, protoEv.ObservedAt)
		if err != nil {
			observedAt, err = time.Parse(time.RFC3339, protoEv.ObservedAt)
			if err != nil {
				observedAt = time.Now().UTC()
			}
		}

		subjectID := ResourceIdentity{
			Provider:     protoEv.Subject.Provider,
			ResourceType: protoEv.Subject.ResourceType,
			ProviderID:   protoEv.Subject.ProviderId,
		}

		domainEvidence = append(domainEvidence, Evidence{
			WorkspaceID: ws,
			ID:          protoEv.Id,
			Source: EvidenceSource{
				Provider:  protoEv.Source.Provider,
				Collector: protoEv.Source.Collector,
			},
			ObservedAt:      observedAt,
			ObservationType: protoEv.ObservationType,
			Subject:         subjectID,
			Data:            protoEv.Data,
		})

		// Track subject resource
		key := subjectID.String()
		if existing, exists := resourcesMap[key]; !exists {
			resourcesMap[key] = Resource{
				WorkspaceID:     ws,
				Identity:        subjectID,
				FirstObservedAt: observedAt,
				LastObservedAt:  observedAt,
			}
		} else {
			if observedAt.After(existing.LastObservedAt) {
				existing.LastObservedAt = observedAt
				resourcesMap[key] = existing
			}
		}
	}

	// 2. Persist Evidence Records
	if err := a.store.SaveEvidence(ctx, ws, domainEvidence); err != nil {
		a.logger.Error("Failed to persist evidence observations",
			"workspace_id", ws,
			"evidence_count", len(domainEvidence),
			"error", err.Error(),
		)
		return nil, fmt.Errorf("failed to save evidence: %w", err)
	}

	// 3. Persist Subject Resources
	resourcesToSave := make([]Resource, 0, len(resourcesMap))
	for _, r := range resourcesMap {
		resourcesToSave = append(resourcesToSave, r)
	}
	if err := a.store.SaveResources(ctx, ws, resourcesToSave); err != nil {
		a.logger.Error("Failed to persist subject resources",
			"workspace_id", ws,
			"resource_count", len(resourcesToSave),
			"error", err.Error(),
		)
		return nil, fmt.Errorf("failed to save resources: %w", err)
	}
	result.ResourcesRegistered = len(resourcesToSave)

	// 4. Invoke Rust DiscoveryEngine if configured
	if a.discovery != nil {
		discReq := &corev1.RunDiscoveryRequest{Evidence: batch.Evidence}
		discResp, err := a.discovery.RunDiscovery(ctx, discReq)
		if err != nil {
			a.logger.Error("Discovery execution failed in Core Engine",
				"workspace_id", ws,
				"evidence_count", len(batch.Evidence),
				"error", err.Error(),
			)
			return nil, fmt.Errorf("discovery execution failed: %w", err)
		}

		var discoveredRels []Relationship
		var provenanceAssocs []ProvenanceAssociation
		endpointResources := make(map[string]Resource)

		now := time.Now().UTC()

		for _, res := range discResp.Results {
			if res == nil {
				continue
			}

			if disc := res.GetDiscovered(); disc != nil {
				result.DiscoveredCount++
				relProto := disc.Relationship
				if relProto == nil || relProto.Source == nil || relProto.Target == nil {
					continue
				}

				src := ResourceIdentity{
					Provider:     relProto.Source.Provider,
					ResourceType: relProto.Source.ResourceType,
					ProviderID:   relProto.Source.ProviderId,
				}
				tgt := ResourceIdentity{
					Provider:     relProto.Target.Provider,
					ResourceType: relProto.Target.ResourceType,
					ProviderID:   relProto.Target.ProviderId,
				}

				rel := Relationship{
					WorkspaceID:     ws,
					Source:          src,
					Target:          tgt,
					Kind:            relProto.Kind,
					Category:        relProto.Category,
					FirstObservedAt: now,
					LastObservedAt:  now,
				}
				discoveredRels = append(discoveredRels, rel)

				// Ensure endpoints are registered
				endpointResources[src.String()] = Resource{
					WorkspaceID:     ws,
					Identity:        src,
					FirstObservedAt: now,
					LastObservedAt:  now,
				}
				endpointResources[tgt.String()] = Resource{
					WorkspaceID:     ws,
					Identity:        tgt,
					FirstObservedAt: now,
					LastObservedAt:  now,
				}

				// Associate supporting evidence
				relKey := rel.Key()
				for _, evID := range disc.SupportingEvidenceIds {
					provenanceAssocs = append(provenanceAssocs, ProvenanceAssociation{
						WorkspaceID:  ws,
						Relationship: relKey,
						EvidenceID:   evID,
					})
					result.ProvenanceLinksAdded++
				}

			} else if conf := res.GetConflict(); conf != nil {
				result.ConflictCount++
				a.logger.Warn("Discovery conflict observed",
					"workspace_id", ws,
					"description", conf.Description,
				)
				_ = a.store.SaveConflict(ctx, DiscoveryConflict{
					WorkspaceID: ws,
					Description: conf.Description,
					ObservedAt:  now,
				})

			} else if res.GetInsufficient() != nil {
				result.InsufficientCount++
				a.logger.Debug("Evidence insufficient for relationship derivation",
					"workspace_id", ws,
				)

			} else if inv := res.GetInvalid(); inv != nil {
				result.InvalidCount++
				a.logger.Warn("Discovery encountered invalid evidence observation",
					"workspace_id", ws,
					"description", inv.Description,
				)
			}
		}

		// 5. Persist Discovered Relationships, Endpoints & Provenance
		if len(endpointResources) > 0 {
			epList := make([]Resource, 0, len(endpointResources))
			for _, r := range endpointResources {
				epList = append(epList, r)
			}
			_ = a.store.SaveResources(ctx, ws, epList)
		}

		if len(discoveredRels) > 0 {
			if err := a.store.SaveRelationships(ctx, ws, discoveredRels); err != nil {
				a.logger.Error("Failed to persist discovered relationships",
					"workspace_id", ws,
					"error", err.Error(),
				)
				return nil, fmt.Errorf("failed to save relationships: %w", err)
			}
		}

		if len(provenanceAssocs) > 0 {
			if err := a.store.SaveProvenance(ctx, ws, provenanceAssocs); err != nil {
				a.logger.Error("Failed to persist provenance associations",
					"workspace_id", ws,
					"error", err.Error(),
				)
				return nil, fmt.Errorf("failed to save provenance: %w", err)
			}
		}
	}

	duration := time.Since(start)
	a.logger.Info("Observation batch ingested successfully",
		"workspace_id", ws,
		"evidence_count", result.EvidenceCount,
		"resources_registered", result.ResourcesRegistered,
		"discovered_count", result.DiscoveredCount,
		"conflict_count", result.ConflictCount,
		"duration_ms", duration.Milliseconds(),
	)

	return result, nil
}

// IngestAndMaterialize ingests an observation batch and refreshes the Rust Core Engine state.
func (a *StateAssembler) IngestAndMaterialize(ctx context.Context, batch ObservationBatch) (*IngestionResult, error) {
	res, err := a.Ingest(ctx, batch)
	if err != nil {
		return nil, err
	}

	if a.materializer != nil {
		if _, err := a.materializer.Materialize(ctx, batch.WorkspaceID); err != nil {
			a.logger.Error("Post-ingestion state materialization failed",
				"workspace_id", batch.WorkspaceID,
				"error", err.Error(),
			)
			return res, fmt.Errorf("materialization failed: %w", err)
		}
	}

	return res, nil
}
