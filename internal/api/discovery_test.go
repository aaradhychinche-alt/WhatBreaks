package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/aaradhychinche-alt/WhatBreaks/internal/api"
	"github.com/aaradhychinche-alt/WhatBreaks/internal/collector/k8s"
	"github.com/aaradhychinche-alt/WhatBreaks/internal/logging"
	"github.com/aaradhychinche-alt/WhatBreaks/internal/state"
)

// mockK8sClient implements k8s.Client for testing collector sync.
type mockK8sClient struct {
	pingErr    error
	namespaces []k8s.Namespace
	pods       []k8s.Pod
	collectErr error
}

func (m *mockK8sClient) Ping(ctx context.Context) error {
	return m.pingErr
}
func (m *mockK8sClient) ListNamespaces(ctx context.Context) ([]k8s.Namespace, error) {
	if m.collectErr != nil {
		return nil, m.collectErr
	}
	return m.namespaces, nil
}
func (m *mockK8sClient) ListNodes(ctx context.Context) ([]k8s.Node, error) { return nil, nil }
func (m *mockK8sClient) ListDeployments(ctx context.Context, ns string) ([]k8s.Deployment, error) {
	return nil, nil
}
func (m *mockK8sClient) ListReplicaSets(ctx context.Context, ns string) ([]k8s.ReplicaSet, error) {
	return nil, nil
}
func (m *mockK8sClient) ListServices(ctx context.Context, ns string) ([]k8s.Service, error) {
	return nil, nil
}
func (m *mockK8sClient) ListIngresses(ctx context.Context, ns string) ([]k8s.Ingress, error) {
	return nil, nil
}
func (m *mockK8sClient) ListPods(ctx context.Context, ns string) ([]k8s.Pod, error) {
	if m.collectErr != nil {
		return nil, m.collectErr
	}
	return m.pods, nil
}
func (m *mockK8sClient) ListConfigMaps(ctx context.Context, ns string) ([]k8s.ConfigMap, error) {
	return nil, nil
}
func (m *mockK8sClient) ListServiceAccounts(ctx context.Context, ns string) ([]k8s.ServiceAccount, error) {
	return nil, nil
}
func (m *mockK8sClient) ListSecretsMetadata(ctx context.Context, ns string) ([]k8s.SecretMetadata, error) {
	return nil, nil
}
func (m *mockK8sClient) ListPVs(ctx context.Context) ([]k8s.PersistentVolume, error) {
	return nil, nil
}
func (m *mockK8sClient) ListPVCs(ctx context.Context, ns string) ([]k8s.PersistentVolumeClaim, error) {
	return nil, nil
}
func (m *mockK8sClient) Watch(ctx context.Context, resourcePath string, resourceVersion string) (<-chan k8s.WatchEvent, <-chan error, error) {
	return nil, nil, nil
}

// mockReconciler implements state.Reconciler for testing.
type mockReconciler struct {
	reconcileErr error
	mu           sync.Mutex
	reconciled   bool
}

func (m *mockReconciler) Reconcile(ctx context.Context, batch state.ObservationBatch) (*state.ReconciliationResult, error) {
	return m.ReconcileAndMaterialize(ctx, batch)
}

func (m *mockReconciler) ReconcileAndMaterialize(ctx context.Context, batch state.ObservationBatch) (*state.ReconciliationResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.reconcileErr != nil {
		return nil, m.reconcileErr
	}
	m.reconciled = true
	return &state.ReconciliationResult{
		WorkspaceID:          batch.WorkspaceID,
		EvidenceCount:        len(batch.Evidence),
		ResourcesCreated:     1,
		ResourcesTotal:       1,
		RelationshipsCreated: 0,
		Duration:             10 * time.Millisecond,
	}, nil
}

func TestDiscoverySync_Success(t *testing.T) {
	wsID := "00000000-0000-0000-0000-000000000001"
	logger := logging.NewStandardLogger(nil, logging.LevelWarn)

	client := &mockK8sClient{
		pods: []k8s.Pod{
			{
				ObjectMeta: k8s.ObjectMeta{
					Name:              "backend-pod",
					Namespace:         "default",
					CreationTimestamp: time.Now().Format(time.RFC3339),
				},
			},
		},
	}

	collector, err := k8s.New(
		k8s.WithClusterID("k3d-cluster"),
		k8s.WithWorkspaceID(wsID),
		k8s.WithClient(client),
		k8s.WithLogger(logger),
	)
	if err != nil {
		t.Fatalf("failed to create collector: %v", err)
	}

	rec := &mockReconciler{}

	cfg := api.DefaultConfig()
	cfg.IsTest = true
	cfg.ConfiguredWorkspaceID = wsID
	cfg.K8sCollector = collector
	cfg.Reconciler = rec
	cfg.Logger = logger

	srv := api.NewServer(cfg)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/discovery/sync?workspace_id="+wsID, nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp api.DiscoverySyncResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse sync response: %v", err)
	}

	if resp.Status != "COMPLETED" {
		t.Errorf("expected COMPLETED, got %s", resp.Status)
	}
	if resp.WorkspaceID != wsID {
		t.Errorf("expected workspace ID %s, got %s", wsID, resp.WorkspaceID)
	}
	if resp.EvidenceCount == 0 {
		t.Errorf("expected non-zero evidence count, got %d", resp.EvidenceCount)
	}
}

func TestDiscoverySync_ClusterFailure_PreservesState(t *testing.T) {
	wsID := "00000000-0000-0000-0000-000000000001"
	logger := logging.NewStandardLogger(nil, logging.LevelWarn)

	// Simulated unreachable cluster
	client := &mockK8sClient{
		pingErr:    errors.New("connection refused to kubernetes API: 127.0.0.1:6443"),
		collectErr: errors.New("connection refused to kubernetes API: 127.0.0.1:6443"),
	}

	collector, err := k8s.New(
		k8s.WithClusterID("k3d-cluster"),
		k8s.WithWorkspaceID(wsID),
		k8s.WithClient(client),
		k8s.WithLogger(logger),
	)
	if err != nil {
		t.Fatalf("failed to create collector: %v", err)
	}

	rec := &mockReconciler{}

	cfg := api.DefaultConfig()
	cfg.IsTest = true
	cfg.ConfiguredWorkspaceID = wsID
	cfg.K8sCollector = collector
	cfg.Reconciler = rec
	cfg.Logger = logger

	srv := api.NewServer(cfg)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/discovery/sync?workspace_id="+wsID, nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusBadGateway {
		t.Fatalf("expected 502 Bad Gateway on cluster error, got %d: %s", rr.Code, rr.Body.String())
	}

	var errResp api.ErrorResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &errResp)
	if errResp.Code != "DISCOVERY_FAILED" {
		t.Errorf("expected DISCOVERY_FAILED, got %s", errResp.Code)
	}

	// Verify reconciler was NOT called on collection failure (protecting persistent state)
	if rec.reconciled {
		t.Errorf("reconciler should not have been called when collection failed")
	}
}

func TestDiscoverySync_WorkspaceIsolation(t *testing.T) {
	wsID := "00000000-0000-0000-0000-000000000001"
	attackerWS := "00000000-0000-0000-0000-000000000002"
	logger := logging.NewStandardLogger(nil, logging.LevelWarn)

	client := &mockK8sClient{}
	collector, _ := k8s.New(
		k8s.WithClusterID("k3d-cluster"),
		k8s.WithWorkspaceID(wsID),
		k8s.WithClient(client),
		k8s.WithLogger(logger),
	)

	cfg := api.DefaultConfig()
	cfg.IsTest = true
	cfg.ConfiguredWorkspaceID = wsID
	cfg.K8sCollector = collector
	cfg.Reconciler = &mockReconciler{}
	cfg.Logger = logger

	srv := api.NewServer(cfg)

	// Cross-workspace sync attempt
	reqCross := httptest.NewRequest(http.MethodPost, "/api/v1/discovery/sync?workspace_id="+attackerWS, nil)
	recCross := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recCross, reqCross)

	if recCross.Code != http.StatusForbidden {
		t.Errorf("expected 403 Forbidden for cross-workspace sync, got %d", recCross.Code)
	}
}

// slowMockK8sClient blocks in ListPods until unblocked to simulate long sync.
type slowMockK8sClient struct {
	mockK8sClient
	blockCh chan struct{}
}

func (s *slowMockK8sClient) ListPods(ctx context.Context, ns string) ([]k8s.Pod, error) {
	<-s.blockCh
	return nil, nil
}

func TestDiscoverySync_Conflict_ConcurrentRequests(t *testing.T) {
	wsID := "00000000-0000-0000-0000-000000000001"
	logger := logging.NewStandardLogger(nil, logging.LevelWarn)

	blockCh := make(chan struct{})
	client := &slowMockK8sClient{blockCh: blockCh}

	collector, err := k8s.New(
		k8s.WithClusterID("k3d-cluster"),
		k8s.WithWorkspaceID(wsID),
		k8s.WithClient(client),
		k8s.WithLogger(logger),
	)
	if err != nil {
		t.Fatalf("failed to create collector: %v", err)
	}

	cfg := api.DefaultConfig()
	cfg.IsTest = true
	cfg.ConfiguredWorkspaceID = wsID
	cfg.K8sCollector = collector
	cfg.Reconciler = &mockReconciler{}
	cfg.Logger = logger

	srv := api.NewServer(cfg)

	// Launch first request in goroutine
	req1 := httptest.NewRequest(http.MethodPost, "/api/v1/discovery/sync?workspace_id="+wsID, nil)
	rec1 := httptest.NewRecorder()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		srv.Handler().ServeHTTP(rec1, req1)
	}()

	// Wait briefly for first request to acquire syncMgr lock
	time.Sleep(50 * time.Millisecond)

	// Second request while first is in progress
	req2 := httptest.NewRequest(http.MethodPost, "/api/v1/discovery/sync?workspace_id="+wsID, nil)
	rec2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusConflict {
		t.Errorf("expected 409 Conflict for concurrent sync, got %d: %s", rec2.Code, rec2.Body.String())
	}

	var errResp api.ErrorResponse
	_ = json.Unmarshal(rec2.Body.Bytes(), &errResp)
	if errResp.Code != "DISCOVERY_IN_PROGRESS" {
		t.Errorf("expected DISCOVERY_IN_PROGRESS, got %s", errResp.Code)
	}

	// Unblock first request
	close(blockCh)
	wg.Wait()

	if rec1.Code != http.StatusOK {
		t.Errorf("expected 200 OK for first request, got %d: %s", rec1.Code, rec1.Body.String())
	}
}
