package scheduler

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aaradhychinche-alt/WhatBreaks/internal/logging"
)

type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (n int, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *safeBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.buf.Bytes()...)
}

func (b *safeBuffer) Contains(sub []byte) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return bytes.Contains(b.buf.Bytes(), sub)
}

func TestScheduler_Registration(t *testing.T) {
	s := New()

	// Valid interval job
	job1 := &SimpleJob{
		JobName:   "interval-job",
		JobConfig: ScheduleConfig{Mode: ModeInterval, Interval: 50 * time.Millisecond},
	}
	if err := s.Register(job1); err != nil {
		t.Fatalf("unexpected registration error: %v", err)
	}

	// Duplicate job name
	if err := s.Register(job1); err == nil {
		t.Fatalf("expected duplicate error, got nil")
	}

	// Invalid interval (<= 0)
	badIntervalJob := &SimpleJob{
		JobName:   "bad-interval",
		JobConfig: ScheduleConfig{Mode: ModeInterval, Interval: 0},
	}
	if err := s.Register(badIntervalJob); err == nil {
		t.Fatalf("expected error for zero interval")
	}

	// Valid cron job
	cronJob := &SimpleJob{
		JobName:   "cron-job",
		JobConfig: ScheduleConfig{Mode: ModeCron, CronExpression: "*/5 * * * *"},
	}
	if err := s.Register(cronJob); err != nil {
		t.Fatalf("unexpected cron registration error: %v", err)
	}

	// Invalid cron expression
	badCronJob := &SimpleJob{
		JobName:   "bad-cron",
		JobConfig: ScheduleConfig{Mode: ModeCron, CronExpression: "invalid-cron"},
	}
	if err := s.Register(badCronJob); err == nil {
		t.Fatalf("expected error for invalid cron")
	}

	// Nil job
	if err := s.Register(nil); err == nil {
		t.Fatalf("expected error for nil job")
	}

	// Job with empty name
	emptyNameJob := &SimpleJob{
		JobName:   "",
		JobConfig: ScheduleConfig{Mode: ModeInterval, Interval: time.Second},
	}
	if err := s.Register(emptyNameJob); err == nil {
		t.Fatalf("expected error for empty name job")
	}
}

func TestScheduler_RunOnce(t *testing.T) {
	var countA, countB atomic.Int32

	s := New()
	_ = s.Register(&SimpleJob{
		JobName:   "worker-a",
		JobConfig: ScheduleConfig{Mode: ModeInterval, Interval: time.Second},
		RunFn: func(ctx context.Context) error {
			countA.Add(1)
			return nil
		},
	})
	_ = s.Register(&SimpleJob{
		JobName:   "worker-b",
		JobConfig: ScheduleConfig{Mode: ModeInterval, Interval: time.Second},
		RunFn: func(ctx context.Context) error {
			countB.Add(1)
			return nil
		},
	})

	// Run specific worker
	results, err := s.RunOnce(context.Background(), "worker-a")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 || results[0].Worker != "worker-a" || results[0].Status != StatusSuccess {
		t.Errorf("unexpected results: %v", results)
	}
	if countA.Load() != 1 || countB.Load() != 0 {
		t.Errorf("expected countA=1, countB=0; got countA=%d, countB=%d", countA.Load(), countB.Load())
	}

	// Run all workers
	resultsAll, err := s.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resultsAll) != 2 {
		t.Errorf("expected 2 results, got %d", len(resultsAll))
	}
	if countA.Load() != 2 || countB.Load() != 1 {
		t.Errorf("expected countA=2, countB=1; got countA=%d, countB=%d", countA.Load(), countB.Load())
	}

	// Unknown worker error
	_, errUnknown := s.RunOnce(context.Background(), "non-existent")
	if errUnknown == nil {
		t.Fatalf("expected error for unknown worker")
	}
}

func TestScheduler_Interval_RunOnStartAndTicks(t *testing.T) {
	var startupCalled atomic.Bool
	var tickCount atomic.Int32

	s := New()
	_ = s.Register(&SimpleJob{
		JobName: "ticker-worker",
		JobConfig: ScheduleConfig{
			Mode:       ModeInterval,
			Interval:   20 * time.Millisecond,
			RunOnStart: true,
		},
		RunFn: func(ctx context.Context) error {
			if !startupCalled.Swap(true) {
				return nil
			}
			tickCount.Add(1)
			return nil
		},
	})

	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("unexpected start error: %v", err)
	}

	// Cannot register after start
	errReg := s.Register(&SimpleJob{
		JobName:   "late-job",
		JobConfig: ScheduleConfig{Mode: ModeInterval, Interval: time.Second},
	})
	if errReg == nil {
		t.Errorf("expected error registering after start")
	}

	// Wait for startup and a few ticks
	time.Sleep(70 * time.Millisecond)

	if err := s.Stop(context.Background()); err != nil {
		t.Fatalf("unexpected stop error: %v", err)
	}

	if !startupCalled.Load() {
		t.Errorf("expected startup to be called")
	}
	if tickCount.Load() < 1 {
		t.Errorf("expected at least 1 tick, got %d", tickCount.Load())
	}
}

func TestScheduler_GracefulShutdown_DrainActiveRuns(t *testing.T) {
	var logBuf safeBuffer
	logger := logging.NewJSONLogger(&logBuf, logging.LevelDebug, "shutdown-test")

	taskStarted := make(chan struct{})
	taskDrainFinished := make(chan struct{})

	s := New(
		WithLogger(logger),
		WithShutdownTimeout(2*time.Second),
	)

	_ = s.Register(&SimpleJob{
		JobName: "draining-worker",
		JobConfig: ScheduleConfig{
			Mode:       ModeInterval,
			Interval:   500 * time.Millisecond,
			RunOnStart: true,
		},
		RunFn: func(ctx context.Context) error {
			close(taskStarted)
			// Simulate work taking 100ms
			time.Sleep(100 * time.Millisecond)
			close(taskDrainFinished)
			return nil
		},
	})

	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("start error: %v", err)
	}

	// Wait until task starts
	<-taskStarted

	// Call Stop while task is in flight
	stopDone := make(chan error)
	go func() {
		stopDone <- s.Stop(context.Background())
	}()

	select {
	case err := <-stopDone:
		if err != nil {
			t.Fatalf("unexpected stop error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("Stop() timed out")
	}

	// Verify task finished before Stop returned
	select {
	case <-taskDrainFinished:
		// Succeeded
	default:
		t.Errorf("expected taskDrainFinished before Stop() completed")
	}

	if s.ActiveRuns() != 0 {
		t.Errorf("expected active runs 0, got %d", s.ActiveRuns())
	}

	logStr := logBuf.String()
	if !bytes.Contains(logBuf.Bytes(), []byte("worker-runner-stopping")) {
		t.Errorf("expected worker-runner-stopping log, got: %s", logStr)
	}
	if bytes.Contains(logBuf.Bytes(), []byte("worker-runner-shutdown-timeout")) {
		t.Errorf("should not have logged shutdown timeout")
	}
}

func TestScheduler_ShutdownTimeout(t *testing.T) {
	var logBuf safeBuffer
	logger := logging.NewJSONLogger(&logBuf, logging.LevelDebug, "shutdown-timeout-test")

	taskStarted := make(chan struct{})
	taskRelease := make(chan struct{})

	// 50ms shutdown timeout
	s := New(
		WithLogger(logger),
		WithShutdownTimeout(50*time.Millisecond),
	)

	_ = s.Register(&SimpleJob{
		JobName: "stuck-worker",
		JobConfig: ScheduleConfig{
			Mode:       ModeInterval,
			Interval:   time.Hour,
			RunOnStart: true,
		},
		RunFn: func(ctx context.Context) error {
			close(taskStarted)
			<-taskRelease
			return nil
		},
	})

	_ = s.Start(context.Background())
	<-taskStarted

	startStop := time.Now()
	err := s.Stop(context.Background())
	stopDuration := time.Since(startStop)

	if err != nil {
		t.Fatalf("stop error: %v", err)
	}

	// Stop should return around 50ms (the shutdown timeout), not hang indefinitely
	if stopDuration > 500*time.Millisecond {
		t.Errorf("Stop took too long: %v (expected ~50ms)", stopDuration)
	}

	if !logBuf.Contains([]byte("worker-runner-shutdown-timeout")) {
		t.Errorf("expected worker-runner-shutdown-timeout log, got: %s", logBuf.String())
	}

	// Release stuck worker
	close(taskRelease)
}

func TestScheduler_ShutdownIdempotencyAndConcurrency(t *testing.T) {
	var dbClosedCount atomic.Int32

	s := New(
		WithShutdownTimeout(time.Second),
		WithDBClose(func() error {
			dbClosedCount.Add(1)
			return nil
		}),
	)

	_ = s.Register(&SimpleJob{
		JobName:   "dummy",
		JobConfig: ScheduleConfig{Mode: ModeInterval, Interval: time.Second},
	})
	_ = s.Start(context.Background())

	const callers = 25
	var wg sync.WaitGroup
	wg.Add(callers)

	errorsList := make([]error, callers)
	for i := 0; i < callers; i++ {
		idx := i
		go func() {
			defer wg.Done()
			errorsList[idx] = s.Stop(context.Background())
		}()
	}

	wg.Wait()

	for _, err := range errorsList {
		if err != nil {
			t.Errorf("unexpected error from concurrent Stop: %v", err)
		}
	}

	if dbClosedCount.Load() != 1 {
		t.Errorf("expected DB close to be called exactly once, got %d", dbClosedCount.Load())
	}

	// Sequential call after already stopped
	if err := s.Stop(context.Background()); err != nil {
		t.Errorf("expected nil on subsequent Stop, got: %v", err)
	}
}

func TestScheduler_DBCloseError(t *testing.T) {
	var logBuf safeBuffer
	logger := logging.NewJSONLogger(&logBuf, logging.LevelDebug, "db-close-test")
	dbErr := errors.New("simulated db pool close failure")

	s := New(
		WithLogger(logger),
		WithShutdownTimeout(time.Second),
		WithDBClose(func() error {
			return dbErr
		}),
	)

	_ = s.Register(&SimpleJob{
		JobName:   "test",
		JobConfig: ScheduleConfig{Mode: ModeInterval, Interval: time.Second},
	})
	_ = s.Start(context.Background())

	err := s.Stop(context.Background())
	if !errors.Is(err, dbErr) {
		t.Errorf("expected dbErr, got: %v", err)
	}

	if !logBuf.Contains([]byte("worker-runner-pool-close-failure")) {
		t.Errorf("expected worker-runner-pool-close-failure log, got: %s", logBuf.String())
	}
}

func TestScheduler_ExitOnError(t *testing.T) {
	var logBuf safeBuffer
	logger := logging.NewJSONLogger(&logBuf, logging.LevelDebug, "exit-on-error-test")
	failErr := errors.New("fatal worker error")

	s := New(
		WithLogger(logger),
		WithExitOnError(true),
		WithShutdownTimeout(time.Second),
	)

	_ = s.Register(&SimpleJob{
		JobName: "faulty-worker",
		JobConfig: ScheduleConfig{
			Mode:       ModeInterval,
			Interval:   time.Hour,
			RunOnStart: true,
		},
		RunFn: func(ctx context.Context) error {
			return failErr
		},
	})

	_ = s.Start(context.Background())

	// Wait up to 2 seconds for scheduler to complete shutdown
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if s.IsStopped() {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if !s.IsStopped() {
		t.Errorf("expected scheduler to be stopped on fatal worker error")
	}

	if !logBuf.Contains([]byte("worker-runner-exit-on-error")) {
		t.Errorf("expected worker-runner-exit-on-error log, got: %s", logBuf.String())
	}
}

func TestScheduler_SecretRedactionInLogs(t *testing.T) {
	var logBuf safeBuffer
	logger := logging.NewJSONLogger(&logBuf, logging.LevelDebug, "security-test")

	s := New(WithLogger(logger))
	_ = s.Register(&SimpleJob{
		JobName: "secure-worker",
		JobConfig: ScheduleConfig{
			Mode:     ModeInterval,
			Interval: time.Hour,
		},
		RunFn: func(ctx context.Context) error {
			return errors.New("failed with Authorization: Bearer super-secret-token-xyz and token=secret-pass-123")
		},
	})

	_, _ = s.RunOnce(context.Background())

	logOutput := logBuf.String()
	if logBuf.Contains([]byte("super-secret-token-xyz")) {
		t.Fatalf("CRITICAL SECURITY LEAK: sensitive token leaked in log: %s", logOutput)
	}
	if !logBuf.Contains([]byte("[REDACTED]")) {
		t.Errorf("expected [REDACTED] in sanitized log, got: %s", logOutput)
	}
}
