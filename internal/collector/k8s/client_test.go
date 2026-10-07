package k8s

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestClient_ListOperations(t *testing.T) {
	var receivedMethods []string
	var mu sync.Mutex

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		receivedMethods = append(receivedMethods, r.Method)
		mu.Unlock()

		// Verify read-only guarantee: every single request MUST be GET
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/namespaces":
			_ = json.NewEncoder(w).Encode(ResourceList[Namespace]{
				Items: []Namespace{
					{ObjectMeta: ObjectMeta{Name: "default"}, Status: NamespaceStatus{Phase: "Active"}},
				},
			})
		case "/api/v1/nodes":
			_ = json.NewEncoder(w).Encode(ResourceList[Node]{
				Items: []Node{
					{ObjectMeta: ObjectMeta{Name: "worker-1"}},
				},
			})
		case "/api/v1/namespaces/default/pods":
			_ = json.NewEncoder(w).Encode(ResourceList[Pod]{
				Items: []Pod{
					{ObjectMeta: ObjectMeta{Name: "web-pod-1", Namespace: "default"}},
				},
			})
		case "/apis/apps/v1/namespaces/default/deployments":
			_ = json.NewEncoder(w).Encode(ResourceList[Deployment]{
				Items: []Deployment{
					{ObjectMeta: ObjectMeta{Name: "web-deploy", Namespace: "default"}},
				},
			})
		case "/apis/apps/v1/namespaces/default/replicasets":
			_ = json.NewEncoder(w).Encode(ResourceList[ReplicaSet]{
				Items: []ReplicaSet{
					{ObjectMeta: ObjectMeta{Name: "web-rs", Namespace: "default"}},
				},
			})
		case "/api/v1/namespaces/default/services":
			_ = json.NewEncoder(w).Encode(ResourceList[Service]{
				Items: []Service{
					{ObjectMeta: ObjectMeta{Name: "web-svc", Namespace: "default"}},
				},
			})
		case "/apis/networking.k8s.io/v1/namespaces/default/ingresses":
			_ = json.NewEncoder(w).Encode(ResourceList[Ingress]{
				Items: []Ingress{
					{ObjectMeta: ObjectMeta{Name: "web-ingress", Namespace: "default"}},
				},
			})
		case "/api/v1/namespaces/default/configmaps":
			_ = json.NewEncoder(w).Encode(ResourceList[ConfigMap]{
				Items: []ConfigMap{
					{ObjectMeta: ObjectMeta{Name: "web-config", Namespace: "default"}},
				},
			})
		case "/api/v1/namespaces/default/persistentvolumeclaims":
			_ = json.NewEncoder(w).Encode(ResourceList[PersistentVolumeClaim]{
				Items: []PersistentVolumeClaim{
					{ObjectMeta: ObjectMeta{Name: "web-pvc", Namespace: "default"}},
				},
			})
		case "/api/v1/persistentvolumes":
			_ = json.NewEncoder(w).Encode(ResourceList[PersistentVolume]{
				Items: []PersistentVolume{
					{ObjectMeta: ObjectMeta{Name: "pv-01"}},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer mockServer.Close()

	client, err := NewRESTClient(ClientConfig{
		BaseURL: mockServer.URL,
	})
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	ctx := context.Background()

	// 1. Namespaces
	nsList, err := client.ListNamespaces(ctx)
	if err != nil || len(nsList) != 1 {
		t.Errorf("ListNamespaces failed: %v, count: %d", err, len(nsList))
	}

	// 2. Nodes
	nodeList, err := client.ListNodes(ctx)
	if err != nil || len(nodeList) != 1 {
		t.Errorf("ListNodes failed: %v, count: %d", err, len(nodeList))
	}

	// 3. Pods
	podList, err := client.ListPods(ctx, "default")
	if err != nil || len(podList) != 1 {
		t.Errorf("ListPods failed: %v, count: %d", err, len(podList))
	}

	// 4. Deployments
	depList, err := client.ListDeployments(ctx, "default")
	if err != nil || len(depList) != 1 {
		t.Errorf("ListDeployments failed: %v, count: %d", err, len(depList))
	}

	rsList, err := client.ListReplicaSets(ctx, "default")
	if err != nil || len(rsList) != 1 {
		t.Errorf("ListReplicaSets failed: %v, count: %d", err, len(rsList))
	}

	// 5. Services
	svcList, err := client.ListServices(ctx, "default")
	if err != nil || len(svcList) != 1 {
		t.Errorf("ListServices failed: %v, count: %d", err, len(svcList))
	}

	// 6. Ingresses
	ingList, err := client.ListIngresses(ctx, "default")
	if err != nil || len(ingList) != 1 {
		t.Errorf("ListIngresses failed: %v, count: %d", err, len(ingList))
	}

	// 7. ConfigMaps
	cmList, err := client.ListConfigMaps(ctx, "default")
	if err != nil || len(cmList) != 1 {
		t.Errorf("ListConfigMaps failed: %v, count: %d", err, len(cmList))
	}

	// 8. PVCs
	pvcList, err := client.ListPVCs(ctx, "default")
	if err != nil || len(pvcList) != 1 {
		t.Errorf("ListPVCs failed: %v, count: %d", err, len(pvcList))
	}

	// 9. PVs
	pvList, err := client.ListPVs(ctx)
	if err != nil || len(pvList) != 1 {
		t.Errorf("ListPVs failed: %v, count: %d", err, len(pvList))
	}

	// Verify all HTTP calls used GET
	mu.Lock()
	defer mu.Unlock()
	for _, method := range receivedMethods {
		if method != http.MethodGet {
			t.Errorf("non-GET method observed: %s", method)
		}
	}
}

func TestClient_ErrorClassification(t *testing.T) {
	tests := []struct {
		status  int
		wantErr error
	}{
		{http.StatusUnauthorized, ErrUnauthorized},
		{http.StatusForbidden, ErrForbidden},
		{http.StatusNotFound, ErrNotFound},
		{http.StatusTooManyRequests, ErrThrottled},
		{http.StatusInternalServerError, ErrServerUnavailable},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("status_%d", tt.status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
			}))
			defer server.Close()

			client, _ := NewRESTClient(ClientConfig{BaseURL: server.URL})
			_, err := client.ListNamespaces(context.Background())
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("expected error %v, got %v", tt.wantErr, err)
			}
		})
	}
}

func TestClient_WatchStreaming(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("watch") != "true" {
			http.Error(w, "watch query required", http.StatusBadRequest)
			return
		}

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)

		events := []string{
			`{"type":"ADDED","object":{"metadata":{"name":"pod-1","namespace":"default"}}}`,
			`{"type":"MODIFIED","object":{"metadata":{"name":"pod-1","namespace":"default"}}}`,
			`{"type":"DELETED","object":{"metadata":{"name":"pod-1","namespace":"default"}}}`,
		}

		for _, ev := range events {
			_, _ = fmt.Fprintf(w, "%s\n", ev)
			flusher.Flush()
			time.Sleep(10 * time.Millisecond)
		}
	}))
	defer mockServer.Close()

	client, err := NewRESTClient(ClientConfig{
		BaseURL: mockServer.URL,
	})
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	eventsCh, errsCh, err := client.Watch(ctx, "/api/v1/namespaces/default/pods", "0")
	if err != nil {
		t.Fatalf("Watch failed: %v", err)
	}

	var received []WatchEventType
	for ev := range eventsCh {
		received = append(received, ev.Type)
		if len(received) == 3 {
			break
		}
	}

	// Check if errors occurred
	select {
	case err := <-errsCh:
		if err != nil {
			t.Errorf("unexpected watch error: %v", err)
		}
	default:
	}

	if len(received) != 3 {
		t.Fatalf("expected 3 events, got %d", len(received))
	}
	if received[0] != WatchAdded || received[1] != WatchModified || received[2] != WatchDeleted {
		t.Errorf("unexpected event sequence: %v", received)
	}
}
