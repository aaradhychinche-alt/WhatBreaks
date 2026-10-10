package discovery_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	corev1 "github.com/aaradhychinche-alt/WhatBreaks/gen/go/wb/core/v1"
	"github.com/aaradhychinche-alt/WhatBreaks/internal/discovery"
	"github.com/aaradhychinche-alt/WhatBreaks/internal/logging"
	"github.com/aaradhychinche-alt/WhatBreaks/internal/state"
)

// mockCollector implements discovery.ClusterCollector for unit testing.
type mockCollector struct {
	mu          sync.Mutex
	clusterID   string
	workspaceID string
	pingErr     error
	collectErr  error
	evidence    []*corev1.Evidence
	pingCount   int
	collectWait chan struct{}
}

func (m *mockCollector) ClusterID() string {
	return m.clusterID
}

func (m *mockCollector) WorkspaceID() string {
	return m.workspaceID
}

func (m *mockCollector) Ping(ctx context.Context) error {
	m.mu.Lock()
	m.pingCount++
	m.mu.Unlock()
	return m.pingErr
}

func (m *mockCollector) Collect(ctx context.Context) ([]*corev1.Evidence, error) {
	if m.collectWait != nil {
		select {
		case <-m.collectWait:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if m.collectErr != nil {
		return nil, m.collectErr
	}
	return m.evidence, nil
}

// mockReconciler implements state.Reconciler for unit testing.
type mockReconciler struct {
	mu           sync.Mutex
	reconcileErr error
	reconciled   bool
	batch        state.ObservationBatch
}

func (m *mockReconciler) Reconcile(ctx context.Context, batch state.ObservationBatch) (*state.ReconciliationResult, error) {
	return m.ReconcileAndMaterialize(ctx, batch)
}

func (m *mockReconciler) ReconcileAndMaterialize(ctx context.Context, batch state.ObservationBatch) (*state.ReconciliationResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reconciled = true
	m.batch = batch
	if m.reconcileErr != nil {
		return nil, m.reconcileErr
	}
	return &state.ReconciliationResult{
		WorkspaceID:          batch.WorkspaceID,
		EvidenceCount:        len(batch.Evidence),
		ResourcesCreated:     1,
		ResourcesUpdated:     2,
		ResourcesTotal:       3,
		RelationshipsCreated: 1,
		RelationshipsTotal:   2,
		Duration:             5 * time.Millisecond,
	}, nil
}

func sampleEvidence(id string) *corev1.Evidence {
	return &corev1.Evidence{
		Id: id,
		Source: &corev1.EvidenceSource{
			Provider:  "kubernetes",
			Collector: "test-collector",
		},
		ObservedAt:      time.Now().UTC().Format(time.RFC3339Nano),
		ObservationType: "CONTROL_PLANE_OBJECT",
		Subject: &corev1.ResourceIdentity{
			Provider:     "kubernetes",
			ResourceType: "pod",
			ProviderId:   "test-cluster/default/pod-1",
		},
	}
}

// ---------------------------------------------------------------------------
// 1. Success Coordinator Execution
// ---------------------------------------------------------------------------

func TestCoordinator_Discover_Success(t *testing.T) {
	logger := logging.NewStandardLogger(nil, logging.LevelWarn)
	wsID := "00000000-0000-0000-0000-000000000001"
	clusterID := "prod-cluster"

	col := &mockCollector{
		clusterID:   clusterID,
		workspaceID: wsID,
		evidence:    []*corev1.Evidence{sampleEvidence("ev-1")},
	}
	rec := &mockReconciler{}

	coord := discovery.NewCoordinator(
		discovery.WithLogger(logger),
		discovery.WithCollector(col),
		discovery.WithReconciler(rec),
	)

	req := discovery.SyncRequest{
		WorkspaceID:   wsID,
		ClusterID:     clusterID,
		Trigger:       discovery.TriggerManual,
		CorrelationID: "run-test-1",
	}

	res, err := coord.Discover(context.Background(), req)
	if err != nil {
		t.Fatalf("expected successful discovery, got error: %v", err)
	}

	if res.Status != "COMPLETED" {
		t.Errorf("expected status COMPLETED, got %s", res.Status)
	}
	if res.Stage != discovery.StageCompleted {
		t.Errorf("expected stage COMPLETED, got %s", res.Stage)
	}
	if res.WorkspaceID != wsID {
		t.Errorf("expected workspace ID %s, got %s", wsID, res.WorkspaceID)
	}
	if res.ClusterID != clusterID {
		t.Errorf("expected cluster ID %s, got %s", clusterID, res.ClusterID)
	}
	if res.EvidenceCount != 1 {
		t.Errorf("expected evidence count 1, got %d", res.EvidenceCount)
	}
	if res.ResourcesTotal != 3 {
		t.Errorf("expected resources total 3, got %d", res.ResourcesTotal)
	}
	if res.RunID != "run-test-1" {
		t.Errorf("expected run ID run-test-1, got %s", res.RunID)
	}
	if !rec.reconciled {
		t.Errorf("expected reconciler to have been called")
	}
}

// ---------------------------------------------------------------------------
// 2. Default Cluster Resolution
// ---------------------------------------------------------------------------

func TestCoordinator_Discover_DefaultClusterResolution(t *testing.T) {
	wsID := "00000000-0000-0000-0000-000000000001"
	col := &mockCollector{
		clusterID:   "single-cluster",
		workspaceID: wsID,
		evidence:    []*corev1.Evidence{sampleEvidence("ev-1")},
	}
	rec := &mockReconciler{}

	coord := discovery.NewCoordinator(
		discovery.WithCollector(col),
		discovery.WithReconciler(rec),
	)

	// Omit ClusterID in request
	req := discovery.SyncRequest{
		WorkspaceID: wsID,
		Trigger:     discovery.TriggerManual,
	}

	res, err := coord.Discover(context.Background(), req)
	if err != nil {
		t.Fatalf("expected auto-resolution of single cluster, got error: %v", err)
	}
	if res.ClusterID != "single-cluster" {
		t.Errorf("expected cluster ID 'single-cluster', got %s", res.ClusterID)
	}
}

// ---------------------------------------------------------------------------
// 3. Cluster Reachability Failure (Ping Check)
// ---------------------------------------------------------------------------

func TestCoordinator_Discover_ReachabilityFailure_PreservesState(t *testing.T) {
	wsID := "00000000-0000-0000-0000-000000000001"
	col := &mockCollector{
		clusterID:   "unreachable-cluster",
		workspaceID: wsID,
		pingErr:     errors.New("connection refused to API server"),
	}
	rec := &mockReconciler{}

	coord := discovery.NewCoordinator(
		discovery.WithCollector(col),
		discovery.WithReconciler(rec),
	)

	req := discovery.SyncRequest{
		WorkspaceID: wsID,
		ClusterID:   "unreachable-cluster",
		Trigger:     discovery.TriggerManual,
	}

	res, err := coord.Discover(context.Background(), req)
	if err == nil {
		t.Fatalf("expected error on unreachable cluster, got nil")
	}
	if !errors.Is(err, discovery.ErrClusterUnreachable) {
		t.Errorf("expected ErrClusterUnreachable, got: %v", err)
	}
	if res == nil {
		t.Fatalf("expected structured result even on failure")
	}
	if res.Status != "FAILED" {
		t.Errorf("expected status FAILED, got %s", res.Status)
	}
	if res.Stage != discovery.StageReachability {
		t.Errorf("expected stage REACHABILITY, got %s", res.Stage)
	}
	// Verify reconciler was never invoked (zero state modified)
	if rec.reconciled {
		t.Errorf("PERSISTENCE VIOLATION: reconciler was invoked after ping failure")
	}
}

// ---------------------------------------------------------------------------
// 4. Collection Failure
// ---------------------------------------------------------------------------

func TestCoordinator_Discover_CollectionFailure_PreservesState(t *testing.T) {
	wsID := "00000000-0000-0000-0000-000000000001"
	col := &mockCollector{
		clusterID:   "failing-collector",
		workspaceID: wsID,
		collectErr:  errors.New("timed out reading pods stream"),
	}
	rec := &mockReconciler{}

	coord := discovery.NewCoordinator(
		discovery.WithCollector(col),
		discovery.WithReconciler(rec),
	)

	req := discovery.SyncRequest{
		WorkspaceID: wsID,
		ClusterID:   "failing-collector",
		Trigger:     discovery.TriggerManual,
	}

	res, err := coord.Discover(context.Background(), req)
	if err == nil {
		t.Fatalf("expected error on collection failure, got nil")
	}
	if !errors.Is(err, discovery.ErrCollectionFailed) {
		t.Errorf("expected ErrCollectionFailed, got: %v", err)
	}
	if res.Status != "FAILED" {
		t.Errorf("expected status FAILED, got %s", res.Status)
	}
	if res.Stage != discovery.StageCollection {
		t.Errorf("expected stage COLLECTION, got %s", res.Stage)
	}
	// Verify reconciler was never invoked
	if rec.reconciled {
		t.Errorf("PERSISTENCE VIOLATION: reconciler was invoked after collection failure")
	}
}

// ---------------------------------------------------------------------------
// 5. Reconciliation Failure
// ---------------------------------------------------------------------------

func TestCoordinator_Discover_ReconciliationFailure(t *testing.T) {
	wsID := "00000000-0000-0000-0000-000000000001"
	col := &mockCollector{
		clusterID:   "test-cluster",
		workspaceID: wsID,
		evidence:    []*corev1.Evidence{sampleEvidence("ev-1")},
	}
	rec := &mockReconciler{
		reconcileErr: errors.New("database connection pool closed"),
	}

	coord := discovery.NewCoordinator(
		discovery.WithCollector(col),
		discovery.WithReconciler(rec),
	)

	req := discovery.SyncRequest{
		WorkspaceID: wsID,
		ClusterID:   "test-cluster",
		Trigger:     discovery.TriggerManual,
	}

	res, err := coord.Discover(context.Background(), req)
	if err == nil {
		t.Fatalf("expected error on reconciliation failure, got nil")
	}
	if !errors.Is(err, discovery.ErrReconciliationFailed) {
		t.Errorf("expected ErrReconciliationFailed, got: %v", err)
	}
	if res.Status != "FAILED" {
		t.Errorf("expected status FAILED, got %s", res.Status)
	}
	if res.Stage != discovery.StageReconciliation {
		t.Errorf("expected stage RECONCILIATION, got %s", res.Stage)
	}
}

// ---------------------------------------------------------------------------
// 6. Materialization Failure
// ---------------------------------------------------------------------------

func TestCoordinator_Discover_MaterializationFailure(t *testing.T) {
	wsID := "00000000-0000-0000-0000-000000000001"
	col := &mockCollector{
		clusterID:   "test-cluster",
		workspaceID: wsID,
		evidence:    []*corev1.Evidence{sampleEvidence("ev-1")},
	}
	rec := &mockReconciler{
		reconcileErr: fmt.Errorf("materializer failed to load state: gRPC unavailable"),
	}

	coord := discovery.NewCoordinator(
		discovery.WithCollector(col),
		discovery.WithReconciler(rec),
	)

	req := discovery.SyncRequest{
		WorkspaceID: wsID,
		ClusterID:   "test-cluster",
		Trigger:     discovery.TriggerManual,
	}

	res, err := coord.Discover(context.Background(), req)
	if err == nil {
		t.Fatalf("expected error on materialization failure, got nil")
	}
	if !errors.Is(err, discovery.ErrMaterializationFailed) {
		t.Errorf("expected ErrMaterializationFailed, got: %v", err)
	}
	if res.Status != "FAILED" {
		t.Errorf("expected status FAILED, got %s", res.Status)
	}
	if res.Stage != discovery.StageMaterialization {
		t.Errorf("expected stage MATERIALIZATION, got %s", res.Stage)
	}
}

// ---------------------------------------------------------------------------
// 7. Context Cancellation
// ---------------------------------------------------------------------------

func TestCoordinator_Discover_ContextCancellation(t *testing.T) {
	wsID := "00000000-0000-0000-0000-000000000001"
	waitCh := make(chan struct{})
	col := &mockCollector{
		clusterID:   "test-cluster",
		workspaceID: wsID,
		collectWait: waitCh,
	}
	rec := &mockReconciler{}

	coord := discovery.NewCoordinator(
		discovery.WithCollector(col),
		discovery.WithReconciler(rec),
	)

	ctx, cancel := context.WithCancel(context.Background())

	// Cancel after brief delay
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	req := discovery.SyncRequest{
		WorkspaceID: wsID,
		ClusterID:   "test-cluster",
		Trigger:     discovery.TriggerManual,
	}

	_, err := coord.Discover(ctx, req)
	if err == nil {
		t.Fatalf("expected context cancellation error, got nil")
	}
	if !errors.Is(err, context.Canceled) && !errors.Is(err, discovery.ErrCollectionFailed) {
		t.Errorf("expected cancellation-related error, got: %v", err)
	}
	if rec.reconciled {
		t.Errorf("reconciler should not have been called when collection was cancelled")
	}
}

// ---------------------------------------------------------------------------
// 8. Same-Cluster Concurrency Guard (Overlapping runs blocked)
// ---------------------------------------------------------------------------

func TestCoordinator_Discover_SameCluster_BlocksOverlap(t *testing.T) {
	wsID := "00000000-0000-0000-0000-000000000001"
	waitCh := make(chan struct{})
	col := &mockCollector{
		clusterID:   "locked-cluster",
		workspaceID: wsID,
		collectWait: waitCh,
	}
	rec := &mockReconciler{}

	coord := discovery.NewCoordinator(
		discovery.WithCollector(col),
		discovery.WithReconciler(rec),
	)

	req := discovery.SyncRequest{
		WorkspaceID: wsID,
		ClusterID:   "locked-cluster",
		Trigger:     discovery.TriggerManual,
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = coord.Discover(context.Background(), req)
	}()

	// Wait briefly for first run to acquire cluster lock
	time.Sleep(30 * time.Millisecond)

	// Second concurrent attempt for SAME cluster must fail with ErrDiscoveryInProgress
	_, err2 := coord.Discover(context.Background(), req)
	if err2 == nil {
		t.Fatalf("expected ErrDiscoveryInProgress for concurrent run on same cluster, got nil")
	}
	if !errors.Is(err2, discovery.ErrDiscoveryInProgress) {
		t.Errorf("expected ErrDiscoveryInProgress, got: %v", err2)
	}

	// Release first run
	close(waitCh)
	wg.Wait()

	// Third attempt after completion succeeds
	res3, err3 := coord.Discover(context.Background(), req)
	if err3 != nil {
		t.Fatalf("expected clean run after previous run settled, got error: %v", err3)
	}
	if res3.Status != "COMPLETED" {
		t.Errorf("expected COMPLETED, got %s", res3.Status)
	}
}

// ---------------------------------------------------------------------------
// 9. Different-Cluster Concurrency (Independent clusters do NOT block)
// ---------------------------------------------------------------------------

func TestCoordinator_Discover_DifferentClusters_ExecuteConcurrently(t *testing.T) {
	wsID := "00000000-0000-0000-0000-000000000001"
	waitChA := make(chan struct{})
	waitChB := make(chan struct{})

	colA := &mockCollector{
		clusterID:   "cluster-a",
		workspaceID: wsID,
		collectWait: waitChA,
	}
	colB := &mockCollector{
		clusterID:   "cluster-b",
		workspaceID: wsID,
		collectWait: waitChB,
	}
	rec := &mockReconciler{}

	coord := discovery.NewCoordinator(
		discovery.WithCollectors(colA, colB),
		discovery.WithReconciler(rec),
	)

	var wg sync.WaitGroup
	var errA, errB error
	var resA, resB *discovery.Result

	// Start Cluster A
	wg.Add(1)
	go func() {
		defer wg.Done()
		resA, errA = coord.Discover(context.Background(), discovery.SyncRequest{
			WorkspaceID: wsID,
			ClusterID:   "cluster-a",
		})
	}()

	// Wait briefly for Cluster A to start
	time.Sleep(20 * time.Millisecond)

	// Start Cluster B while Cluster A is still blocked in collection
	wg.Add(1)
	go func() {
		defer wg.Done()
		resB, errB = coord.Discover(context.Background(), discovery.SyncRequest{
			WorkspaceID: wsID,
			ClusterID:   "cluster-b",
		})
	}()

	// Wait briefly then unblock both
	time.Sleep(20 * time.Millisecond)
	close(waitChA)
	close(waitChB)
	wg.Wait()

	if errA != nil {
		t.Errorf("Cluster A failed: %v", errA)
	}
	if errB != nil {
		t.Errorf("Cluster B failed (unexpected serialization or interference): %v", errB)
	}
	if resA == nil || resA.Status != "COMPLETED" {
		t.Errorf("Cluster A result incomplete: %+v", resA)
	}
	if resB == nil || resB.Status != "COMPLETED" {
		t.Errorf("Cluster B result incomplete: %+v", resB)
	}
}

// ---------------------------------------------------------------------------
// 10. Workspace / Cluster Binding Validation
// ---------------------------------------------------------------------------

func TestCoordinator_Discover_WorkspaceBindingMismatch(t *testing.T) {
	col := &mockCollector{
		clusterID:   "bound-cluster",
		workspaceID: "00000000-0000-0000-0000-000000000001",
	}
	rec := &mockReconciler{}

	coord := discovery.NewCoordinator(
		discovery.WithCollector(col),
		discovery.WithReconciler(rec),
	)

	// Attempt sync with different workspace
	req := discovery.SyncRequest{
		WorkspaceID: "00000000-0000-0000-0000-000000000002",
		ClusterID:   "bound-cluster",
	}

	_, err := coord.Discover(context.Background(), req)
	if err == nil {
		t.Fatalf("expected error on workspace mismatch, got nil")
	}
	if !errors.Is(err, discovery.ErrWorkspaceMismatch) {
		t.Errorf("expected ErrWorkspaceMismatch, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// 11. Configuration Guards
// ---------------------------------------------------------------------------

func TestCoordinator_Discover_MissingDependencies(t *testing.T) {
	wsID := "00000000-0000-0000-0000-000000000001"

	// 1. Missing workspace ID
	coord1 := discovery.NewCoordinator()
	_, err1 := coord1.Discover(context.Background(), discovery.SyncRequest{})
	if !errors.Is(err1, discovery.ErrWorkspaceRequired) {
		t.Errorf("expected ErrWorkspaceRequired, got: %v", err1)
	}

	// 2. No collectors registered
	_, err2 := coord1.Discover(context.Background(), discovery.SyncRequest{WorkspaceID: wsID})
	if !errors.Is(err2, discovery.ErrCollectorUnavailable) {
		t.Errorf("expected ErrCollectorUnavailable, got: %v", err2)
	}

	// 3. Unknown cluster requested
	col := &mockCollector{clusterID: "cluster-1", workspaceID: wsID}
	coord3 := discovery.NewCoordinator(discovery.WithCollector(col))
	_, err3 := coord3.Discover(context.Background(), discovery.SyncRequest{
		WorkspaceID: wsID,
		ClusterID:   "cluster-unknown",
	})
	if !errors.Is(err3, discovery.ErrCollectorNotFound) {
		t.Errorf("expected ErrCollectorNotFound, got: %v", err3)
	}

	// 4. Missing reconciler
	_, err4 := coord3.Discover(context.Background(), discovery.SyncRequest{
		WorkspaceID: wsID,
		ClusterID:   "cluster-1",
	})
	if !errors.Is(err4, discovery.ErrReconcilerUnavailable) {
		t.Errorf("expected ErrReconcilerUnavailable, got: %v", err4)
	}

	// 5. Multiple clusters registered, cluster ID omitted in request
	col2 := &mockCollector{clusterID: "cluster-2", workspaceID: wsID}
	rec := &mockReconciler{}
	coord5 := discovery.NewCoordinator(
		discovery.WithCollectors(col, col2),
		discovery.WithReconciler(rec),
	)
	_, err5 := coord5.Discover(context.Background(), discovery.SyncRequest{
		WorkspaceID: wsID,
	})
	if !errors.Is(err5, discovery.ErrClusterRequired) {
		t.Errorf("expected ErrClusterRequired when multiple clusters registered, got: %v", err5)
	}
}
