package lifecycle

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRuntime_StartupOrder(t *testing.T) {
	var order []string
	var mu sync.Mutex

	record := func(action string) {
		mu.Lock()
		defer mu.Unlock()
		order = append(order, action)
	}

	reporter := &BasePort{
		StartFunc: func(ctx context.Context) error {
			record("reporter.start")
			return nil
		},
		Alive: true,
		Ready: true,
	}

	client := &BasePort{
		StartFunc: func(ctx context.Context) error {
			record("client.start")
			return nil
		},
		Alive: true,
		Ready: true,
	}

	r := NewRuntime(client, reporter)

	if r.IsStarted() {
		t.Fatalf("expected runtime not to be started initially")
	}
	if r.IsAcceptingWork() {
		t.Fatalf("expected runtime not to be accepting work initially")
	}

	if err := r.Start(context.Background()); err != nil {
		t.Fatalf("runtime start failed: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(order) != 2 || order[0] != "reporter.start" || order[1] != "client.start" {
		t.Fatalf("incorrect startup order: %v, expected [reporter.start, client.start]", order)
	}

	if !r.IsStarted() {
		t.Errorf("expected runtime to be marked started")
	}
	if !r.IsAcceptingWork() {
		t.Errorf("expected runtime to be accepting work")
	}
	if !r.IsAlive() {
		t.Errorf("expected runtime to report alive")
	}
	if !r.IsReady() {
		t.Errorf("expected runtime to report ready")
	}
}

func TestRuntime_StartupFailure_Reporter(t *testing.T) {
	reporterErr := errors.New("reporter failure")
	reporter := &BasePort{
		StartFunc: func(ctx context.Context) error {
			return reporterErr
		},
	}
	client := &BasePort{}

	r := NewRuntime(client, reporter)
	err := r.Start(context.Background())
	if !errors.Is(err, reporterErr) {
		t.Fatalf("expected %v, got %v", reporterErr, err)
	}
	if r.IsAcceptingWork() {
		t.Errorf("expected acceptingWork to remain false on reporter start failure")
	}
	if r.IsStarted() {
		t.Errorf("expected started to remain false on reporter start failure")
	}
}

func TestRuntime_StartupFailure_Client(t *testing.T) {
	clientErr := errors.New("client failure")
	reporter := &BasePort{
		StartFunc: func(ctx context.Context) error { return nil },
	}
	client := &BasePort{
		StartFunc: func(ctx context.Context) error { return clientErr },
	}

	r := NewRuntime(client, reporter)
	err := r.Start(context.Background())
	if !errors.Is(err, clientErr) {
		t.Fatalf("expected %v, got %v", clientErr, err)
	}
	if r.IsAcceptingWork() {
		t.Errorf("expected acceptingWork to be reset to false on client start failure")
	}
	if r.IsStarted() {
		t.Errorf("expected started to remain false on client start failure")
	}
}

func TestRuntime_WorkTrackingAndStopping(t *testing.T) {
	r := NewRuntime(NewUnavailablePort(), NewUnavailablePort())

	// Reject work before runtime start
	err := r.TrackWork(func() error { return nil })
	if !errors.Is(err, ErrControllerStopping) {
		t.Fatalf("expected ErrControllerStopping before start, got %v", err)
	}

	// Start runtime
	if err := r.Start(context.Background()); err != nil {
		t.Fatalf("failed to start: %v", err)
	}

	// Successful work execution
	executed := false
	err = r.TrackWork(func() error {
		executed = true
		if r.ActiveCount() != 1 {
			t.Errorf("expected active count 1 inside work, got %d", r.ActiveCount())
		}
		return nil
	})
	if err != nil {
		t.Fatalf("trackWork returned error: %v", err)
	}
	if !executed {
		t.Errorf("expected work function to execute")
	}
	if r.ActiveCount() != 0 {
		t.Errorf("expected active count 0 after work, got %d", r.ActiveCount())
	}

	// Stop accepting work
	if err := r.StopAcceptingWork(context.Background()); err != nil {
		t.Fatalf("stopAcceptingWork failed: %v", err)
	}
	if r.IsAcceptingWork() {
		t.Errorf("expected acceptingWork to be false after StopAcceptingWork")
	}

	// Reject work after stop
	err = r.TrackWork(func() error { return nil })
	if !errors.Is(err, ErrControllerStopping) {
		t.Fatalf("expected ErrControllerStopping after StopAcceptingWork, got %v", err)
	}
	if GetErrorCode(err, "") != "CONTROLLER_STOPPING" {
		t.Errorf("expected code CONTROLLER_STOPPING, got %q", GetErrorCode(err, ""))
	}
}

func TestRuntime_WaitForIdle(t *testing.T) {
	r := NewRuntime(NewUnavailablePort(), NewUnavailablePort())
	_ = r.Start(context.Background())

	// When active count is 0, returns true immediately
	if !r.WaitForIdle(context.Background(), 10*time.Millisecond) {
		t.Fatalf("expected immediate idle true when 0 active work")
	}

	// Launch in-flight tasks
	workStarted := make(chan struct{})
	workRelease := make(chan struct{})

	go func() {
		_ = r.TrackWork(func() error {
			close(workStarted)
			<-workRelease
			return nil
		})
	}()

	<-workStarted
	if r.ActiveCount() != 1 {
		t.Fatalf("expected active count 1, got %d", r.ActiveCount())
	}

	// WaitForIdle with short timeout should timeout and return false
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	startWait := time.Now()
	idle := r.WaitForIdle(ctx, 20*time.Millisecond)
	elapsed := time.Since(startWait)

	if idle {
		t.Errorf("expected waitForIdle to return false on timeout")
	}
	if elapsed < 15*time.Millisecond {
		t.Errorf("waitForIdle returned prematurely: %v", elapsed)
	}

	// Release work
	close(workRelease)

	// WaitForIdle should now return true
	idleAfterRelease := r.WaitForIdle(context.Background(), 200*time.Millisecond)
	if !idleAfterRelease {
		t.Errorf("expected waitForIdle to return true after work release")
	}
	if r.ActiveCount() != 0 {
		t.Errorf("expected active count 0, got %d", r.ActiveCount())
	}
}

func TestRuntime_ConcurrentCallers(t *testing.T) {
	r := NewRuntime(NewUnavailablePort(), NewUnavailablePort())
	_ = r.Start(context.Background())

	goroutines := 50
	iterations := 20
	var wg sync.WaitGroup
	var completed int64

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				err := r.TrackWork(func() error {
					time.Sleep(100 * time.Microsecond)
					atomic.AddInt64(&completed, 1)
					return nil
				})
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			}
		}()
	}

	wg.Wait()

	expectedTotal := int64(goroutines * iterations)
	if atomic.LoadInt64(&completed) != expectedTotal {
		t.Errorf("expected %d completed, got %d", expectedTotal, completed)
	}
	if !r.WaitForIdle(context.Background(), 100*time.Millisecond) {
		t.Errorf("expected idle after all goroutines finish")
	}
}

func TestRuntime_CloseConcurrently(t *testing.T) {
	var closedOrder []string
	var mu sync.Mutex

	reporter := &BasePort{
		CloseFunc: func(ctx context.Context) error {
			mu.Lock()
			closedOrder = append(closedOrder, "reporter")
			mu.Unlock()
			return nil
		},
	}
	client := &BasePort{
		CloseFunc: func(ctx context.Context) error {
			mu.Lock()
			closedOrder = append(closedOrder, "client")
			mu.Unlock()
			return nil
		},
	}

	r := NewRuntime(client, reporter)
	_ = r.Start(context.Background())

	if err := r.Close(context.Background()); err != nil {
		t.Fatalf("close failed: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(closedOrder) != 2 {
		t.Fatalf("expected both ports closed, got %v", closedOrder)
	}
	if r.IsAcceptingWork() {
		t.Errorf("expected acceptingWork false after close")
	}
}
