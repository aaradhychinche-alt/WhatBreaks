package state

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	corev1 "github.com/aaradhychinche-alt/WhatBreaks/gen/go/wb/core/v1"
	"github.com/aaradhychinche-alt/WhatBreaks/internal/logging"
)

// Reconciler is the provider-agnostic contract for reconciling normalized observation
// batches with persistent workspace state.
type Reconciler interface {
	// Reconcile processes a batch of normalized evidence observations, synchronizing
	// resources, evidence, and discovered relationships while adhering to lifecycle,
	// immutability, and workspace-isolation invariants.
	Reconcile(ctx context.Context, batch ObservationBatch) (*ReconciliationResult, error)

	// ReconcileAndMaterialize reconciles an observation batch and refreshes the Rust Core Engine state.
	ReconcileAndMaterialize(ctx context.Context, batch ObservationBatch) (*ReconciliationResult, error)
}

// ReconciliationResult provides detailed lifecycle and state synchronization telemetry.
type ReconciliationResult struct {
	WorkspaceID          string        `json:"workspace_id"`
	EvidenceCount        int           `json:"evidence_count"`
	ResourcesCreated     int           `json:"resources_created"`
	ResourcesUpdated     int           `json:"resources_updated"`
	ResourcesTotal       int           `json:"resources_total"`
	RelationshipsCreated int           `json:"relationships_created"`
	RelationshipsUpdated int           `json:"relationships_updated"`
	RelationshipsTotal   int           `json:"relationships_total"`
	DiscoveredCount      int           `json:"discovered_count"`
	ProvenanceLinksAdded int           `json:"provenance_links_added"`
	ConflictsCount       int           `json:"conflicts_count"`
	InsufficientCount    int           `json:"insufficient_count"`
	InvalidCount         int           `json:"invalid_count"`
	Duration             time.Duration `json:"duration"`
}

// DefaultReconciler coordinates observation ingestion, resource lifecycle tracking,
// relationship reconciliation, evidence immutability, and state materialization.
//
// Lifecycle & Deletion Guarantees:
// 1. Missing observation is NEVER interpreted as deletion.
// 2. No automatic reaper or timestamp-based tombstoning exists in v1.
// 3. No Inactive state is introduced (history preserves Supported vs Unknown).
// 4. Evidence records are strictly immutable.
// 5. Stable ResourceIdentity is preserved across updates.
type DefaultReconciler struct {
	store        Store
	discovery    DiscoveryRunner
	materializer *Materializer
	logger       logging.Logger

	mu           sync.Mutex
	workspaceMus map[string]*sync.Mutex
}

// NewReconciler constructs a DefaultReconciler instance.
func NewReconciler(
	store Store,
	discovery DiscoveryRunner,
	materializer *Materializer,
	logger logging.Logger,
) *DefaultReconciler {
	if logger == nil {
		logger = logging.NewJSONLogger(nil, logging.LevelInfo, "state-reconciler")
	}
	return &DefaultReconciler{
		store:        store,
		discovery:    discovery,
		materializer: materializer,
		logger:       logger,
		workspaceMus: make(map[string]*sync.Mutex),
	}
}

// getWorkspaceLock returns a dedicated mutex for workspaceID, enabling concurrent
// reconciliations across distinct workspaces while safely serializing concurrent
// reconciliations targeting the same workspace.
func (r *DefaultReconciler) getWorkspaceLock(ws string) *sync.Mutex {
	r.mu.Lock()
	defer r.mu.Unlock()
	lock, ok := r.workspaceMus[ws]
	if !ok {
		lock = &sync.Mutex{}
		r.workspaceMus[ws] = lock
	}
	return lock
}

// Reconcile processes a batch of normalized evidence observations for workspaceID.
func (r *DefaultReconciler) Reconcile(ctx context.Context, batch ObservationBatch) (*ReconciliationResult, error) {
	ws := strings.TrimSpace(batch.WorkspaceID)
	if ws == "" {
		return nil, ErrEmptyWorkspace
	}

	wsLock := r.getWorkspaceLock(ws)
	wsLock.Lock()
	defer wsLock.Unlock()

	start := time.Now()
	result := &ReconciliationResult{
		WorkspaceID:   ws,
		EvidenceCount: len(batch.Evidence),
	}

	if len(batch.Evidence) == 0 {
		// Empty batch does NOT delete any state
		if existingRes, err := r.store.ListResources(ctx, ws); err == nil {
			result.ResourcesTotal = len(existingRes)
		}
		if existingRels, err := r.store.ListRelationships(ctx, ws); err == nil {
			result.RelationshipsTotal = len(existingRels)
		}
		result.Duration = time.Since(start)
		return result, nil
	}

	// 1. Validate & Transform Evidence + Extract Subject Resources
	domainEvidence := make([]Evidence, 0, len(batch.Evidence))
	incomingSubjectResources := make(map[string]struct {
		identity ResourceIdentity
		time     time.Time
	})

	for _, protoEv := range batch.Evidence {
		if protoEv == nil {
			continue
		}
		if protoEv.Id == "" {
			return nil, fmt.Errorf("reconciler: evidence missing required id")
		}
		if protoEv.Subject == nil || protoEv.Subject.Provider == "" || protoEv.Subject.ResourceType == "" || protoEv.Subject.ProviderId == "" {
			return nil, fmt.Errorf("reconciler: evidence %s missing required subject identity", protoEv.Id)
		}
		if protoEv.Source == nil || protoEv.Source.Provider == "" || protoEv.Source.Collector == "" {
			return nil, fmt.Errorf("reconciler: evidence %s missing required source", protoEv.Id)
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

		key := subjectID.String()
		if existing, ok := incomingSubjectResources[key]; !ok || observedAt.After(existing.time) {
			incomingSubjectResources[key] = struct {
				identity ResourceIdentity
				time     time.Time
			}{identity: subjectID, time: observedAt}
		}
	}

	// 2. Persist Evidence Records Immutably
	// Historical evidence is strictly immutable. If an evidence ID already exists,
	// it is preserved without modification (idempotent).
	if err := r.store.SaveEvidence(ctx, ws, domainEvidence); err != nil {
		r.logger.Error("Failed to persist immutable evidence observations",
			"workspace_id", ws,
			"evidence_count", len(domainEvidence),
			"error", err.Error(),
		)
		return nil, fmt.Errorf("failed to save evidence: %w", err)
	}

	// 3. Reconcile Subject Resources
	// Distinguish newly discovered resources from previously observed resources
	var resourcesToSave []Resource
	for _, inc := range incomingSubjectResources {
		existing, err := r.store.GetResource(ctx, ws, inc.identity)
		if err != nil {
			if errors.Is(err, ErrResourceNotFound) {
				// Newly observed resource: FirstObservedAt = LastObservedAt = inc.time
				result.ResourcesCreated++
				resourcesToSave = append(resourcesToSave, Resource{
					WorkspaceID:     ws,
					Identity:        inc.identity,
					FirstObservedAt: inc.time,
					LastObservedAt:  inc.time,
				})
			} else {
				return nil, fmt.Errorf("failed to inspect resource %s: %w", inc.identity, err)
			}
		} else {
			// Previously observed resource: preserve FirstObservedAt, update LastObservedAt
			result.ResourcesUpdated++
			lastObs := existing.LastObservedAt
			if inc.time.After(lastObs) {
				lastObs = inc.time
			}
			resourcesToSave = append(resourcesToSave, Resource{
				WorkspaceID:     ws,
				Identity:        existing.Identity,
				FirstObservedAt: existing.FirstObservedAt,
				LastObservedAt:  lastObs,
			})
		}
	}

	if len(resourcesToSave) > 0 {
		if err := r.store.SaveResources(ctx, ws, resourcesToSave); err != nil {
			r.logger.Error("Failed to persist reconciled subject resources",
				"workspace_id", ws,
				"resource_count", len(resourcesToSave),
				"error", err.Error(),
			)
			return nil, fmt.Errorf("failed to save resources: %w", err)
		}
	}

	// 4. Discovery Execution & Relationship Reconciliation
	if r.discovery != nil {
		discReq := &corev1.RunDiscoveryRequest{Evidence: batch.Evidence}
		discResp, err := r.discovery.RunDiscovery(ctx, discReq)
		if err != nil {
			r.logger.Error("Discovery execution failed in Core Engine",
				"workspace_id", ws,
				"evidence_count", len(batch.Evidence),
				"error", err.Error(),
			)
			return nil, fmt.Errorf("discovery execution failed: %w", err)
		}

		now := time.Now().UTC()
		var discoveredRels []Relationship
		var provenanceAssocs []ProvenanceAssociation
		endpointResourcesMap := make(map[string]ResourceIdentity)

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

				endpointResourcesMap[src.String()] = src
				endpointResourcesMap[tgt.String()] = tgt

				relKey := RelationshipKey{Source: src, Target: tgt, Kind: relProto.Kind}

				// Check existing relationship in store to distinguish created vs updated
				existingRel, err := r.store.GetRelationship(ctx, ws, relKey)
				if err != nil {
					if errors.Is(err, ErrRelationshipNotFound) {
						result.RelationshipsCreated++
						discoveredRels = append(discoveredRels, Relationship{
							WorkspaceID:     ws,
							Source:          src,
							Target:          tgt,
							Kind:            relProto.Kind,
							Category:        relProto.Category,
							FirstObservedAt: now,
							LastObservedAt:  now,
						})
					} else {
						return nil, fmt.Errorf("failed to inspect relationship %s: %w", relKey, err)
					}
				} else {
					result.RelationshipsUpdated++
					lastObs := existingRel.LastObservedAt
					if now.After(lastObs) {
						lastObs = now
					}
					discoveredRels = append(discoveredRels, Relationship{
						WorkspaceID:     ws,
						Source:          src,
						Target:          tgt,
						Kind:            relProto.Kind,
						Category:        relProto.Category,
						FirstObservedAt: existingRel.FirstObservedAt,
						LastObservedAt:  lastObs,
					})
				}

				for _, evID := range disc.SupportingEvidenceIds {
					provenanceAssocs = append(provenanceAssocs, ProvenanceAssociation{
						WorkspaceID:  ws,
						Relationship: relKey,
						EvidenceID:   evID,
					})
					result.ProvenanceLinksAdded++
				}

			} else if conf := res.GetConflict(); conf != nil {
				result.ConflictsCount++
				r.logger.Warn("Discovery conflict observed",
					"workspace_id", ws,
					"description", conf.Description,
				)
				_ = r.store.SaveConflict(ctx, DiscoveryConflict{
					WorkspaceID: ws,
					Description: conf.Description,
					ObservedAt:  now,
				})

			} else if res.GetInsufficient() != nil {
				result.InsufficientCount++
				r.logger.Debug("Evidence insufficient for relationship derivation",
					"workspace_id", ws,
				)

			} else if inv := res.GetInvalid(); inv != nil {
				result.InvalidCount++
				r.logger.Warn("Discovery encountered invalid evidence observation",
					"workspace_id", ws,
					"description", inv.Description,
				)
			}
		}

		// Reconcile relationship endpoint resources
		var endpointsToSave []Resource
		for _, epID := range endpointResourcesMap {
			existing, err := r.store.GetResource(ctx, ws, epID)
			if err != nil {
				if errors.Is(err, ErrResourceNotFound) {
					result.ResourcesCreated++
					endpointsToSave = append(endpointsToSave, Resource{
						WorkspaceID:     ws,
						Identity:        epID,
						FirstObservedAt: now,
						LastObservedAt:  now,
					})
				}
			} else {
				// If not already updated in incoming subject resources, update LastObservedAt
				if _, ok := incomingSubjectResources[epID.String()]; !ok {
					result.ResourcesUpdated++
					lastObs := existing.LastObservedAt
					if now.After(lastObs) {
						lastObs = now
					}
					endpointsToSave = append(endpointsToSave, Resource{
						WorkspaceID:     ws,
						Identity:        existing.Identity,
						FirstObservedAt: existing.FirstObservedAt,
						LastObservedAt:  lastObs,
					})
				}
			}
		}

		if len(endpointsToSave) > 0 {
			if err := r.store.SaveResources(ctx, ws, endpointsToSave); err != nil {
				r.logger.Error("Failed to persist relationship endpoints",
					"workspace_id", ws,
					"error", err.Error(),
				)
				return nil, fmt.Errorf("failed to save endpoint resources: %w", err)
			}
		}

		if len(discoveredRels) > 0 {
			if err := r.store.SaveRelationships(ctx, ws, discoveredRels); err != nil {
				r.logger.Error("Failed to persist reconciled relationships",
					"workspace_id", ws,
					"error", err.Error(),
				)
				return nil, fmt.Errorf("failed to save relationships: %w", err)
			}
		}

		if len(provenanceAssocs) > 0 {
			if err := r.store.SaveProvenance(ctx, ws, provenanceAssocs); err != nil {
				r.logger.Error("Failed to persist provenance associations",
					"workspace_id", ws,
					"error", err.Error(),
				)
				return nil, fmt.Errorf("failed to save provenance: %w", err)
			}
		}
	}

	// 5. Query totals for telemetry
	if totalRes, err := r.store.ListResources(ctx, ws); err == nil {
		result.ResourcesTotal = len(totalRes)
	}
	if totalRels, err := r.store.ListRelationships(ctx, ws); err == nil {
		result.RelationshipsTotal = len(totalRels)
	}

	result.Duration = time.Since(start)
	r.logger.Info("Reconciliation completed successfully",
		"workspace_id", ws,
		"evidence_count", result.EvidenceCount,
		"resources_created", result.ResourcesCreated,
		"resources_updated", result.ResourcesUpdated,
		"resources_total", result.ResourcesTotal,
		"relationships_created", result.RelationshipsCreated,
		"relationships_updated", result.RelationshipsUpdated,
		"relationships_total", result.RelationshipsTotal,
		"duration_ms", result.Duration.Milliseconds(),
	)

	return result, nil
}

// ReconcileAndMaterialize reconciles an observation batch and refreshes the Rust Core Engine state.
func (r *DefaultReconciler) ReconcileAndMaterialize(ctx context.Context, batch ObservationBatch) (*ReconciliationResult, error) {
	res, err := r.Reconcile(ctx, batch)
	if err != nil {
		return nil, err
	}

	if r.materializer != nil {
		if _, err := r.materializer.Materialize(ctx, batch.WorkspaceID); err != nil {
			r.logger.Error("Post-reconciliation state materialization failed",
				"workspace_id", batch.WorkspaceID,
				"error", err.Error(),
			)
			return res, fmt.Errorf("materialization failed: %w", err)
		}
	}

	return res, nil
}
