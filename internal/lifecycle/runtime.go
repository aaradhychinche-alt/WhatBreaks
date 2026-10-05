package lifecycle

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// WorkTracker defines the work-tracking contract used by platform components to ensure
// clean in-flight task draining during shutdown.
type WorkTracker interface {
	TrackWork(ctx context.Context, name string, fn func(ctx context.Context) error) error
	ActiveCount() int
	WaitForIdle(ctx context.Context) error
}

// Runtime manages active task tracking, dependency ports, and work acceptance,
// faithfully migrating kubernetes/controller/runtime.js.
type Runtime struct {
	mu               sync.Mutex
	kubernetesClient Port
	reporter         Port

	acceptingWork atomic.Bool
	started       atomic.Bool

	activeWorkCount int64
	idleCh          chan struct{}
}

// NewRuntime creates a new Runtime instance with the specified client and reporter ports.
func NewRuntime(kubernetesClient Port, reporter Port) *Runtime {
	if kubernetesClient == nil {
		kubernetesClient = NewUnavailablePort()
	}
	if reporter == nil {
		reporter = NewUnavailablePort()
	}

	return &Runtime{
		kubernetesClient: kubernetesClient,
		reporter:         reporter,
	}
}

// Start initiates the reporter and client dependencies in the exact order required by runtime.js:
// 1. reporter.Start()
// 2. acceptingWork = true
// 3. kubernetesClient.Start()
// 4. started = true
func (r *Runtime) Start(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if err := r.reporter.Start(ctx); err != nil {
		r.acceptingWork.Store(false)
		return err
	}

	r.acceptingWork.Store(true)

	if err := r.kubernetesClient.Start(ctx); err != nil {
		r.acceptingWork.Store(false)
		return err
	}

	r.started.Store(true)
	return nil
}

// StopAcceptingWork sets acceptingWork to false and invokes stopAcceptingWork concurrently
// across kubernetesClient and reporter, settling all calls and returning the first failure if any.
func (r *Runtime) StopAcceptingWork(ctx context.Context) error {
	r.acceptingWork.Store(false)

	var wg sync.WaitGroup
	var clientErr, reporterErr error

	wg.Add(2)
	go func() {
		defer wg.Done()
		clientErr = r.kubernetesClient.StopAcceptingWork(ctx)
	}()
	go func() {
		defer wg.Done()
		reporterErr = r.reporter.StopAcceptingWork(ctx)
	}()
	wg.Wait()

	if clientErr != nil {
		return clientErr
	}
	return reporterErr
}

// TrackWork tracks the execution of a work item.
// If the controller is stopping (acceptingWork is false), it rejects the work immediately
// with ErrControllerStopping (code: CONTROLLER_STOPPING).
func (r *Runtime) TrackWork(fn func() error) error {
	if !r.acceptingWork.Load() {
		return ErrControllerStopping
	}

	r.mu.Lock()
	// Double check under lock to prevent race on shutdown transition
	if !r.acceptingWork.Load() {
		r.mu.Unlock()
		return ErrControllerStopping
	}
	r.activeWorkCount++
	if r.idleCh == nil {
		r.idleCh = make(chan struct{})
	}
	r.mu.Unlock()

	defer func() {
		r.mu.Lock()
		r.activeWorkCount--
		if r.activeWorkCount == 0 && r.idleCh != nil {
			close(r.idleCh)
			r.idleCh = nil
		}
		r.mu.Unlock()
	}()

	return fn()
}

// Track tracks an execution with context, satisfying functional task execution.
func (r *Runtime) Track(ctx context.Context, fn func(ctx context.Context) error) error {
	return r.TrackWork(func() error {
		return fn(ctx)
	})
}

// ActiveCount returns the number of currently active in-flight work items.
func (r *Runtime) ActiveCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return int(r.activeWorkCount)
}

// WaitForIdle waits until all active work items finish or the specified timeout expires.
// Returns true if all work completed (idle), or false if the timeout expired while work remained.
func (r *Runtime) WaitForIdle(ctx context.Context, timeout time.Duration) bool {
	r.mu.Lock()
	if r.activeWorkCount == 0 {
		r.mu.Unlock()
		return true
	}
	ch := r.idleCh
	r.mu.Unlock()

	if timeout <= 0 {
		return false
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-ch:
		return true
	case <-timer.C:
		return false
	case <-ctx.Done():
		return false
	}
}

// Close closes the reporter and kubernetesClient dependencies concurrently (all settled),
// matching runtime.js lines 92-100.
func (r *Runtime) Close(ctx context.Context) error {
	r.acceptingWork.Store(false)

	var wg sync.WaitGroup
	var reporterErr, clientErr error

	wg.Add(2)
	go func() {
		defer wg.Done()
		reporterErr = r.reporter.Close(ctx)
	}()
	go func() {
		defer wg.Done()
		clientErr = r.kubernetesClient.Close(ctx)
	}()
	wg.Wait()

	if reporterErr != nil {
		return reporterErr
	}
	return clientErr
}

// IsAlive reports whether the runtime has started and all dependent ports are alive.
func (r *Runtime) IsAlive() bool {
	return r.started.Load() &&
		r.kubernetesClient.IsAlive() &&
		r.reporter.IsAlive()
}

// IsReady reports whether the runtime has started, is accepting work, and all dependent ports are ready.
func (r *Runtime) IsReady() bool {
	return r.started.Load() &&
		r.acceptingWork.Load() &&
		r.kubernetesClient.IsReady() &&
		r.reporter.IsReady()
}

// IsAcceptingWork reports whether the runtime is currently accepting new work.
func (r *Runtime) IsAcceptingWork() bool {
	return r.acceptingWork.Load()
}

// IsStarted reports whether the runtime has completed its start sequence.
func (r *Runtime) IsStarted() bool {
	return r.started.Load()
}

// SimpleTracker is maintained for backward compatibility with internal/platform scaffolding.
type SimpleTracker struct {
	mu     sync.Mutex
	active int64
	idleCh chan struct{}
}

// NewSimpleTracker creates a SimpleTracker ready to monitor work.
func NewSimpleTracker() *SimpleTracker {
	ch := make(chan struct{}, 1)
	ch <- struct{}{}
	return &SimpleTracker{
		idleCh: ch,
	}
}

func (t *SimpleTracker) ActiveCount() int {
	return int(atomic.LoadInt64(&t.active))
}

func (t *SimpleTracker) TrackWork(ctx context.Context, name string, fn func(ctx context.Context) error) error {
	atomic.AddInt64(&t.active, 1)
	defer func() {
		if atomic.AddInt64(&t.active, -1) == 0 {
			select {
			case t.idleCh <- struct{}{}:
			default:
			}
		}
	}()
	return fn(ctx)
}

func (t *SimpleTracker) WaitForIdle(ctx context.Context) error {
	if t.ActiveCount() == 0 {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.idleCh:
		return nil
	}
}
