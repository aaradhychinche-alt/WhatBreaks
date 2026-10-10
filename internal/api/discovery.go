package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/aaradhychinche-alt/WhatBreaks/internal/discovery"
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

// DiscoverySyncManager is retained for backwards compatibility.
// Synchronization is now coordinated by discovery.Coordinator.
type DiscoverySyncManager struct {
	mu sync.Mutex
}

// NewDiscoverySyncManager creates a new DiscoverySyncManager.
func NewDiscoverySyncManager() *DiscoverySyncManager {
	return &DiscoverySyncManager{}
}

// handleDiscoverySync handles POST /api/v1/discovery/sync.
// It delegates the entire discovery lifecycle to discovery.Coordinator.
func (s *Server) handleDiscoverySync(_ ...*DiscoverySyncManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeErrorResponse(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
			return
		}

		if s.coordinator == nil {
			writeErrorResponse(w, http.StatusServiceUnavailable, "Kubernetes collector is not configured or enabled", "COLLECTOR_UNAVAILABLE")
			return
		}

		wsID, err := extractAndAuthorizeWorkspace(r, s.cfg.WorkspaceAuthorizer, s.cfg.ConfiguredWorkspaceID)
		if err != nil {
			handleWorkspaceAuthError(w, err)
			return
		}

		// Optional cluster identifier from query or header
		clusterID := r.URL.Query().Get("cluster_id")
		if clusterID == "" {
			clusterID = r.Header.Get("X-Cluster-ID")
		}

		syncCtx, syncCancel := context.WithTimeout(r.Context(), 45*time.Second)
		defer syncCancel()

		req := discovery.SyncRequest{
			WorkspaceID:   wsID,
			ClusterID:     clusterID,
			Trigger:       discovery.TriggerManual,
			CorrelationID: r.Header.Get("X-Correlation-ID"),
		}

		s.logger.Info("Starting manual Kubernetes discovery sweep", "workspace_id", wsID, "cluster_id", clusterID)

		result, err := s.coordinator.Discover(syncCtx, req)
		if err != nil {
			switch {
			case errors.Is(err, discovery.ErrCollectorUnavailable), errors.Is(err, discovery.ErrCollectorNotFound):
				writeErrorResponse(w, http.StatusServiceUnavailable, "Kubernetes collector is not configured or enabled", "COLLECTOR_UNAVAILABLE")
			case errors.Is(err, discovery.ErrReconcilerUnavailable):
				writeErrorResponse(w, http.StatusServiceUnavailable, "State reconciler is not configured", "RECONCILER_UNAVAILABLE")
			case errors.Is(err, discovery.ErrWorkspaceMismatch):
				s.logger.Warn("Rejected discovery sync targeting unconfigured workspace",
					"requested_workspace", wsID,
				)
				writeErrorResponse(w, http.StatusForbidden, "Discovery collector is not configured for this workspace", "FORBIDDEN")
			case errors.Is(err, discovery.ErrDiscoveryInProgress):
				writeErrorResponse(w, http.StatusConflict, "A discovery sweep is currently in progress", "DISCOVERY_IN_PROGRESS")
			case errors.Is(err, discovery.ErrClusterUnreachable):
				s.logger.Error("Kubernetes cluster ping check failed",
					"workspace_id", wsID,
					"error", err,
				)
				writeErrorResponse(w, http.StatusBadGateway, "Kubernetes cluster is unreachable: "+err.Error(), "DISCOVERY_FAILED")
			case errors.Is(err, discovery.ErrCollectionFailed):
				s.logger.Error("Kubernetes collection sweep failed",
					"workspace_id", wsID,
					"error", err,
				)
				writeErrorResponse(w, http.StatusBadGateway, "Failed to collect observations from Kubernetes cluster", "DISCOVERY_FAILED")
			case errors.Is(err, discovery.ErrReconciliationFailed), errors.Is(err, discovery.ErrMaterializationFailed):
				s.logger.Error("Reconciliation and materialization failed",
					"workspace_id", wsID,
					"error", err,
				)
				writeErrorResponse(w, http.StatusInternalServerError, "Failed to reconcile and materialize discovery batch", "RECONCILIATION_FAILED")
			default:
				writeErrorResponse(w, http.StatusInternalServerError, err.Error(), "INTERNAL_ERROR")
			}
			return
		}

		s.logger.Info("Discovery sweep and materialization completed",
			"workspace_id", result.WorkspaceID,
			"cluster_id", result.ClusterID,
			"status", result.Status,
			"evidence_count", result.EvidenceCount,
			"resources_total", result.ResourcesTotal,
			"duration_ms", result.DurationMs,
		)

		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(DiscoverySyncResponse{
			WorkspaceID:          result.WorkspaceID,
			Status:               result.Status,
			EvidenceCount:        result.EvidenceCount,
			ResourcesCreated:     result.ResourcesCreated,
			ResourcesUpdated:     result.ResourcesUpdated,
			ResourcesTotal:       result.ResourcesTotal,
			RelationshipsCreated: result.RelationshipsCreated,
			RelationshipsTotal:   result.RelationshipsTotal,
			ConflictsCount:       result.ConflictsCount,
			DurationMs:           result.DurationMs,
		})
	}
}
