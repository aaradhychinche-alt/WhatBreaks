package discovery

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/aaradhychinche-alt/WhatBreaks/internal/logging"
	"github.com/aaradhychinche-alt/WhatBreaks/internal/state"
)

// Option configures DiscoveryCoordinator instances.
type Option func(*DefaultCoordinator)

// WithLogger sets the structured logger for the coordinator.
func WithLogger(logger logging.Logger) Option {
	return func(c *DefaultCoordinator) {
		if logger != nil {
			c.logger = logger
		}
	}
}

// WithReconciler sets the state reconciler for the coordinator.
func WithReconciler(reconciler state.Reconciler) Option {
	return func(c *DefaultCoordinator) {
		c.reconciler = reconciler
	}
}

// WithCollector registers an initial infrastructure collector.
func WithCollector(collector ClusterCollector) Option {
	return func(c *DefaultCoordinator) {
		if collector != nil {
			_ = c.RegisterCollector(collector)
		}
	}
}

// WithCollectors registers multiple infrastructure collectors.
func WithCollectors(collectors ...ClusterCollector) Option {
	return func(c *DefaultCoordinator) {
		for _, col := range collectors {
			if col != nil {
				_ = c.RegisterCollector(col)
			}
		}
	}
}

// DefaultCoordinator orchestrates the complete discovery lifecycle:
// reachability probing, observation collection, state reconciliation,
// and Rust Core Engine materialization.
//
// Concurrency Model:
// Provides process-local mutual exclusion partitioned by cluster ID.
// Overlapping discovery runs for the SAME cluster are rejected immediately
// with ErrDiscoveryInProgress. Distinct clusters execute independently
// without serializing each other.
// Note: This in-memory lock does not provide cross-process distributed coordination.
type DefaultCoordinator struct {
	mu               sync.Mutex
	collectors       map[string]ClusterCollector
	defaultClusterID string
	activeRuns       map[string]bool
	reconciler       state.Reconciler
	logger           logging.Logger
}

// NewCoordinator constructs a DefaultCoordinator instance with provided options.
func NewCoordinator(opts ...Option) *DefaultCoordinator {
	c := &DefaultCoordinator{
		collectors: make(map[string]ClusterCollector),
		activeRuns: make(map[string]bool),
		logger:     logging.NewJSONLogger(nil, logging.LevelInfo, "discovery-coordinator"),
	}

	for _, opt := range opts {
		opt(c)
	}

	return c
}

// RegisterCollector registers an infrastructure collector under its stable ClusterID.
func (c *DefaultCoordinator) RegisterCollector(collector ClusterCollector) error {
	if collector == nil {
		return fmt.Errorf("collector cannot be nil")
	}
	clusterID := strings.TrimSpace(collector.ClusterID())
	if clusterID == "" {
		return fmt.Errorf("collector cluster ID cannot be empty")
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	c.collectors[clusterID] = collector
	if len(c.collectors) == 1 {
		c.defaultClusterID = clusterID
	}

	return nil
}

// GetCollector retrieves a registered collector by its cluster identifier.
func (c *DefaultCoordinator) GetCollector(clusterID string) (ClusterCollector, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	col, ok := c.collectors[clusterID]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrCollectorNotFound, clusterID)
	}
	return col, nil
}

// ListCollectors returns all registered collectors sorted by cluster identifier.
func (c *DefaultCoordinator) ListCollectors() []ClusterCollector {
	c.mu.Lock()
	defer c.mu.Unlock()

	keys := make([]string, 0, len(c.collectors))
	for k := range c.collectors {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	list := make([]ClusterCollector, 0, len(keys))
	for _, k := range keys {
		list = append(list, c.collectors[k])
	}
	return list
}

// Discover executes the shared discovery pipeline for a specified workspace and cluster.
func (c *DefaultCoordinator) Discover(ctx context.Context, req SyncRequest) (*Result, error) {
	wsID := strings.TrimSpace(req.WorkspaceID)
	if wsID == "" {
		return nil, ErrWorkspaceRequired
	}

	start := time.Now()
	runID := req.CorrelationID
	if runID == "" {
		runID = fmt.Sprintf("run-%d", time.Now().UnixNano())
	}

	// 1. Resolve target collector and cluster ID
	c.mu.Lock()
	if len(c.collectors) == 0 {
		c.mu.Unlock()
		return nil, ErrCollectorUnavailable
	}

	targetCluster := strings.TrimSpace(req.ClusterID)
	if targetCluster == "" {
		if len(c.collectors) == 1 {
			targetCluster = c.defaultClusterID
		} else {
			c.mu.Unlock()
			return nil, ErrClusterRequired
		}
	}

	collector, ok := c.collectors[targetCluster]
	if !ok {
		c.mu.Unlock()
		return nil, fmt.Errorf("%w: %q", ErrCollectorNotFound, targetCluster)
	}

	// 2. Validate workspace binding
	colWS := strings.TrimSpace(collector.WorkspaceID())
	if colWS != "" && wsID != colWS {
		c.mu.Unlock()
		c.logger.Warn("Rejected discovery sweep targeting unconfigured workspace",
			"requested_workspace", wsID,
			"configured_workspace", colWS,
			"cluster_id", targetCluster,
		)
		return nil, ErrWorkspaceMismatch
	}

	// 3. Validate reconciler availability
	if c.reconciler == nil {
		c.mu.Unlock()
		return nil, ErrReconcilerUnavailable
	}

	// 4. Per-cluster mutual exclusion: reject overlapping sweeps for the SAME cluster
	if c.activeRuns[targetCluster] {
		c.mu.Unlock()
		return nil, ErrDiscoveryInProgress
	}
	c.activeRuns[targetCluster] = true
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		delete(c.activeRuns, targetCluster)
		c.mu.Unlock()
	}()

	c.logger.Info("Starting discovery sweep",
		"workspace_id", wsID,
		"cluster_id", targetCluster,
		"trigger", req.Trigger,
		"run_id", runID,
	)

	// 5. Stage: Cluster Reachability Check
	// Crucial safety guarantee: fail fast if unreachable before modifying any state.
	if err := collector.Ping(ctx); err != nil {
		c.logger.Error("Cluster reachability ping check failed",
			"workspace_id", wsID,
			"cluster_id", targetCluster,
			"error", err.Error(),
		)
		duration := time.Since(start)
		return &Result{
			WorkspaceID: wsID,
			ClusterID:   targetCluster,
			Trigger:     req.Trigger,
			RunID:       runID,
			Status:      "FAILED",
			Stage:       StageReachability,
			Duration:    duration,
			DurationMs:  duration.Milliseconds(),
			Error:       err,
		}, fmt.Errorf("%w: %v", ErrClusterUnreachable, err)
	}

	// Check context cancellation before heavy collection
	if err := ctx.Err(); err != nil {
		duration := time.Since(start)
		return &Result{
			WorkspaceID: wsID,
			ClusterID:   targetCluster,
			Trigger:     req.Trigger,
			RunID:       runID,
			Status:      "FAILED",
			Stage:       StageReachability,
			Duration:    duration,
			DurationMs:  duration.Milliseconds(),
			Error:       err,
		}, err
	}

	// 6. Stage: Observation Collection Sweep
	evidenceList, err := collector.Collect(ctx)
	if err != nil {
		c.logger.Error("Cluster observation collection sweep failed",
			"workspace_id", wsID,
			"cluster_id", targetCluster,
			"error", err.Error(),
		)
		duration := time.Since(start)
		return &Result{
			WorkspaceID: wsID,
			ClusterID:   targetCluster,
			Trigger:     req.Trigger,
			RunID:       runID,
			Status:      "FAILED",
			Stage:       StageCollection,
			Duration:    duration,
			DurationMs:  duration.Milliseconds(),
			Error:       err,
		}, fmt.Errorf("%w: %v", ErrCollectionFailed, err)
	}

	// Check context cancellation before reconciliation
	if err := ctx.Err(); err != nil {
		duration := time.Since(start)
		return &Result{
			WorkspaceID:   wsID,
			ClusterID:     targetCluster,
			Trigger:       req.Trigger,
			RunID:         runID,
			Status:        "FAILED",
			Stage:         StageCollection,
			EvidenceCount: len(evidenceList),
			Duration:      duration,
			DurationMs:    duration.Milliseconds(),
			Error:         err,
		}, err
	}

	// 7. Stage: Reconciliation & Materialization
	batch := state.ObservationBatch{
		WorkspaceID: wsID,
		Evidence:    evidenceList,
	}

	recResult, err := c.reconciler.ReconcileAndMaterialize(ctx, batch)
	if err != nil {
		duration := time.Since(start)
		stage := StageReconciliation
		errType := ErrReconciliationFailed

		// Differentiate materialization failure from database reconciliation failure
		errMsg := strings.ToLower(err.Error())
		if strings.Contains(errMsg, "materializ") {
			stage = StageMaterialization
			errType = ErrMaterializationFailed
		}

		c.logger.Error("State reconciliation and materialization failed",
			"workspace_id", wsID,
			"cluster_id", targetCluster,
			"stage", stage,
			"error", err.Error(),
		)

		res := &Result{
			WorkspaceID:   wsID,
			ClusterID:     targetCluster,
			Trigger:       req.Trigger,
			RunID:         runID,
			Status:        "FAILED",
			Stage:         stage,
			EvidenceCount: len(evidenceList),
			Duration:      duration,
			DurationMs:    duration.Milliseconds(),
			Error:         err,
		}
		if recResult != nil {
			res.ResourcesCreated = recResult.ResourcesCreated
			res.ResourcesUpdated = recResult.ResourcesUpdated
			res.ResourcesTotal = recResult.ResourcesTotal
			res.RelationshipsCreated = recResult.RelationshipsCreated
			res.RelationshipsUpdated = recResult.RelationshipsUpdated
			res.RelationshipsTotal = recResult.RelationshipsTotal
			res.ConflictsCount = recResult.ConflictsCount
		}
		return res, fmt.Errorf("%w: %v", errType, err)
	}

	// 8. Stage: Completed
	duration := time.Since(start)
	status := "COMPLETED"
	if recResult.ConflictsCount > 0 || recResult.InsufficientCount > 0 {
		status = "PARTIAL"
	}

	c.logger.Info("Discovery sweep and materialization completed successfully",
		"workspace_id", wsID,
		"cluster_id", targetCluster,
		"status", status,
		"evidence_count", len(evidenceList),
		"resources_total", recResult.ResourcesTotal,
		"duration_ms", duration.Milliseconds(),
	)

	return &Result{
		WorkspaceID:          wsID,
		ClusterID:            targetCluster,
		Trigger:              req.Trigger,
		RunID:                runID,
		Status:               status,
		Stage:                StageCompleted,
		EvidenceCount:        len(evidenceList),
		ResourcesCreated:     recResult.ResourcesCreated,
		ResourcesUpdated:     recResult.ResourcesUpdated,
		ResourcesTotal:       recResult.ResourcesTotal,
		RelationshipsCreated: recResult.RelationshipsCreated,
		RelationshipsUpdated: recResult.RelationshipsUpdated,
		RelationshipsTotal:   recResult.RelationshipsTotal,
		ConflictsCount:       recResult.ConflictsCount,
		Duration:             duration,
		DurationMs:           duration.Milliseconds(),
	}, nil
}
