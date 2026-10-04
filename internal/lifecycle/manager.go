package lifecycle

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
)

// Phase represents the current execution stage of the platform daemon.
type Phase int

const (
	PhaseStarting Phase = iota
	PhaseRunning
	PhaseStopping
	PhaseStopped
	PhaseFailed
)

func (p Phase) String() string {
	switch p {
	case PhaseStarting:
		return "starting"
	case PhaseRunning:
		return "running"
	case PhaseStopping:
		return "stopping"
	case PhaseStopped:
		return "stopped"
	case PhaseFailed:
		return "failed"
	default:
		return "unknown"
	}
}

var (
	ErrStopping = errors.New("lifecycle: system is shutting down; not accepting new work")
)

// Manager coordinates platform lifecycle transitions and graceful teardown.
type Manager interface {
	Phase() Phase
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
	Wait() <-chan struct{}
}

// WorkTracker tracks in-flight operational tasks to ensure clean shutdown draining.
type WorkTracker interface {
	TrackWork(ctx context.Context, name string, fn func(ctx context.Context) error) error
	ActiveCount() int
	WaitForIdle(ctx context.Context) error
}

// SimpleTracker is a reference concurrency-safe work tracker for the platform skeleton.
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
