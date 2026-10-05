package lifecycle

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/aaradhychinche-alt/WhatBreaks/internal/health"
	"github.com/aaradhychinche-alt/WhatBreaks/internal/logging"
)

// HealthServer defines the contract required from a health probe server by the lifecycle manager.
type HealthServer interface {
	Listen() error
	Close() error
}

// RuntimeController defines the contract required from the controller runtime by the lifecycle manager.
type RuntimeController interface {
	Start(ctx context.Context) error
	StopAcceptingWork(ctx context.Context) error
	WaitForIdle(ctx context.Context, timeout time.Duration) bool
	Close(ctx context.Context) error
	IsAlive() bool
	IsReady() bool
}

// LifecycleConfig bundles dependencies and configuration for the controller lifecycle.
type LifecycleConfig struct {
	Runtime         RuntimeController
	HealthServer    HealthServer
	Logger          logging.Logger
	ShutdownTimeout time.Duration
	ExitProcess     func(code int)
}

// ControllerLifecycle coordinates startup, running state, signal handling, graceful shutdown,
// and process termination, faithfully migrating kubernetes/controller/lifecycle.js.
type ControllerLifecycle struct {
	runtime         RuntimeController
	healthServer    HealthServer
	logger          logging.Logger
	shutdownTimeout time.Duration
	exitProcess     func(code int)

	mu                   sync.Mutex
	phase                Phase
	startupStarted       bool
	startupDone          chan struct{}
	startupErr           error
	shutdownRequested    bool
	shutdownDone         chan struct{}
	shutdownExitCode     int
	shutdownErr          error
	exitCode             *int
	removeSignalHandlers func()
}

// NewControllerLifecycle creates a new ControllerLifecycle instance.
func NewControllerLifecycle(cfg LifecycleConfig) (*ControllerLifecycle, error) {
	if cfg.Runtime == nil || cfg.HealthServer == nil || cfg.Logger == nil {
		return nil, fmt.Errorf("%w: runtime, healthServer, and logger are required", ErrInvalidConfig)
	}

	timeout := cfg.ShutdownTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	exitFn := cfg.ExitProcess
	if exitFn == nil {
		exitFn = func(code int) {
			os.Exit(code)
		}
	}

	return &ControllerLifecycle{
		runtime:              cfg.Runtime,
		healthServer:         cfg.HealthServer,
		logger:               cfg.Logger,
		shutdownTimeout:      timeout,
		exitProcess:          exitFn,
		phase:                PhaseStarting,
		startupDone:          make(chan struct{}),
		shutdownDone:         make(chan struct{}),
		removeSignalHandlers: func() {},
	}, nil
}

// Phase returns the current lifecycle phase.
func (l *ControllerLifecycle) Phase() Phase {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.phase
}

// Status returns the observable health, readiness, and phase matching
// kubernetes/controller/lifecycle.js lines 35-42.
func (l *ControllerLifecycle) Status() health.ControllerStatus {
	l.mu.Lock()
	currentPhase := l.phase
	l.mu.Unlock()

	alive := l.runtime.IsAlive()
	healthy := (currentPhase == PhaseRunning) && alive
	ready := healthy && l.runtime.IsReady()

	return health.ControllerStatus{
		Healthy: healthy,
		Ready:   ready,
		Phase:   string(currentPhase),
	}
}

// Start begins controller initialization matching kubernetes/controller/lifecycle.js lines 44-74:
// 1. healthServer.Listen()
// 2. if shutdownRequested return
// 3. runtime.Start()
// 4. if shutdownRequested return
// 5. phase = PhaseRunning
// 6. log controller-started
// On error:
// 1. if shutdownRequested throw error
// 2. phase = PhaseFailed
// 3. concurrently cleanup (stopAcceptingWork, healthServer.close, runtime.close)
// 4. log controller-startup-failed
// 5. exitOnce(1)
func (l *ControllerLifecycle) Start(ctx context.Context) error {
	l.mu.Lock()
	if l.startupStarted {
		l.mu.Unlock()
		<-l.startupDone
		return l.startupErr
	}
	l.startupStarted = true
	l.mu.Unlock()

	defer close(l.startupDone)

	// 1. healthServer.Listen()
	if err := l.healthServer.Listen(); err != nil {
		l.startupErr = l.handleStartupFailure(ctx, err)
		return l.startupErr
	}

	l.mu.Lock()
	if l.shutdownRequested {
		l.mu.Unlock()
		return nil
	}
	l.mu.Unlock()

	// 2. runtime.Start()
	if err := l.runtime.Start(ctx); err != nil {
		l.startupErr = l.handleStartupFailure(ctx, err)
		return l.startupErr
	}

	l.mu.Lock()
	if l.shutdownRequested {
		l.mu.Unlock()
		return nil
	}
	l.phase = PhaseRunning
	l.mu.Unlock()

	l.logger.Info("controller-started")
	return nil
}

func (l *ControllerLifecycle) handleStartupFailure(ctx context.Context, err error) error {
	l.mu.Lock()
	if l.shutdownRequested {
		l.mu.Unlock()
		return err
	}
	l.phase = PhaseFailed
	l.mu.Unlock()

	// Concurrently settle cleanup tasks (Promise.allSettled)
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		_ = l.runtime.StopAcceptingWork(ctx)
	}()
	go func() {
		defer wg.Done()
		_ = l.healthServer.Close()
	}()
	go func() {
		defer wg.Done()
		_ = l.runtime.Close(ctx)
	}()
	wg.Wait()

	l.logger.Error("controller-startup-failed",
		"code", GetErrorCode(err, "CONTROLLER_STARTUP_FAILED"),
	)
	l.exitOnce(1)
	return err
}

// Shutdown initiates graceful controller teardown matching kubernetes/controller/lifecycle.js lines 76-125:
//  1. if shutdownPromise return shutdownPromise
//  2. shutdownRequested = true; phase = PhaseStopping
//  3. removeSignalHandlers()
//  4. log controller-stopping
//  5. await startupPromise (catch error)
//  6. sequential teardown:
//     a. runtime.stopAcceptingWork()
//     b. runtime.waitForIdle(timeout) (warn on timeout)
//     c. healthServer.close()
//     d. runtime.close()
//  7. exitCode = failure ? 1 : 0; phase = failure ? PhaseFailed : PhaseStopped
//  8. log controller-shutdown-failed or controller-stopped
//  9. exitOnce(exitCode)
func (l *ControllerLifecycle) Shutdown(ctx context.Context, signal string) (int, error) {
	if signal == "" {
		signal = "manual"
	}

	l.mu.Lock()
	if l.shutdownRequested {
		l.mu.Unlock()
		<-l.shutdownDone
		return l.shutdownExitCode, l.shutdownErr
	}

	l.shutdownRequested = true
	l.phase = PhaseStopping
	removeSignals := l.removeSignalHandlers
	l.mu.Unlock()

	defer close(l.shutdownDone)

	// Step 3: remove signal handlers
	if removeSignals != nil {
		removeSignals()
	}

	// Step 4: log controller-stopping
	l.logger.Info("controller-stopping", "signal", signal)

	var failure error

	// Step 5: await startup if it was started
	l.mu.Lock()
	started := l.startupStarted
	l.mu.Unlock()
	if started {
		<-l.startupDone
		if l.startupErr != nil {
			failure = l.startupErr
		}
	}

	// Step 6a: runtime.stopAcceptingWork()
	if err := l.runtime.StopAcceptingWork(ctx); err != nil && failure == nil {
		failure = err
	}

	// Step 6b: runtime.waitForIdle(shutdownTimeout)
	idle := l.runtime.WaitForIdle(ctx, l.shutdownTimeout)
	if !idle {
		l.logger.Warn("controller-shutdown-timeout",
			"shutdownTimeoutMs", l.shutdownTimeout.Milliseconds(),
		)
	}

	// Step 6c: healthServer.close()
	if err := l.healthServer.Close(); err != nil && failure == nil {
		failure = err
	}

	// Step 6d: runtime.close()
	if err := l.runtime.Close(ctx); err != nil && failure == nil {
		failure = err
	}

	// Step 7: determine exit code & phase
	exitCode := 0
	if failure != nil {
		exitCode = 1
	}

	l.mu.Lock()
	if failure != nil {
		l.phase = PhaseFailed
	} else {
		l.phase = PhaseStopped
	}
	l.shutdownExitCode = exitCode
	l.shutdownErr = failure
	l.mu.Unlock()

	// Step 8: log outcome
	if failure != nil {
		l.logger.Error("controller-shutdown-failed",
			"code", GetErrorCode(failure, "CONTROLLER_SHUTDOWN_FAILED"),
		)
	} else {
		l.logger.Info("controller-stopped", "signal", signal)
	}

	// Step 9: exitOnce(exitCode)
	l.exitOnce(exitCode)

	return exitCode, failure
}

func (l *ControllerLifecycle) exitOnce(code int) {
	l.mu.Lock()
	if l.exitCode != nil {
		l.mu.Unlock()
		return
	}
	codeCopy := code
	l.exitCode = &codeCopy
	fn := l.exitProcess
	l.mu.Unlock()

	if fn != nil {
		fn(code)
	}
}

// HasExited reports whether the process exit handler was triggered.
func (l *ControllerLifecycle) HasExited() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.exitCode != nil
}

// ExitCode returns the recorded exit code, if any.
func (l *ControllerLifecycle) ExitCode() (int, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.exitCode == nil {
		return 0, false
	}
	return *l.exitCode, true
}

// IsShutdownRequested reports whether shutdown has been initiated.
func (l *ControllerLifecycle) IsShutdownRequested() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.shutdownRequested
}

// InstallSignalHandlers installs listeners for SIGINT and SIGTERM matching
// kubernetes/controller/lifecycle.js lines 127-139, returning a cleanup function.
func (l *ControllerLifecycle) InstallSignalHandlers(signals ...os.Signal) func() {
	if len(signals) == 0 {
		signals = []os.Signal{syscall.SIGINT, syscall.SIGTERM}
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, signals...)

	done := make(chan struct{})
	var once sync.Once

	cleanup := func() {
		once.Do(func() {
			signal.Stop(sigCh)
			close(done)
		})
	}

	l.mu.Lock()
	l.removeSignalHandlers = cleanup
	l.mu.Unlock()

	go func() {
		select {
		case sig, ok := <-sigCh:
			if ok && sig != nil {
				signalName := sig.String()
				if sig == syscall.SIGINT {
					signalName = "SIGINT"
				} else if sig == syscall.SIGTERM {
					signalName = "SIGTERM"
				}
				_, _ = l.Shutdown(context.Background(), signalName)
			}
		case <-done:
			return
		}
	}()

	return cleanup
}
