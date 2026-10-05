package lifecycle

import (
	"bytes"
	"context"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/aaradhychinche-alt/WhatBreaks/internal/logging"
)

// mockHealthServer implements HealthServer for testing.
type mockHealthServer struct {
	mu          sync.Mutex
	listenFunc  func() error
	closeFunc   func() error
	listenCalls int
	closeCalls  int
}

func (m *mockHealthServer) Listen() error {
	m.mu.Lock()
	m.listenCalls++
	fn := m.listenFunc
	m.mu.Unlock()
	if fn != nil {
		return fn()
	}
	return nil
}

func (m *mockHealthServer) Close() error {
	m.mu.Lock()
	m.closeCalls++
	fn := m.closeFunc
	m.mu.Unlock()
	if fn != nil {
		return fn()
	}
	return nil
}

// mockRuntimeController implements RuntimeController for testing.
type mockRuntimeController struct {
	mu                    sync.Mutex
	startFunc             func(ctx context.Context) error
	stopAcceptingWorkFunc func(ctx context.Context) error
	waitForIdleFunc       func(ctx context.Context, timeout time.Duration) bool
	closeFunc             func(ctx context.Context) error
	alive                 bool
	ready                 bool
	startCalls            int
	stopCalls             int
	waitCalls             int
	closeCalls            int
}

func (m *mockRuntimeController) Start(ctx context.Context) error {
	m.mu.Lock()
	m.startCalls++
	fn := m.startFunc
	m.mu.Unlock()
	if fn != nil {
		return fn(ctx)
	}
	return nil
}

func (m *mockRuntimeController) StopAcceptingWork(ctx context.Context) error {
	m.mu.Lock()
	m.stopCalls++
	fn := m.stopAcceptingWorkFunc
	m.mu.Unlock()
	if fn != nil {
		return fn(ctx)
	}
	return nil
}

func (m *mockRuntimeController) WaitForIdle(ctx context.Context, timeout time.Duration) bool {
	m.mu.Lock()
	m.waitCalls++
	fn := m.waitForIdleFunc
	m.mu.Unlock()
	if fn != nil {
		return fn(ctx, timeout)
	}
	return true
}

func (m *mockRuntimeController) Close(ctx context.Context) error {
	m.mu.Lock()
	m.closeCalls++
	fn := m.closeFunc
	m.mu.Unlock()
	if fn != nil {
		return fn(ctx)
	}
	return nil
}

func (m *mockRuntimeController) IsAlive() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.alive
}

func (m *mockRuntimeController) IsReady() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.ready
}

func TestPhase_StateTransitions(t *testing.T) {
	// Valid transitions
	validPairs := [][2]Phase{
		{PhaseStarting, PhaseRunning},
		{PhaseStarting, PhaseStopping},
		{PhaseStarting, PhaseFailed},
		{PhaseRunning, PhaseStopping},
		{PhaseRunning, PhaseFailed},
		{PhaseStopping, PhaseStopped},
		{PhaseStopping, PhaseFailed},
	}

	for _, pair := range validPairs {
		if !IsValidTransition(pair[0], pair[1]) {
			t.Errorf("expected transition %s -> %s to be valid", pair[0], pair[1])
		}
	}

	// Invalid transitions
	invalidPairs := [][2]Phase{
		{PhaseStopped, PhaseRunning},
		{PhaseStopped, PhaseStarting},
		{PhaseStopped, PhaseStopping},
		{PhaseStopped, PhaseFailed},
		{PhaseFailed, PhaseRunning},
		{PhaseFailed, PhaseStarting},
		{PhaseFailed, PhaseStopping},
		{PhaseFailed, PhaseStopped},
		{PhaseRunning, PhaseStarting},
		{PhaseStopping, PhaseRunning},
		{PhaseStopping, PhaseStarting},
		{Phase("unknown"), PhaseRunning},
	}

	for _, pair := range invalidPairs {
		if IsValidTransition(pair[0], pair[1]) {
			t.Errorf("expected transition %s -> %s to be INVALID", pair[0], pair[1])
		}
	}

	// Terminal check
	if !PhaseStopped.IsTerminal() {
		t.Errorf("expected PhaseStopped to be terminal")
	}
	if !PhaseFailed.IsTerminal() {
		t.Errorf("expected PhaseFailed to be terminal")
	}
	if PhaseRunning.IsTerminal() {
		t.Errorf("expected PhaseRunning to not be terminal")
	}
	if PhaseStarting.IsTerminal() {
		t.Errorf("expected PhaseStarting to not be terminal")
	}
	if PhaseStopping.IsTerminal() {
		t.Errorf("expected PhaseStopping to not be terminal")
	}
}

func TestControllerLifecycle_NormalStartup(t *testing.T) {
	var seq []string
	var mu sync.Mutex
	record := func(action string) {
		mu.Lock()
		defer mu.Unlock()
		seq = append(seq, action)
	}

	healthSrv := &mockHealthServer{
		listenFunc: func() error {
			record("health.listen")
			return nil
		},
	}
	rt := &mockRuntimeController{
		startFunc: func(ctx context.Context) error {
			record("runtime.start")
			return nil
		},
		alive: true,
		ready: true,
	}

	var logBuf bytes.Buffer
	logger := logging.NewJSONLogger(&logBuf, logging.LevelDebug, "controller-test")

	var exitCode *int
	cfg := LifecycleConfig{
		Runtime:      rt,
		HealthServer: healthSrv,
		Logger:       logger,
		ExitProcess: func(code int) {
			exitCode = &code
		},
	}

	lc, err := NewControllerLifecycle(cfg)
	if err != nil {
		t.Fatalf("failed to create lifecycle: %v", err)
	}

	if lc.Phase() != PhaseStarting {
		t.Errorf("initial phase should be PhaseStarting, got %s", lc.Phase())
	}

	statusBefore := lc.Status()
	if statusBefore.Healthy || statusBefore.Ready {
		t.Errorf("expected unhealthy and unready before start: %+v", statusBefore)
	}

	// Start
	if err := lc.Start(context.Background()); err != nil {
		t.Fatalf("start failed: %v", err)
	}

	mu.Lock()
	if len(seq) != 2 || seq[0] != "health.listen" || seq[1] != "runtime.start" {
		t.Errorf("unexpected startup sequence: %v", seq)
	}
	mu.Unlock()

	if lc.Phase() != PhaseRunning {
		t.Errorf("expected phase PhaseRunning, got %s", lc.Phase())
	}

	status := lc.Status()
	if !status.Healthy || !status.Ready || status.Phase != string(PhaseRunning) {
		t.Errorf("expected healthy and ready running status, got: %+v", status)
	}

	if exitCode != nil {
		t.Errorf("exitProcess should not be called on normal startup, got %d", *exitCode)
	}

	if !bytes.Contains(logBuf.Bytes(), []byte("controller-started")) {
		t.Errorf("expected log to contain controller-started: %s", logBuf.String())
	}
}

func TestControllerLifecycle_StartupFailure_Health(t *testing.T) {
	healthErr := errors.New("cannot bind port 8080")
	var cleanupActions []string
	var mu sync.Mutex
	record := func(action string) {
		mu.Lock()
		defer mu.Unlock()
		cleanupActions = append(cleanupActions, action)
	}

	healthSrv := &mockHealthServer{
		listenFunc: func() error {
			return healthErr
		},
		closeFunc: func() error {
			record("health.close")
			return nil
		},
	}
	rt := &mockRuntimeController{
		stopAcceptingWorkFunc: func(ctx context.Context) error {
			record("runtime.stopAcceptingWork")
			return nil
		},
		closeFunc: func(ctx context.Context) error {
			record("runtime.close")
			return nil
		},
	}

	var logBuf bytes.Buffer
	logger := logging.NewJSONLogger(&logBuf, logging.LevelDebug, "controller-test")

	var recordedExitCode *int
	cfg := LifecycleConfig{
		Runtime:      rt,
		HealthServer: healthSrv,
		Logger:       logger,
		ExitProcess: func(code int) {
			recordedExitCode = &code
		},
	}

	lc, err := NewControllerLifecycle(cfg)
	if err != nil {
		t.Fatalf("failed to create lifecycle: %v", err)
	}

	err = lc.Start(context.Background())
	if !errors.Is(err, healthErr) {
		t.Fatalf("expected healthErr, got %v", err)
	}

	if lc.Phase() != PhaseFailed {
		t.Errorf("expected phase PhaseFailed, got %s", lc.Phase())
	}

	mu.Lock()
	if len(cleanupActions) != 3 {
		t.Errorf("expected 3 cleanup actions, got %v", cleanupActions)
	}
	mu.Unlock()

	if recordedExitCode == nil || *recordedExitCode != 1 {
		t.Errorf("expected exit code 1, got %v", recordedExitCode)
	}

	if !bytes.Contains(logBuf.Bytes(), []byte("controller-startup-failed")) {
		t.Errorf("expected log to contain controller-startup-failed: %s", logBuf.String())
	}
}

func TestControllerLifecycle_StartupFailure_Runtime(t *testing.T) {
	runtimeErr := errors.New("failed to connect to cluster")
	var cleanupActions []string
	var mu sync.Mutex
	record := func(action string) {
		mu.Lock()
		defer mu.Unlock()
		cleanupActions = append(cleanupActions, action)
	}

	healthSrv := &mockHealthServer{
		closeFunc: func() error {
			record("health.close")
			return nil
		},
	}
	rt := &mockRuntimeController{
		startFunc: func(ctx context.Context) error {
			return runtimeErr
		},
		stopAcceptingWorkFunc: func(ctx context.Context) error {
			record("runtime.stopAcceptingWork")
			return nil
		},
		closeFunc: func(ctx context.Context) error {
			record("runtime.close")
			return nil
		},
	}

	var logBuf bytes.Buffer
	logger := logging.NewJSONLogger(&logBuf, logging.LevelDebug, "controller-test")

	var recordedExitCode *int
	cfg := LifecycleConfig{
		Runtime:      rt,
		HealthServer: healthSrv,
		Logger:       logger,
		ExitProcess: func(code int) {
			recordedExitCode = &code
		},
	}

	lc, err := NewControllerLifecycle(cfg)
	if err != nil {
		t.Fatalf("failed to create lifecycle: %v", err)
	}

	err = lc.Start(context.Background())
	if !errors.Is(err, runtimeErr) {
		t.Fatalf("expected runtimeErr, got %v", err)
	}

	if lc.Phase() != PhaseFailed {
		t.Errorf("expected phase PhaseFailed, got %s", lc.Phase())
	}

	mu.Lock()
	if len(cleanupActions) != 3 {
		t.Errorf("expected 3 cleanup actions, got %v", cleanupActions)
	}
	mu.Unlock()

	if recordedExitCode == nil || *recordedExitCode != 1 {
		t.Errorf("expected exit code 1, got %v", recordedExitCode)
	}
}

func TestControllerLifecycle_ShutdownSequence(t *testing.T) {
	var seq []string
	var mu sync.Mutex
	record := func(action string) {
		mu.Lock()
		defer mu.Unlock()
		seq = append(seq, action)
	}

	healthSrv := &mockHealthServer{
		closeFunc: func() error {
			record("health.close")
			return nil
		},
	}
	rt := &mockRuntimeController{
		stopAcceptingWorkFunc: func(ctx context.Context) error {
			record("runtime.stopAcceptingWork")
			return nil
		},
		waitForIdleFunc: func(ctx context.Context, timeout time.Duration) bool {
			record("runtime.waitForIdle")
			return true
		},
		closeFunc: func(ctx context.Context) error {
			record("runtime.close")
			return nil
		},
		alive: true,
		ready: true,
	}

	var logBuf bytes.Buffer
	logger := logging.NewJSONLogger(&logBuf, logging.LevelDebug, "controller-test")

	var recordedExitCode *int
	cfg := LifecycleConfig{
		Runtime:         rt,
		HealthServer:    healthSrv,
		Logger:          logger,
		ShutdownTimeout: 50 * time.Millisecond,
		ExitProcess: func(code int) {
			recordedExitCode = &code
		},
	}

	lc, err := NewControllerLifecycle(cfg)
	if err != nil {
		t.Fatalf("failed to create lifecycle: %v", err)
	}

	if err := lc.Start(context.Background()); err != nil {
		t.Fatalf("start failed: %v", err)
	}

	// Trigger shutdown
	exitCode, err := lc.Shutdown(context.Background(), "SIGTERM")
	if err != nil {
		t.Fatalf("shutdown returned error: %v", err)
	}
	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", exitCode)
	}

	mu.Lock()
	expectedSeq := []string{
		"runtime.stopAcceptingWork",
		"runtime.waitForIdle",
		"health.close",
		"runtime.close",
	}
	if len(seq) != len(expectedSeq) {
		t.Fatalf("unexpected shutdown sequence length: %v, expected: %v", seq, expectedSeq)
	}
	for i := range expectedSeq {
		if seq[i] != expectedSeq[i] {
			t.Errorf("step %d mismatch: got %s, expected %s", i, seq[i], expectedSeq[i])
		}
	}
	mu.Unlock()

	if lc.Phase() != PhaseStopped {
		t.Errorf("expected phase PhaseStopped, got %s", lc.Phase())
	}

	if recordedExitCode == nil || *recordedExitCode != 0 {
		t.Errorf("expected recorded exit code 0, got %v", recordedExitCode)
	}

	logStr := logBuf.String()
	if !bytes.Contains([]byte(logStr), []byte("controller-stopping")) {
		t.Errorf("expected log to contain controller-stopping")
	}
	if !bytes.Contains([]byte(logStr), []byte("controller-stopped")) {
		t.Errorf("expected log to contain controller-stopped")
	}
}

func TestControllerLifecycle_ShutdownTimeout(t *testing.T) {
	healthSrv := &mockHealthServer{}
	rt := &mockRuntimeController{
		waitForIdleFunc: func(ctx context.Context, timeout time.Duration) bool {
			return false // simulates timeout
		},
	}

	var logBuf bytes.Buffer
	logger := logging.NewJSONLogger(&logBuf, logging.LevelDebug, "controller-test")

	cfg := LifecycleConfig{
		Runtime:         rt,
		HealthServer:    healthSrv,
		Logger:          logger,
		ShutdownTimeout: 25 * time.Millisecond,
		ExitProcess:     func(code int) {},
	}

	lc, err := NewControllerLifecycle(cfg)
	if err != nil {
		t.Fatalf("failed: %v", err)
	}

	_ = lc.Start(context.Background())
	exitCode, err := lc.Shutdown(context.Background(), "SIGINT")
	if err != nil {
		t.Fatalf("shutdown error: %v", err)
	}
	if exitCode != 0 {
		t.Errorf("expected exit code 0 despite timeout, got %d", exitCode)
	}

	if !bytes.Contains(logBuf.Bytes(), []byte("controller-shutdown-timeout")) {
		t.Errorf("expected log to contain controller-shutdown-timeout, got: %s", logBuf.String())
	}
}

func TestControllerLifecycle_ShutdownFailure(t *testing.T) {
	closeErr := errors.New("health server close error")
	healthSrv := &mockHealthServer{
		closeFunc: func() error {
			return closeErr
		},
	}
	rt := &mockRuntimeController{}

	var logBuf bytes.Buffer
	logger := logging.NewJSONLogger(&logBuf, logging.LevelDebug, "controller-test")

	var recordedExitCode *int
	cfg := LifecycleConfig{
		Runtime:      rt,
		HealthServer: healthSrv,
		Logger:       logger,
		ExitProcess: func(code int) {
			recordedExitCode = &code
		},
	}

	lc, err := NewControllerLifecycle(cfg)
	if err != nil {
		t.Fatalf("failed: %v", err)
	}

	_ = lc.Start(context.Background())
	exitCode, err := lc.Shutdown(context.Background(), "manual")
	if !errors.Is(err, closeErr) {
		t.Fatalf("expected closeErr, got %v", err)
	}
	if exitCode != 1 {
		t.Errorf("expected exit code 1 on failure, got %d", exitCode)
	}

	if lc.Phase() != PhaseFailed {
		t.Errorf("expected phase PhaseFailed, got %s", lc.Phase())
	}

	if recordedExitCode == nil || *recordedExitCode != 1 {
		t.Errorf("expected exitProcess(1), got %v", recordedExitCode)
	}

	if !bytes.Contains(logBuf.Bytes(), []byte("controller-shutdown-failed")) {
		t.Errorf("expected log to contain controller-shutdown-failed")
	}
}

func TestControllerLifecycle_ShutdownIdempotency(t *testing.T) {
	healthSrv := &mockHealthServer{}
	rt := &mockRuntimeController{}

	var logBuf bytes.Buffer
	logger := logging.NewJSONLogger(&logBuf, logging.LevelDebug, "controller-test")

	var exitCalls int32
	cfg := LifecycleConfig{
		Runtime:      rt,
		HealthServer: healthSrv,
		Logger:       logger,
		ExitProcess: func(code int) {
			atomic.AddInt32(&exitCalls, 1)
		},
	}

	lc, err := NewControllerLifecycle(cfg)
	if err != nil {
		t.Fatalf("failed: %v", err)
	}

	_ = lc.Start(context.Background())

	// Call shutdown twice sequentially
	code1, err1 := lc.Shutdown(context.Background(), "SIGTERM")
	code2, err2 := lc.Shutdown(context.Background(), "SIGINT")

	if code1 != 0 || code2 != 0 {
		t.Errorf("expected 0, got %d, %d", code1, code2)
	}
	if err1 != nil || err2 != nil {
		t.Errorf("unexpected error: %v, %v", err1, err2)
	}

	if atomic.LoadInt32(&exitCalls) != 1 {
		t.Errorf("expected exitProcess called exactly once, got %d", exitCalls)
	}

	if healthSrv.closeCalls != 1 {
		t.Errorf("expected health close called once, got %d", healthSrv.closeCalls)
	}
	if rt.closeCalls != 1 {
		t.Errorf("expected runtime close called once, got %d", rt.closeCalls)
	}
}

func TestControllerLifecycle_ShutdownConcurrent(t *testing.T) {
	healthSrv := &mockHealthServer{}
	rt := &mockRuntimeController{}

	var logBuf bytes.Buffer
	logger := logging.NewJSONLogger(&logBuf, logging.LevelDebug, "controller-test")

	var exitCalls int32
	cfg := LifecycleConfig{
		Runtime:      rt,
		HealthServer: healthSrv,
		Logger:       logger,
		ExitProcess: func(code int) {
			atomic.AddInt32(&exitCalls, 1)
		},
	}

	lc, err := NewControllerLifecycle(cfg)
	if err != nil {
		t.Fatalf("failed: %v", err)
	}

	_ = lc.Start(context.Background())

	goroutines := 20
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			code, err := lc.Shutdown(context.Background(), "SIGTERM")
			if code != 0 || err != nil {
				t.Errorf("concurrent shutdown failed: code=%d, err=%v", code, err)
			}
		}()
	}

	wg.Wait()

	if atomic.LoadInt32(&exitCalls) != 1 {
		t.Errorf("expected exitProcess called once, got %d", exitCalls)
	}
	if healthSrv.closeCalls != 1 {
		t.Errorf("expected healthServer.Close called once, got %d", healthSrv.closeCalls)
	}
	if rt.closeCalls != 1 {
		t.Errorf("expected runtime.Close called once, got %d", rt.closeCalls)
	}
}

func TestControllerLifecycle_ShutdownWhileStartInFlight(t *testing.T) {
	runtimeStartBlock := make(chan struct{})
	rt := &mockRuntimeController{
		startFunc: func(ctx context.Context) error {
			<-runtimeStartBlock
			return nil
		},
	}
	healthSrv := &mockHealthServer{}

	var logBuf bytes.Buffer
	logger := logging.NewJSONLogger(&logBuf, logging.LevelDebug, "controller-test")

	cfg := LifecycleConfig{
		Runtime:      rt,
		HealthServer: healthSrv,
		Logger:       logger,
		ExitProcess:  func(code int) {},
	}

	lc, err := NewControllerLifecycle(cfg)
	if err != nil {
		t.Fatalf("failed: %v", err)
	}

	startDone := make(chan error)
	go func() {
		startDone <- lc.Start(context.Background())
	}()

	// Give Start time to block
	time.Sleep(10 * time.Millisecond)

	shutdownDone := make(chan struct{})
	go func() {
		code, err := lc.Shutdown(context.Background(), "SIGTERM")
		if code != 0 || err != nil {
			t.Errorf("shutdown failed: code=%d err=%v", code, err)
		}
		close(shutdownDone)
	}()

	// Unblock start
	close(runtimeStartBlock)

	if err := <-startDone; err != nil {
		t.Errorf("start failed: %v", err)
	}

	select {
	case <-shutdownDone:
	case <-time.After(2 * time.Second):
		t.Fatalf("shutdown timed out")
	}

	if lc.Phase() != PhaseStopped {
		t.Errorf("expected phase PhaseStopped, got %s", lc.Phase())
	}
}

func TestControllerLifecycle_SignalHandling(t *testing.T) {
	healthSrv := &mockHealthServer{}
	rt := &mockRuntimeController{}

	var logBuf bytes.Buffer
	logger := logging.NewJSONLogger(&logBuf, logging.LevelDebug, "controller-test")

	cfg := LifecycleConfig{
		Runtime:      rt,
		HealthServer: healthSrv,
		Logger:       logger,
		ExitProcess:  func(code int) {},
	}

	lc, err := NewControllerLifecycle(cfg)
	if err != nil {
		t.Fatalf("failed: %v", err)
	}

	// Install signal handler for a test signal
	cleanup := lc.InstallSignalHandlers(syscall.SIGUSR1)
	defer cleanup()

	// Sending SIGUSR1 to our own process
	p, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatalf("failed to find process: %v", err)
	}
	_ = p.Signal(syscall.SIGUSR1)

	// Wait for shutdown to be requested
	deadline := time.Now().Add(1 * time.Second)
	for !lc.IsShutdownRequested() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}

	if !lc.IsShutdownRequested() {
		t.Errorf("expected shutdown to be requested via signal")
	}
}
