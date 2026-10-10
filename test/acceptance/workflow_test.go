package acceptance_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"testing"
	"time"

	"github.com/aaradhychinche-alt/WhatBreaks/internal/answer"
	"github.com/aaradhychinche-alt/WhatBreaks/internal/api"
	"github.com/aaradhychinche-alt/WhatBreaks/internal/collector/k8s"
	"github.com/aaradhychinche-alt/WhatBreaks/internal/coreclient"
	"github.com/aaradhychinche-alt/WhatBreaks/internal/database"
	"github.com/aaradhychinche-alt/WhatBreaks/internal/logging"
	"github.com/aaradhychinche-alt/WhatBreaks/internal/state"
)

// mockWorkflowK8sClient implements k8s.Client returning deterministic infrastructure manifests.
type mockWorkflowK8sClient struct {
	pingErr bool
}

func (m *mockWorkflowK8sClient) Ping(ctx context.Context) error {
	if m.pingErr {
		return fmt.Errorf("connection refused to kubernetes API server")
	}
	return nil
}
func (m *mockWorkflowK8sClient) ListNodes(ctx context.Context) ([]k8s.Node, error) {
	return []k8s.Node{
		{
			ObjectMeta: k8s.ObjectMeta{Name: "worker-node-1"},
		},
	}, nil
}
func (m *mockWorkflowK8sClient) ListNamespaces(ctx context.Context) ([]k8s.Namespace, error) {
	return []k8s.Namespace{
		{
			ObjectMeta: k8s.ObjectMeta{Name: "production"},
		},
	}, nil
}
func (m *mockWorkflowK8sClient) ListDeployments(ctx context.Context, ns string) ([]k8s.Deployment, error) {
	return []k8s.Deployment{
		{
			ObjectMeta: k8s.ObjectMeta{Name: "backend", Namespace: "production"},
		},
	}, nil
}
func (m *mockWorkflowK8sClient) ListReplicaSets(ctx context.Context, ns string) ([]k8s.ReplicaSet, error) {
	isController := true
	return []k8s.ReplicaSet{
		{
			ObjectMeta: k8s.ObjectMeta{
				Name:      "backend-rs",
				Namespace: "production",
				OwnerReferences: []k8s.OwnerReference{
					{Kind: "Deployment", Name: "backend", Controller: &isController},
				},
			},
		},
	}, nil
}
func (m *mockWorkflowK8sClient) ListServices(ctx context.Context, ns string) ([]k8s.Service, error) {
	return []k8s.Service{
		{
			ObjectMeta: k8s.ObjectMeta{Name: "backend-svc", Namespace: "production"},
		},
	}, nil
}
func (m *mockWorkflowK8sClient) ListIngresses(ctx context.Context, ns string) ([]k8s.Ingress, error) {
	return nil, nil
}
func (m *mockWorkflowK8sClient) ListPods(ctx context.Context, ns string) ([]k8s.Pod, error) {
	isController := true
	return []k8s.Pod{
		{
			ObjectMeta: k8s.ObjectMeta{
				Name:      "backend-pod-1",
				Namespace: "production",
				OwnerReferences: []k8s.OwnerReference{
					{Kind: "ReplicaSet", Name: "backend-rs", Controller: &isController},
				},
			},
			Spec: k8s.PodSpec{
				Volumes: []k8s.Volume{
					{
						Name: "config-vol",
						ConfigMap: &k8s.ConfigMapVolumeSource{
							Name: "backend-config",
						},
					},
				},
			},
		},
	}, nil
}
func (m *mockWorkflowK8sClient) ListConfigMaps(ctx context.Context, ns string) ([]k8s.ConfigMap, error) {
	return []k8s.ConfigMap{
		{
			ObjectMeta: k8s.ObjectMeta{Name: "backend-config", Namespace: "production"},
		},
	}, nil
}
func (m *mockWorkflowK8sClient) ListServiceAccounts(ctx context.Context, ns string) ([]k8s.ServiceAccount, error) {
	return nil, nil
}
func (m *mockWorkflowK8sClient) ListSecretsMetadata(ctx context.Context, ns string) ([]k8s.SecretMetadata, error) {
	return nil, nil
}
func (m *mockWorkflowK8sClient) ListPVs(ctx context.Context) ([]k8s.PersistentVolume, error) {
	return nil, nil
}
func (m *mockWorkflowK8sClient) ListPVCs(ctx context.Context, ns string) ([]k8s.PersistentVolumeClaim, error) {
	return nil, nil
}
func (m *mockWorkflowK8sClient) Watch(ctx context.Context, resourcePath string, resourceVersion string) (<-chan k8s.WatchEvent, <-chan error, error) {
	return nil, nil, nil
}

func TestProductWorkflow_EndToEnd(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	logger := logging.NewStandardLogger(nil, logging.LevelWarn)
	workspaceID := "00000000-0000-0000-0000-000000000001"
	attackerWS := "00000000-0000-0000-0000-000000000002"
	clusterID := "k8s-cluster"

	// 1. Connect real PostgreSQL database
	dbCfg := getTestDatabaseConfig(t)
	db, err := database.New(ctx, dbCfg, database.WithLogger(logger))
	if err != nil {
		t.Fatalf("failed to connect to PostgreSQL: %v", err)
	}
	defer db.Close()

	if err := state.EnsureSchema(ctx, db); err != nil {
		t.Fatalf("EnsureSchema failed: %v", err)
	}
	pgStore := state.NewPostgresStore(db)

	_ = pgStore.DeleteWorkspaceState(ctx, workspaceID)
	_ = pgStore.DeleteWorkspaceState(ctx, attackerWS)
	defer func() {
		_ = pgStore.DeleteWorkspaceState(context.Background(), workspaceID)
		_ = pgStore.DeleteWorkspaceState(context.Background(), attackerWS)
	}()

	// 2. Start Rust Core Engine Server
	addr, err := allocateFreeAddr()
	if err != nil {
		t.Fatalf("failed to allocate free address: %v", err)
	}
	managedServer := startManagedServer(t, addr)
	defer managedServer.Stop()

	coreClient, err := coreclient.Connect(ctx, addr)
	if err != nil {
		t.Fatalf("failed to connect to Rust core engine: %v", err)
	}
	defer coreClient.Close()

	// 3. Setup Reconciler and Materializer
	materializer := state.NewMaterializer(pgStore, coreClient, logger)
	reconciler := state.NewReconciler(pgStore, coreClient, materializer, logger)

	// 4. Setup Kubernetes Collector
	k8sClient := &mockWorkflowK8sClient{pingErr: false}
	collector, err := k8s.New(
		k8s.WithClusterID(clusterID),
		k8s.WithWorkspaceID(workspaceID),
		k8s.WithClient(k8sClient),
		k8s.WithCoreClient(coreClient),
		k8s.WithLogger(logger),
	)
	if err != nil {
		t.Fatalf("failed to initialize collector: %v", err)
	}

	// 5. Setup Answer Service
	answerSvc := answer.NewService(coreClient, logger)
	answerHandler := answer.NewHandler(answerSvc, answer.WithLogger(logger))

	// 6. Setup WhatBreaks HTTP API Server
	apiCfg := api.DefaultConfig()
	apiCfg.IsTest = true
	apiCfg.Port = 0 // dynamically allocate
	apiCfg.Store = pgStore
	apiCfg.Reconciler = reconciler
	apiCfg.K8sCollector = collector
	apiCfg.ConfiguredWorkspaceID = workspaceID
	apiCfg.ImpactHandler = answerHandler
	apiCfg.Logger = logger

	apiServer := api.NewServer(apiCfg)
	if err := apiServer.Start(); err != nil {
		t.Fatalf("failed to start API server: %v", err)
	}
	defer func() { _ = apiServer.Shutdown(context.Background()) }()

	apiBaseURL := fmt.Sprintf("http://%s", apiServer.Addr())

	// HTTP Client with Cookie Jar for CSRF double-submit protection
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("failed to create cookie jar: %v", err)
	}
	httpClient := &http.Client{Jar: jar, Timeout: 10 * time.Second}

	// -----------------------------------------------------------------------
	// STEP A: Fetch CSRF Token
	// -----------------------------------------------------------------------
	csrfReq, err := http.NewRequestWithContext(ctx, http.MethodGet, apiBaseURL+"/api/csrf-token", nil)
	if err != nil {
		t.Fatalf("failed to create csrf request: %v", err)
	}
	csrfResp, err := httpClient.Do(csrfReq)
	if err != nil {
		t.Fatalf("GET /api/csrf-token failed: %v", err)
	}
	defer csrfResp.Body.Close()

	if csrfResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK for csrf token, got %d", csrfResp.StatusCode)
	}

	var csrfData struct {
		CSRFToken string `json:"csrfToken"`
	}
	_ = json.NewDecoder(csrfResp.Body).Decode(&csrfData)
	if csrfData.CSRFToken == "" {
		t.Fatalf("expected non-empty csrfToken")
	}

	// -----------------------------------------------------------------------
	// STEP B: Manual Discovery Sync (POST /api/v1/discovery/sync)
	// -----------------------------------------------------------------------
	syncReq, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		apiBaseURL+"/api/v1/discovery/sync?workspace_id="+workspaceID,
		nil,
	)
	if err != nil {
		t.Fatalf("failed to create sync request: %v", err)
	}
	syncReq.Header.Set("X-Workspace-ID", workspaceID)
	syncReq.Header.Set("X-CSRF-Token", csrfData.CSRFToken)

	syncResp, err := httpClient.Do(syncReq)
	if err != nil {
		t.Fatalf("POST /api/v1/discovery/sync failed: %v", err)
	}
	defer syncResp.Body.Close()

	if syncResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK for discovery sync, got %d", syncResp.StatusCode)
	}

	var syncData api.DiscoverySyncResponse
	_ = json.NewDecoder(syncResp.Body).Decode(&syncData)
	if syncData.Status != "COMPLETED" {
		t.Errorf("expected COMPLETED status, got %s", syncData.Status)
	}
	if syncData.ResourcesTotal == 0 {
		t.Errorf("expected non-zero resources total, got 0")
	}
	if syncData.EvidenceCount == 0 {
		t.Errorf("expected non-zero evidence count, got 0")
	}

	// -----------------------------------------------------------------------
	// STEP C: Browse Resource Inventory (GET /api/v1/resources)
	// -----------------------------------------------------------------------
	listReq, _ := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		apiBaseURL+"/api/v1/resources?workspace_id="+workspaceID,
		nil,
	)
	listResp, err := httpClient.Do(listReq)
	if err != nil {
		t.Fatalf("GET /api/v1/resources failed: %v", err)
	}
	defer listResp.Body.Close()

	if listResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK for list resources, got %d", listResp.StatusCode)
	}

	var listData api.ListResourcesResponse
	_ = json.NewDecoder(listResp.Body).Decode(&listData)
	if listData.Total == 0 {
		t.Fatalf("expected discovered resources in store, got 0")
	}

	// Verify filtering by type
	filterReq, _ := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		apiBaseURL+"/api/v1/resources?workspace_id="+workspaceID+"&type=configmap",
		nil,
	)
	filterResp, _ := httpClient.Do(filterReq)
	defer filterResp.Body.Close()
	var filterData api.ListResourcesResponse
	_ = json.NewDecoder(filterResp.Body).Decode(&filterData)
	if filterData.Total != 1 || filterData.Resources[0].ResourceType != "configmap" {
		t.Errorf("expected 1 configmap, got %d", filterData.Total)
	}

	// Security: Verify cross-workspace access rejection
	crossReq, _ := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		apiBaseURL+"/api/v1/resources?workspace_id="+attackerWS,
		nil,
	)
	crossResp, _ := httpClient.Do(crossReq)
	defer crossResp.Body.Close()
	if crossResp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403 Forbidden for cross-workspace access, got %d", crossResp.StatusCode)
	}

	// -----------------------------------------------------------------------
	// STEP D: Inspect Selected Resource Details (GET /api/v1/resources/detail)
	// -----------------------------------------------------------------------
	configMapProviderID := fmt.Sprintf("%s/production/backend-config", clusterID)
	detailURL := fmt.Sprintf(
		"%s/api/v1/resources/detail?workspace_id=%s&provider=kubernetes&resource_type=configmap&provider_id=%s",
		apiBaseURL, workspaceID, configMapProviderID,
	)
	detailReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, detailURL, nil)
	detailResp, err := httpClient.Do(detailReq)
	if err != nil {
		t.Fatalf("GET /api/v1/resources/detail failed: %v", err)
	}
	defer detailResp.Body.Close()

	if detailResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK for resource detail, got %d", detailResp.StatusCode)
	}

	var detailData api.ResourceDetailResponse
	_ = json.NewDecoder(detailResp.Body).Decode(&detailData)
	if detailData.Resource.ProviderID != configMapProviderID {
		t.Errorf("expected provider ID %s, got %s", configMapProviderID, detailData.Resource.ProviderID)
	}
	if len(detailData.Relationships) == 0 {
		t.Errorf("expected connected relationships for backend-config, got 0")
	}

	// -----------------------------------------------------------------------
	// STEP E: Execute Impact Analysis (POST /api/v1/impact)
	// -----------------------------------------------------------------------
	// 1. UPDATE Change on ConfigMap
	updateBody, _ := json.Marshal(map[string]any{
		"target": map[string]string{
			"provider":      "kubernetes",
			"resource_type": "configmap",
			"provider_id":   configMapProviderID,
		},
		"direction":    "incoming",
		"max_depth":    3,
		"workspace_id": workspaceID,
		"proposed_change": map[string]string{
			"change_type": "UPDATE",
			"details":     "updating backend configuration",
		},
	})
	updateReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, apiBaseURL+"/api/v1/impact", bytes.NewReader(updateBody))
	updateReq.Header.Set("Content-Type", "application/json")
	updateReq.Header.Set("X-Workspace-ID", workspaceID)
	updateReq.Header.Set("X-CSRF-Token", csrfData.CSRFToken)

	updateResp, err := httpClient.Do(updateReq)
	if err != nil {
		t.Fatalf("POST /api/v1/impact (UPDATE) failed: %v", err)
	}
	defer updateResp.Body.Close()

	if updateResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK for UPDATE impact, got %d", updateResp.StatusCode)
	}

	var updateAnswer answer.ImpactAnswer
	_ = json.NewDecoder(updateResp.Body).Decode(&updateAnswer)
	if updateAnswer.Summary.MaxDepth > 1 {
		t.Errorf("expected max_depth 1 for UPDATE, got %d", updateAnswer.Summary.MaxDepth)
	}
	if updateAnswer.ChangeAssessment == nil || updateAnswer.ChangeAssessment.ChangeType != "UPDATE" {
		t.Errorf("expected UPDATE change assessment")
	}
	if len(updateAnswer.ImpactedResources) == 0 {
		t.Errorf("expected impacted pod for backend-config UPDATE, got 0")
	}

	// 2. SCALE Change
	scaleBody, _ := json.Marshal(map[string]any{
		"target": map[string]string{
			"provider":      "kubernetes",
			"resource_type": "configmap",
			"provider_id":   configMapProviderID,
		},
		"direction":    "incoming",
		"max_depth":    2,
		"workspace_id": workspaceID,
		"proposed_change": map[string]string{
			"change_type": "SCALE",
			"details":     "scaling target",
		},
	})
	scaleReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, apiBaseURL+"/api/v1/impact", bytes.NewReader(scaleBody))
	scaleReq.Header.Set("Content-Type", "application/json")
	scaleReq.Header.Set("X-Workspace-ID", workspaceID)
	scaleReq.Header.Set("X-CSRF-Token", csrfData.CSRFToken)

	scaleResp, _ := httpClient.Do(scaleReq)
	defer scaleResp.Body.Close()
	var scaleAnswer answer.ImpactAnswer
	_ = json.NewDecoder(scaleResp.Body).Decode(&scaleAnswer)
	if scaleAnswer.ChangeAssessment == nil || len(scaleAnswer.ChangeAssessment.Limitations) == 0 {
		t.Errorf("expected SCALE change assessment to disclose limitations")
	}

	// 3. REPLACE Change
	replaceBody, _ := json.Marshal(map[string]any{
		"target": map[string]string{
			"provider":      "kubernetes",
			"resource_type": "configmap",
			"provider_id":   configMapProviderID,
		},
		"direction":    "incoming",
		"max_depth":    2,
		"workspace_id": workspaceID,
		"proposed_change": map[string]string{
			"change_type": "REPLACE",
		},
	})
	replaceReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, apiBaseURL+"/api/v1/impact", bytes.NewReader(replaceBody))
	replaceReq.Header.Set("Content-Type", "application/json")
	replaceReq.Header.Set("X-Workspace-ID", workspaceID)
	replaceReq.Header.Set("X-CSRF-Token", csrfData.CSRFToken)

	replaceResp, _ := httpClient.Do(replaceReq)
	defer replaceResp.Body.Close()
	var replaceAnswer answer.ImpactAnswer
	_ = json.NewDecoder(replaceResp.Body).Decode(&replaceAnswer)
	if replaceAnswer.Summary.MaxDepth > 1 {
		t.Errorf("expected REPLACE depth clamped to 1, got %d", replaceAnswer.Summary.MaxDepth)
	}

	// 4. Legacy request (proposed_change omitted)
	legacyBody, _ := json.Marshal(map[string]any{
		"target": map[string]string{
			"provider":      "kubernetes",
			"resource_type": "configmap",
			"provider_id":   configMapProviderID,
		},
		"direction":    "incoming",
		"max_depth":    2,
		"workspace_id": workspaceID,
	})
	legacyReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, apiBaseURL+"/api/v1/impact", bytes.NewReader(legacyBody))
	legacyReq.Header.Set("Content-Type", "application/json")
	legacyReq.Header.Set("X-Workspace-ID", workspaceID)
	legacyReq.Header.Set("X-CSRF-Token", csrfData.CSRFToken)

	legacyResp, _ := httpClient.Do(legacyReq)
	defer legacyResp.Body.Close()
	var legacyAnswer answer.ImpactAnswer
	_ = json.NewDecoder(legacyResp.Body).Decode(&legacyAnswer)
	if legacyAnswer.ChangeAssessment != nil {
		t.Errorf("expected nil ChangeAssessment for legacy request, got %+v", legacyAnswer.ChangeAssessment)
	}

	// -----------------------------------------------------------------------
	// STEP F: Resilience & Zero State Deletion on Cluster Outage
	// -----------------------------------------------------------------------
	// Simulate cluster network failure
	k8sClient.pingErr = true

	failSyncReq, _ := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		apiBaseURL+"/api/v1/discovery/sync?workspace_id="+workspaceID,
		nil,
	)
	failSyncReq.Header.Set("X-Workspace-ID", workspaceID)
	failSyncReq.Header.Set("X-CSRF-Token", csrfData.CSRFToken)

	failSyncResp, err := httpClient.Do(failSyncReq)
	if err != nil {
		t.Fatalf("POST /api/v1/discovery/sync failed: %v", err)
	}
	defer failSyncResp.Body.Close()

	if failSyncResp.StatusCode != http.StatusBadGateway {
		t.Errorf("expected 502 Bad Gateway when cluster ping fails, got %d", failSyncResp.StatusCode)
	}

	// Crucial check: verify that previously persisted resources in PostgreSQL survive!
	persistedResources, err := pgStore.ListResources(ctx, workspaceID)
	if err != nil {
		t.Fatalf("failed to query store after failure: %v", err)
	}
	if len(persistedResources) == 0 {
		t.Fatalf("PERSISTENCE VIOLATION: resources were deleted on discovery failure")
	}
	t.Logf("Verified resilience: %d resources survived failed discovery sweep in PostgreSQL", len(persistedResources))
}
