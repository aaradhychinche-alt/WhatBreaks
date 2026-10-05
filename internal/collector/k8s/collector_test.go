package k8s

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func createMockKubeServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		p := r.URL.Path

		switch {
		case p == "/api/v1/namespaces":
			_ = json.NewEncoder(w).Encode(ResourceList[Namespace]{
				Items: []Namespace{{ObjectMeta: ObjectMeta{Name: "app-prod"}}},
			})
		case p == "/api/v1/nodes":
			_ = json.NewEncoder(w).Encode(ResourceList[Node]{
				Items: []Node{{ObjectMeta: ObjectMeta{Name: "k8s-node-1"}}},
			})
		case p == "/api/v1/persistentvolumes":
			_ = json.NewEncoder(w).Encode(ResourceList[PersistentVolume]{
				Items: []PersistentVolume{{ObjectMeta: ObjectMeta{Name: "pv-storage-1"}}},
			})
		case strings.Contains(p, "/deployments"):
			_ = json.NewEncoder(w).Encode(ResourceList[Deployment]{
				Items: []Deployment{{ObjectMeta: ObjectMeta{Name: "web-deploy", Namespace: "app-prod"}}},
			})
		case strings.Contains(p, "/services"):
			_ = json.NewEncoder(w).Encode(ResourceList[Service]{
				Items: []Service{{
					ObjectMeta: ObjectMeta{Name: "web-svc", Namespace: "app-prod"},
					Spec:       ServiceSpec{ClusterIP: "10.0.0.100", Ports: []ServicePort{{Port: 80}}},
				}},
			})
		case strings.Contains(p, "/ingresses"):
			_ = json.NewEncoder(w).Encode(ResourceList[Ingress]{
				Items: []Ingress{{ObjectMeta: ObjectMeta{Name: "web-ing", Namespace: "app-prod"}}},
			})
		case strings.Contains(p, "/pods"):
			_ = json.NewEncoder(w).Encode(ResourceList[Pod]{
				Items: []Pod{{
					ObjectMeta: ObjectMeta{Name: "web-pod-xyz", Namespace: "app-prod"},
					Status:     PodStatus{PodIP: "10.244.0.5", Phase: "Running"},
				}},
			})
		case strings.Contains(p, "/configmaps"):
			_ = json.NewEncoder(w).Encode(ResourceList[ConfigMap]{
				Items: []ConfigMap{{ObjectMeta: ObjectMeta{Name: "app-cfg", Namespace: "app-prod"}}},
			})
		case strings.Contains(p, "/secrets"):
			_ = json.NewEncoder(w).Encode(ResourceList[SecretMetadata]{
				Items: []SecretMetadata{{ObjectMeta: ObjectMeta{Name: "app-sec", Namespace: "app-prod"}}},
			})
		case strings.Contains(p, "/persistentvolumeclaims"):
			_ = json.NewEncoder(w).Encode(ResourceList[PersistentVolumeClaim]{
				Items: []PersistentVolumeClaim{{ObjectMeta: ObjectMeta{Name: "app-pvc", Namespace: "app-prod"}}},
			})
		case p == "/readyz":
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestCollector_ClusterWideCollect(t *testing.T) {
	server := createMockKubeServer(t)
	defer server.Close()

	client, err := NewRESTClient(ClientConfig{BaseURL: server.URL})
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	collector, err := New(
		WithClusterID("prod-cluster"),
		WithWorkspaceID("ws-123"),
		WithClusterWide(true),
		WithNamespaces("app-prod"),
		WithClient(client),
	)
	if err != nil {
		t.Fatalf("failed to create collector: %v", err)
	}

	ctx := context.Background()
	if err := collector.Start(ctx); err != nil {
		t.Fatalf("failed to start collector: %v", err)
	}
	defer func() { _ = collector.Stop(ctx) }()

	evs, err := collector.Collect(ctx)
	if err != nil {
		t.Fatalf("Collect failed: %v", err)
	}

	if len(evs) == 0 {
		t.Fatalf("expected evidence to be collected, got 0")
	}

	// Verify cluster-scoped evidence exists
	var foundNS, foundNode, foundDeploy, foundSvc bool
	for _, ev := range evs {
		if ev.Subject.ResourceType == TypeNamespace {
			foundNS = true
		}
		if ev.Subject.ResourceType == TypeNode {
			foundNode = true
		}
		if ev.Subject.ResourceType == TypeDeployment {
			foundDeploy = true
		}
		if ev.Subject.ResourceType == TypeService {
			foundSvc = true
		}
	}

	if !foundNS || !foundNode || !foundDeploy || !foundSvc {
		t.Errorf("missing expected resources in sweep: ns=%v, node=%v, deploy=%v, svc=%v",
			foundNS, foundNode, foundDeploy, foundSvc)
	}
}

func TestCollector_NamespaceScoped_GracefulRBACDenial(t *testing.T) {
	// Mock server that returns 403 Forbidden for "forbidden-ns" but 200 OK for "allowed-ns"
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "forbidden-ns") {
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(map[string]string{"message": "forbidden namespace"})
			return
		}
		if strings.Contains(r.URL.Path, "/deployments") {
			_ = json.NewEncoder(w).Encode(ResourceList[Deployment]{
				Items: []Deployment{{ObjectMeta: ObjectMeta{Name: "allowed-app", Namespace: "allowed-ns"}}},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(ResourceList[Deployment]{Items: []Deployment{}})
	}))
	defer mockServer.Close()

	client, err := NewRESTClient(ClientConfig{BaseURL: mockServer.URL})
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	collector, err := New(
		WithClusterID("prod-1"),
		WithClusterWide(false),
		WithNamespaces("forbidden-ns", "allowed-ns"),
		WithClient(client),
	)
	if err != nil {
		t.Fatalf("failed to create collector: %v", err)
	}

	ctx := context.Background()
	evs, err := collector.Collect(ctx)
	if err != nil {
		t.Fatalf("Collect should succeed gracefully on partial RBAC failure, got: %v", err)
	}

	// Should have collected allowed-app despite forbidden-ns error
	foundAllowed := false
	for _, ev := range evs {
		if ev.Subject.ProviderId == "prod-1/allowed-ns/allowed-app" {
			foundAllowed = true
		}
	}

	if !foundAllowed {
		t.Errorf("expected to collect allowed-ns resources despite forbidden-ns RBAC error")
	}
}

func TestCollector_CoreClientNotBound(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer mockServer.Close()

	client, _ := NewRESTClient(ClientConfig{BaseURL: mockServer.URL})
	collector, _ := New(WithClient(client))

	_, err := collector.SubmitToCore(context.Background(), nil)
	if !strings.Contains(err.Error(), "core client is not configured") {
		t.Errorf("expected core client error, got: %v", err)
	}
}
