package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aaradhychinche-alt/WhatBreaks/internal/api"
	"github.com/aaradhychinche-alt/WhatBreaks/internal/logging"
	"github.com/aaradhychinche-alt/WhatBreaks/internal/state"
)

func TestListResources_SuccessAndFiltering(t *testing.T) {
	wsID := "00000000-0000-0000-0000-000000000001"
	memStore := state.NewMemoryStore()

	now := time.Now().UTC()
	err := memStore.SaveResources(context.Background(), wsID, []state.Resource{
		{
			WorkspaceID: wsID,
			Identity: state.ResourceIdentity{
				Provider:     "kubernetes",
				ResourceType: "deployment",
				ProviderID:   "k3d-cluster/default/backend",
			},
			FirstObservedAt: now.Add(-5 * time.Minute),
			LastObservedAt:  now,
		},
		{
			WorkspaceID: wsID,
			Identity: state.ResourceIdentity{
				Provider:     "kubernetes",
				ResourceType: "pod",
				ProviderID:   "k3d-cluster/default/backend-pod-1",
			},
			FirstObservedAt: now.Add(-5 * time.Minute),
			LastObservedAt:  now,
		},
		{
			WorkspaceID: wsID,
			Identity: state.ResourceIdentity{
				Provider:     "kubernetes",
				ResourceType: "service",
				ProviderID:   "k3d-cluster/default/backend-svc",
			},
			FirstObservedAt: now.Add(-5 * time.Minute),
			LastObservedAt:  now,
		},
	})
	if err != nil {
		t.Fatalf("failed to seed resources: %v", err)
	}

	cfg := api.DefaultConfig()
	cfg.IsTest = true
	cfg.Store = memStore
	cfg.ConfiguredWorkspaceID = wsID
	cfg.Logger = logging.NewStandardLogger(nil, logging.LevelWarn)

	srv := api.NewServer(cfg)

	// 1. List all resources
	req := httptest.NewRequest(http.MethodGet, "/api/v1/resources?workspace_id="+wsID, nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp api.ListResourcesResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	if resp.Total != 3 {
		t.Errorf("expected 3 resources, got %d", resp.Total)
	}
	if resp.WorkspaceID != wsID {
		t.Errorf("expected workspace ID %s, got %s", wsID, resp.WorkspaceID)
	}

	// 2. Filter by type
	reqType := httptest.NewRequest(http.MethodGet, "/api/v1/resources?workspace_id="+wsID+"&type=deployment", nil)
	recType := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recType, reqType)

	if recType.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", recType.Code)
	}
	var respType api.ListResourcesResponse
	_ = json.Unmarshal(recType.Body.Bytes(), &respType)
	if respType.Total != 1 || respType.Resources[0].ResourceType != "deployment" {
		t.Errorf("expected 1 deployment, got %d", respType.Total)
	}

	// 3. Search by name
	reqSearch := httptest.NewRequest(http.MethodGet, "/api/v1/resources?workspace_id="+wsID+"&search=svc", nil)
	recSearch := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recSearch, reqSearch)

	if recSearch.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", recSearch.Code)
	}
	var respSearch api.ListResourcesResponse
	_ = json.Unmarshal(recSearch.Body.Bytes(), &respSearch)
	if respSearch.Total != 1 || respSearch.Resources[0].ResourceType != "service" {
		t.Errorf("expected 1 service matching 'svc', got %d", respSearch.Total)
	}
}

func TestListResources_WorkspaceSecurity(t *testing.T) {
	wsID := "00000000-0000-0000-0000-000000000001"
	attackerWS := "00000000-0000-0000-0000-000000000002"
	memStore := state.NewMemoryStore()

	cfg := api.DefaultConfig()
	cfg.IsTest = true
	cfg.Store = memStore
	cfg.ConfiguredWorkspaceID = wsID
	cfg.Logger = logging.NewStandardLogger(nil, logging.LevelWarn)

	srv := api.NewServer(cfg)

	// 1. Cross-workspace request targeting unauthorized workspace
	reqCross := httptest.NewRequest(http.MethodGet, "/api/v1/resources?workspace_id="+attackerWS, nil)
	recCross := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recCross, reqCross)

	if recCross.Code != http.StatusForbidden {
		t.Errorf("expected 403 Forbidden for unconfigured workspace, got %d", recCross.Code)
	}

	// 2. Malformed UUID
	reqBadUUID := httptest.NewRequest(http.MethodGet, "/api/v1/resources?workspace_id=invalid-uuid-123", nil)
	recBadUUID := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recBadUUID, reqBadUUID)

	if recBadUUID.Code != http.StatusBadRequest {
		t.Errorf("expected 400 Bad Request for malformed UUID, got %d", recBadUUID.Code)
	}
}

func TestResourceDetail_SuccessAndProvenance(t *testing.T) {
	wsID := "00000000-0000-0000-0000-000000000001"
	memStore := state.NewMemoryStore()

	deployID := state.ResourceIdentity{
		Provider:     "kubernetes",
		ResourceType: "deployment",
		ProviderID:   "k3d-cluster/default/backend",
	}
	podID := state.ResourceIdentity{
		Provider:     "kubernetes",
		ResourceType: "pod",
		ProviderID:   "k3d-cluster/default/backend-pod",
	}

	now := time.Now().UTC()
	_ = memStore.SaveResources(context.Background(), wsID, []state.Resource{
		{WorkspaceID: wsID, Identity: deployID, FirstObservedAt: now, LastObservedAt: now},
		{WorkspaceID: wsID, Identity: podID, FirstObservedAt: now, LastObservedAt: now},
	})

	relKey := state.RelationshipKey{
		Source: deployID,
		Target: podID,
		Kind:   "OWNS",
	}
	_ = memStore.SaveRelationships(context.Background(), wsID, []state.Relationship{
		{
			WorkspaceID:     wsID,
			Source:          deployID,
			Target:          podID,
			Kind:            "OWNS",
			Category:        "STRUCTURAL",
			FirstObservedAt: now,
			LastObservedAt:  now,
		},
	})
	_ = memStore.SaveProvenance(context.Background(), wsID, []state.ProvenanceAssociation{
		{
			WorkspaceID:  wsID,
			Relationship: relKey,
			EvidenceID:   "ev-owner-ref-123",
		},
	})

	cfg := api.DefaultConfig()
	cfg.IsTest = true
	cfg.Store = memStore
	cfg.ConfiguredWorkspaceID = wsID
	cfg.Logger = logging.NewStandardLogger(nil, logging.LevelWarn)

	srv := api.NewServer(cfg)

	url := "/api/v1/resources/detail?workspace_id=" + wsID +
		"&provider=kubernetes&resource_type=deployment&provider_id=k3d-cluster/default/backend"
	req := httptest.NewRequest(http.MethodGet, url, nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", rec.Code, rec.Body.String())
	}

	var detail api.ResourceDetailResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
		t.Fatalf("failed to decode detail response: %v", err)
	}

	if detail.Resource.ProviderID != deployID.ProviderID {
		t.Errorf("expected provider ID %s, got %s", deployID.ProviderID, detail.Resource.ProviderID)
	}
	if len(detail.Relationships) != 1 {
		t.Fatalf("expected 1 relationship, got %d", len(detail.Relationships))
	}

	rel := detail.Relationships[0]
	if rel.Direction != "OUTGOING" {
		t.Errorf("expected direction OUTGOING, got %s", rel.Direction)
	}
	if rel.Kind != "OWNS" {
		t.Errorf("expected kind OWNS, got %s", rel.Kind)
	}
	if len(rel.EvidenceIDs) == 0 || rel.EvidenceIDs[0] != "ev-owner-ref-123" {
		t.Errorf("expected provenance evidence ID ev-owner-ref-123, got %v", rel.EvidenceIDs)
	}
}

func TestResourceDetail_NotFound(t *testing.T) {
	wsID := "00000000-0000-0000-0000-000000000001"
	memStore := state.NewMemoryStore()

	cfg := api.DefaultConfig()
	cfg.IsTest = true
	cfg.Store = memStore
	cfg.ConfiguredWorkspaceID = wsID
	cfg.Logger = logging.NewStandardLogger(nil, logging.LevelWarn)

	srv := api.NewServer(cfg)

	url := "/api/v1/resources/detail?workspace_id=" + wsID +
		"&provider=kubernetes&resource_type=pod&provider_id=nonexistent-pod"
	req := httptest.NewRequest(http.MethodGet, url, nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404 Not Found, got %d", rec.Code)
	}
}

func TestListResources_WorkspaceSecurity_NoAuthorizer_NoConfiguredWS(t *testing.T) {
	memStore := state.NewMemoryStore()

	cfg := api.DefaultConfig()
	cfg.IsTest = true
	cfg.Store = memStore
	cfg.ConfiguredWorkspaceID = "" // No configured workspace
	cfg.WorkspaceAuthorizer = nil  // No authorizer
	cfg.Logger = logging.NewStandardLogger(nil, logging.LevelWarn)

	srv := api.NewServer(cfg)

	// An arbitrary valid UUID must still be rejected!
	req := httptest.NewRequest(http.MethodGet, "/api/v1/resources?workspace_id=00000000-0000-0000-0000-000000000001", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("expected 403 Forbidden when neither authorizer nor configured WS exists, got %d", rec.Code)
	}
}
