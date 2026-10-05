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

type mockMetricsRecorder struct {
	mu          sync.Mutex
	starts      []string
	finishes    []string
	failures    []string
	overlaps    []string
	durations   []time.Duration
	failureErrs []error
}

func (m *mockMetricsRecorder) RecordJobStart(worker, trigger string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.starts = append(m.starts, worker+":"+trigger)
}

func (m *mockMetricsRecorder) RecordJobFinish(worker, trigger string, d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.finishes = append(m.finishes, worker+":"+trigger)
	m.durations = append(m.durations, d)
}

func (m *mockMetricsRecorder) RecordJobFailure(worker, trigger string, d time.Duration, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failures = append(m.failures, worker+":"+trigger)
	m.failureErrs = append(m.failureErrs, err)
}

func (m *mockMetricsRecorder) RecordJobOverlap(worker, trigger string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.overlaps = append(m.overlaps, worker+":"+trigger)
}

func (m *mockMetricsRecorder) RecordHeartbeat(string, bool) {}

func TestTaskState_SuccessRun(t *testing.T) {
	var logBuf safeBuffer
	logger := logging.NewJSONLogger(&logBuf, logging.LevelDebug, "task-test")
	metrics := &mockMetricsRecorder{}

	job := &SimpleJob{
		JobName: "test-job",
		JobConfig: ScheduleConfig{
			Mode:     ModeInterval,
			Interval: 100 * time.Millisecond,
		},
		RunFn: func(ctx context.Context) error {
			time.Sleep(10 * time.Millisecond)
			return nil
		},
	}

	state, err := newTaskState(job)
	if err != nil {
		t.Fatalf("unexpected error creating task state: %v", err)
	}

	res, err := state.RunOnce(context.Background(), "manual", logger, metrics, false)
	if err != nil {
		t.Fatalf("unexpected error from RunOnce: %v", err)
	}

	if res.Status != StatusSuccess {
		t.Errorf("expected StatusSuccess, got %v", res.Status)
	}
	if res.Worker != "test-job" {
		t.Errorf("expected Worker test-job, got %v", res.Worker)
	}
	if res.Trigger != "manual" {
		t.Errorf("expected Trigger manual, got %v", res.Trigger)
	}
	if res.Duration < 10*time.Millisecond {
		t.Errorf("expected Duration >= 10ms, got %v", res.Duration)
	}

	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	if len(metrics.starts) != 1 || metrics.starts[0] != "test-job:manual" {
		t.Errorf("unexpected starts: %v", metrics.starts)
	}
	if len(metrics.finishes) != 1 || metrics.finishes[0] != "test-job:manual" {
		t.Errorf("unexpected finishes: %v", metrics.finishes)
	}
	if len(metrics.failures) != 0 {
		t.Errorf("unexpected failures: %v", metrics.failures)
	}

	logStr := logBuf.String()
	if !bytes.Contains(logBuf.Bytes(), []byte("worker-runner-job-start")) {
		t.Errorf("expected worker-runner-job-start log, got: %s", logStr)
	}
	if !bytes.Contains(logBuf.Bytes(), []byte("worker-runner-job-finish")) {
		t.Errorf("expected worker-runner-job-finish log, got: %s", logStr)
	}
}

func TestTaskState_FailureRun(t *testing.T) {
	var logBuf safeBuffer
	logger := logging.NewJSONLogger(&logBuf, logging.LevelDebug, "task-test")
	metrics := &mockMetricsRecorder{}
	expectedErr := errors.New("simulated worker explosion")

	job := &SimpleJob{
		JobName: "failing-job",
		JobConfig: ScheduleConfig{
			Mode:     ModeInterval,
			Interval: 100 * time.Millisecond,
		},
		RunFn: func(ctx context.Context) error {
			return expectedErr
		},
	}

	state, err := newTaskState(job)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Test with exitOnError = false
	res, err := state.RunOnce(context.Background(), "interval", logger, metrics, false)
	if err != nil {
		t.Fatalf("expected nil error when exitOnError=false, got: %v", err)
	}
	if res.Status != StatusFailed {
		t.Errorf("expected StatusFailed, got %v", res.Status)
	}
	if !errors.Is(res.Error, expectedErr) {
		t.Errorf("expected error %v, got %v", expectedErr, res.Error)
	}

	// Verify scheduler state is NOT wedged after failure: running must be false!
	if state.IsRunning() {
		t.Errorf("state.running should be false after failed task run")
	}

	// Second run can proceed
	res2, _ := state.RunOnce(context.Background(), "interval", logger, metrics, false)
	if res2.Status != StatusFailed {
		t.Errorf("expected StatusFailed again, got %v", res2.Status)
	}

	// Test with exitOnError = true
	_, errExit := state.RunOnce(context.Background(), "interval", logger, metrics, true)
	if !errors.Is(errExit, expectedErr) {
		t.Errorf("expected exitOnError to return error %v, got: %v", expectedErr, errExit)
	}
}

func TestTaskState_OverlapPrevention(t *testing.T) {
	var logBuf safeBuffer
	logger := logging.NewJSONLogger(&logBuf, logging.LevelDebug, "overlap-test")
	metrics := &mockMetricsRecorder{}

	var runCount atomic.Int32
	startGate := make(chan struct{})
	finishGate := make(chan struct{})

	job := &SimpleJob{
		JobName: "slow-job",
		JobConfig: ScheduleConfig{
			Mode:     ModeInterval,
			Interval: 50 * time.Millisecond,
		},
		RunFn: func(ctx context.Context) error {
			if runCount.Add(1) == 1 {
				close(startGate)
				<-finishGate
			}
			return nil
		},
	}

	state, err := newTaskState(job)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Start task in background
	var firstResult RunResult
	var firstErr error
	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		firstResult, firstErr = state.RunOnce(context.Background(), "tick-1", logger, metrics, false)
	}()

	// Wait for first task to actually start executing
	<-startGate
	if !state.IsRunning() {
		t.Fatalf("expected state.IsRunning() to be true while task is in flight")
	}

	// Trigger second run while first is still running -> must be SKIPPED due to overlap
	secondResult, secondErr := state.RunOnce(context.Background(), "tick-2", logger, metrics, false)
	if secondErr != nil {
		t.Fatalf("unexpected error on overlap run: %v", secondErr)
	}
	if secondResult.Status != StatusSkipped {
		t.Fatalf("expected StatusSkipped, got %v", secondResult.Status)
	}
	if secondResult.Worker != "slow-job" {
		t.Errorf("expected Worker slow-job, got %v", secondResult.Worker)
	}

	// Verify overlap metric recorded
	metrics.mu.Lock()
	if len(metrics.overlaps) != 1 || metrics.overlaps[0] != "slow-job:tick-2" {
		t.Errorf("expected overlap recorded, got: %v", metrics.overlaps)
	}
	metrics.mu.Unlock()

	// Verify log contains worker-runner-skip-overlap
	if !logBuf.Contains([]byte("worker-runner-skip-overlap")) {
		t.Errorf("expected worker-runner-skip-overlap log, got: %s", logBuf.String())
	}

	// Release first task
	close(finishGate)
	<-firstDone

	if firstErr != nil {
		t.Fatalf("unexpected error from first run: %v", firstErr)
	}
	if firstResult.Status != StatusSuccess {
		t.Errorf("expected firstResult StatusSuccess, got %v", firstResult.Status)
	}

	// Verify state is no longer running
	if state.IsRunning() {
		t.Errorf("expected state to not be running after completion")
	}

	// Subsequent execution can now proceed normally
	thirdResult, thirdErr := state.RunOnce(context.Background(), "tick-3", logger, metrics, false)
	if thirdErr != nil {
		t.Fatalf("unexpected error on third run: %v", thirdErr)
	}
	if thirdResult.Status != StatusSuccess {
		t.Errorf("expected StatusSuccess on third run, got %v", thirdResult.Status)
	}
}

func TestTaskState_MultipleDifferentTasks_DoNotBlock(t *testing.T) {
	task1Started := make(chan struct{})
	task1Release := make(chan struct{})
	task2Started := make(chan struct{})
	task2Release := make(chan struct{})

	jobA := &SimpleJob{
		JobName:   "job-a",
		JobConfig: ScheduleConfig{Mode: ModeInterval, Interval: time.Second},
		RunFn: func(ctx context.Context) error {
			close(task1Started)
			<-task1Release
			return nil
		},
	}

	jobB := &SimpleJob{
		JobName:   "job-b",
		JobConfig: ScheduleConfig{Mode: ModeInterval, Interval: time.Second},
		RunFn: func(ctx context.Context) error {
			close(task2Started)
			<-task2Release
			return nil
		},
	}

	stateA, _ := newTaskState(jobA)
	stateB, _ := newTaskState(jobB)

	var wg sync.WaitGroup
	var taskAStatus, taskBStatus RunStatus

	wg.Add(2)
	go func() {
		defer wg.Done()
		res, _ := stateA.RunOnce(context.Background(), "t", nil, nil, false)
		taskAStatus = res.Status
	}()
	go func() {
		defer wg.Done()
		res, _ := stateB.RunOnce(context.Background(), "t", nil, nil, false)
		taskBStatus = res.Status
	}()

	// Both tasks should start concurrently without blocking each other
	<-task1Started
	<-task2Started

	if !stateA.IsRunning() || !stateB.IsRunning() {
		t.Fatalf("both tasks should be running concurrently")
	}

	close(task1Release)
	close(task2Release)
	wg.Wait()

	if taskAStatus != StatusSuccess || taskBStatus != StatusSuccess {
		t.Errorf("both tasks should have succeeded: A=%v, B=%v", taskAStatus, taskBStatus)
	}
}

func TestTaskState_ConcurrentOverlap_Stress(t *testing.T) {
	var runCount atomic.Int32
	var inFlight atomic.Int32
	var maxInFlight atomic.Int32

	job := &SimpleJob{
		JobName:   "concurrent-stress",
		JobConfig: ScheduleConfig{Mode: ModeInterval, Interval: 10 * time.Millisecond},
		RunFn: func(ctx context.Context) error {
			cur := inFlight.Add(1)
			if cur > maxInFlight.Load() {
				maxInFlight.Store(cur)
			}
			time.Sleep(5 * time.Millisecond)
			inFlight.Add(-1)
			runCount.Add(1)
			return nil
		},
	}

	state, err := newTaskState(job)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	const goroutines = 20
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			_, _ = state.RunOnce(context.Background(), "stress", nil, nil, false)
		}()
	}

	wg.Wait()

	if maxInFlight.Load() > 1 {
		t.Errorf("overlap detected! max in-flight executions: %d (must be <= 1)", maxInFlight.Load())
	}
	if state.IsRunning() {
		t.Errorf("state still reports running after all goroutines finished")
	}
}
