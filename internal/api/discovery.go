package api

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/aaradhychinche-alt/WhatBreaks/internal/state"
)

// DiscoverySyncResponse represents the structured telemetry returned by POST /api/v1/discovery/sync.
type DiscoverySyncResponse struct {
	WorkspaceID          string `json:"workspace_id"`
	Status               string `json:"status"` // "COMPLETED" or "PARTIAL"
	EvidenceCount        int    `json:"evidence_count"`
	ResourcesCreated     int    `json:"resources_created"`
	ResourcesUpdated     int    `json:"resources_updated"`
	ResourcesTotal       int    `json:"resources_total"`
	RelationshipsCreated int    `json:"relationships_created"`
	RelationshipsTotal   int    `json:"relationships_total"`
	ConflictsCount       int    `json:"conflicts_count"`
	DurationMs           int64  `json:"duration_ms"`
}

// DiscoverySyncManager coordinates concurrency control across manual sync requests
// and background scheduler runs.
type DiscoverySyncManager struct {
	mu sync.Mutex
}

// NewDiscoverySyncManager creates a new DiscoverySyncManager.
func NewDiscoverySyncManager() *DiscoverySyncManager {
	return &DiscoverySyncManager{}
}

// handleDiscoverySync handles POST /api/v1/discovery/sync.
func (s *Server) handleDiscoverySync(syncMgr *DiscoverySyncManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeErrorResponse(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
			return
		}

		if s.cfg.K8sCollector == nil {
			writeErrorResponse(w, http.StatusServiceUnavailable, "Kubernetes collector is not configured or enabled", "COLLECTOR_UNAVAILABLE")
			return
		}

		if s.cfg.Reconciler == nil {
			writeErrorResponse(w, http.StatusServiceUnavailable, "State reconciler is not configured", "RECONCILER_UNAVAILABLE")
			return
		}

		wsID, err := extractAndAuthorizeWorkspace(r, s.cfg.WorkspaceAuthorizer, s.cfg.ConfiguredWorkspaceID)
		if err != nil {
			handleWorkspaceAuthError(w, err)
			return
		}

		// Ensure the requested workspace matches the collector's bound workspace
		colWS := s.cfg.K8sCollector.WorkspaceID()
		if colWS != "" && wsID != colWS {
			s.logger.Warn("Rejected discovery sync targeting unconfigured workspace",
				"requested_workspace", wsID,
				"configured_workspace", colWS,
			)
			writeErrorResponse(w, http.StatusForbidden, "Discovery collector is not configured for this workspace", "FORBIDDEN")
			return
		}

		// Prevent overlapping concurrent sync operations
		if !syncMgr.mu.TryLock() {
			writeErrorResponse(w, http.StatusConflict, "A discovery sweep is currently in progress", "DISCOVERY_IN_PROGRESS")
			return
		}
		defer syncMgr.mu.Unlock()

		start := time.Now()
		syncCtx, syncCancel := context.WithTimeout(r.Context(), 45*time.Second)
		defer syncCancel()

		s.logger.Info("Starting manual Kubernetes discovery sweep", "workspace_id", wsID)

		// 1. Verify cluster connectivity
		if err := s.cfg.K8sCollector.Ping(syncCtx); err != nil {
			s.logger.Error("Kubernetes cluster ping check failed",
				"workspace_id", wsID,
				"error", err,
			)
			writeErrorResponse(w, http.StatusBadGateway, "Kubernetes cluster is unreachable: "+err.Error(), "DISCOVERY_FAILED")
			return
		}

		// 2. Perform read-only sweep from Kubernetes API
		evidenceList, err := s.cfg.K8sCollector.Collect(syncCtx)
		if err != nil {
			s.logger.Error("Kubernetes collection sweep failed",
				"workspace_id", wsID,
				"error", err,
			)
			// Crucial safety guarantee: do not execute reconciliation on collect failure,
			// ensuring previously persisted resources are never deleted or corrupted.
			writeErrorResponse(w, http.StatusBadGateway, "Failed to collect observations from Kubernetes cluster", "DISCOVERY_FAILED")
			return
		}

		// 2. Reconcile observations and materialize into Rust Core Engine
		batch := state.ObservationBatch{
			WorkspaceID: wsID,
			Evidence:    evidenceList,
		}

		recResult, err := s.cfg.Reconciler.ReconcileAndMaterialize(syncCtx, batch)
		if err != nil {
			s.logger.Error("Reconciliation and materialization failed",
				"workspace_id", wsID,
				"error", err,
			)
			writeErrorResponse(w, http.StatusInternalServerError, "Failed to reconcile and materialize discovery batch", "RECONCILIATION_FAILED")
			return
		}

		durationMs := time.Since(start).Milliseconds()
		status := "COMPLETED"
		if recResult.ConflictsCount > 0 || recResult.InsufficientCount > 0 {
			status = "PARTIAL"
		}

		s.logger.Info("Discovery sweep and materialization completed",
			"workspace_id", wsID,
			"status", status,
			"evidence_count", len(evidenceList),
			"resources_total", recResult.ResourcesTotal,
			"duration_ms", durationMs,
		)

		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(DiscoverySyncResponse{
			WorkspaceID:          wsID,
			Status:               status,
			EvidenceCount:        len(evidenceList),
			ResourcesCreated:     recResult.ResourcesCreated,
			ResourcesUpdated:     recResult.ResourcesUpdated,
			ResourcesTotal:       recResult.ResourcesTotal,
			RelationshipsCreated: recResult.RelationshipsCreated,
			RelationshipsTotal:   recResult.RelationshipsTotal,
			ConflictsCount:       recResult.ConflictsCount,
			DurationMs:           durationMs,
		})
	}
}
