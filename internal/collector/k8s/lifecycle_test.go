package k8s

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestCollector_Lifecycle(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client, err := NewRESTClient(ClientConfig{BaseURL: server.URL})
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	collector, err := New(WithClient(client))
	if err != nil {
		t.Fatalf("failed to create collector: %v", err)
	}

	if collector.IsAlive() || collector.IsReady() {
		t.Fatalf("collector should not be alive or ready prior to Start")
	}

	ctx := context.Background()
	if err := collector.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	if !collector.IsAlive() || !collector.IsReady() {
		t.Errorf("collector should be alive and ready after Start")
	}

	// Double start should fail
	if err := collector.Start(ctx); err == nil {
		t.Errorf("expected error on duplicate Start")
	}

	// StopAcceptingWork flips ready to false
	if err := collector.StopAcceptingWork(ctx); err != nil {
		t.Errorf("StopAcceptingWork failed: %v", err)
	}
	if collector.IsReady() {
		t.Errorf("expected ready to be false after StopAcceptingWork")
	}

	stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := collector.Stop(stopCtx); err != nil {
		t.Fatalf("Stop failed: %v", err)
	}

	if collector.IsAlive() || collector.IsReady() {
		t.Errorf("collector should not be alive or ready after Stop")
	}

	// Repeated Stop is a safe no-op
	if err := collector.Stop(stopCtx); err != nil {
		t.Errorf("repeated Stop should succeed, got: %v", err)
	}
}

func TestCollector_ConcurrentStop(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client, _ := NewRESTClient(ClientConfig{BaseURL: server.URL})
	collector, _ := New(WithClient(client))

	ctx := context.Background()
	if err := collector.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	var wg sync.WaitGroup
	errs := make(chan error, 10)

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if err := collector.Stop(stopCtx); err != nil {
				errs <- err
			}
		}()
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("concurrent Stop failed: %v", err)
	}

	if collector.IsAlive() {
		t.Errorf("expected collector not alive after concurrent stop")
	}
}
