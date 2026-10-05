package scheduler

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aaradhychinche-alt/WhatBreaks/internal/database"
	"github.com/aaradhychinche-alt/WhatBreaks/internal/logging"
)

const (
	// DefaultShutdownTimeoutMs matches SHUTDOWN_TIMEOUT_MS in runner.js (30,000ms = 30s).
	DefaultShutdownTimeout = 30 * time.Second
)

// Scheduler defines the public contract for the worker task scheduler.
type Scheduler interface {
	Register(job Job) error
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
	RunOnce(ctx context.Context, jobNames ...string) ([]RunResult, error)
}

// Option configures Scheduler options.
type Option func(*schedulerConfig)

type schedulerConfig struct {
	logger          logging.Logger
	metrics         MetricsRecorder
	shutdownTimeout time.Duration
	exitOnError     bool
	dbCloser        func() error
	now             func() time.Time
}

// WithLogger sets the structured logger for the scheduler.
func WithLogger(logger logging.Logger) Option {
	return func(c *schedulerConfig) {
		if logger != nil {
			c.logger = logger
		}
	}
}

// WithMetrics sets the metrics recorder for worker execution telemetry.
func WithMetrics(metrics MetricsRecorder) Option {
	return func(c *schedulerConfig) {
		if metrics != nil {
			c.metrics = metrics
		}
	}
}

// WithShutdownTimeout overrides the default 30-second shutdown timeout.
func WithShutdownTimeout(timeout time.Duration) Option {
	return func(c *schedulerConfig) {
		if timeout > 0 {
			c.shutdownTimeout = timeout
		}
	}
}

// WithExitOnError configures the scheduler to stop when a worker returns an error.
func WithExitOnError(exit bool) Option {
	return func(c *schedulerConfig) {
		c.exitOnError = exit
	}
}

// WithDatabase binds an existing database instance to be closed upon scheduler shutdown.
func WithDatabase(db *database.Database) Option {
	return func(c *schedulerConfig) {
		if db != nil {
			c.dbCloser = db.Close
		}
	}
}

// WithDBClose sets a custom database close function.
func WithDBClose(fn func() error) Option {
	return func(c *schedulerConfig) {
		c.dbCloser = fn
	}
}

// WithNow allows overriding time.Now for deterministic testing.
func WithNow(fn func() time.Time) Option {
	return func(c *schedulerConfig) {
		if fn != nil {
			c.now = fn
		}
	}
}

// TaskScheduler coordinates background worker scheduling, cron evaluation,
// interval tickers, overlap prevention, active work tracking, and graceful shutdown.
type TaskScheduler struct {
	mu              sync.RWMutex
	jobs            map[string]*taskState
	jobOrder        []string
	logger          logging.Logger
	metrics         MetricsRecorder
	shutdownTimeout time.Duration
	exitOnError     bool
	dbCloser        func() error
	now             func() time.Time

	ctx       context.Context
	cancel    context.CancelFunc
	stopCh    chan struct{}
	stoppedCh chan struct{}
	stopOnce  sync.Once
	stopErr   error

	started      bool
	stopping     atomic.Bool
	stopped      atomic.Bool
	activeRunsWG sync.WaitGroup
	activeCount  atomic.Int64
}

// New creates an unstarted TaskScheduler with provided options.
func New(opts ...Option) *TaskScheduler {
	cfg := schedulerConfig{
		logger:          logging.NewJSONLogger(nil, logging.LevelInfo, "worker-runner"),
		metrics:         NoopMetricsRecorder{},
		shutdownTimeout: DefaultShutdownTimeout,
		exitOnError:     false,
		now:             time.Now,
	}

	for _, opt := range opts {
		opt(&cfg)
	}

	ctx, cancel := context.WithCancel(context.Background())

	return &TaskScheduler{
		jobs:            make(map[string]*taskState),
		jobOrder:        make([]string, 0),
		logger:          cfg.logger,
		metrics:         cfg.metrics,
		shutdownTimeout: cfg.shutdownTimeout,
		exitOnError:     cfg.exitOnError,
		dbCloser:        cfg.dbCloser,
		now:             cfg.now,
		ctx:             ctx,
		cancel:          cancel,
		stopCh:          make(chan struct{}),
		stoppedCh:       make(chan struct{}),
	}
}

// Register registers a job with the scheduler. Validates configuration and rejects duplicates.
func (s *TaskScheduler) Register(job Job) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.started {
		return errors.New("cannot register jobs after scheduler has started")
	}
	if s.stopping.Load() || s.stopped.Load() {
		return errors.New("cannot register jobs on stopped scheduler")
	}

	state, err := newTaskState(job)
	if err != nil {
		return err
	}

	name := job.Name()
	if _, exists := s.jobs[name]; exists {
		return fmt.Errorf("worker %q is already registered", name)
	}

	s.jobs[name] = state
	s.jobOrder = append(s.jobOrder, name)
	return nil
}

// GetJob returns the taskState for a named job, or nil if not found.
func (s *TaskScheduler) GetJob(name string) Job {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if st, ok := s.jobs[name]; ok {
		return st.job
	}
	return nil
}

// RunOnce executes the specified workers (or all if none specified) synchronously once,
// faithfully implementing runner.js runWorkersOnce.
func (s *TaskScheduler) RunOnce(ctx context.Context, jobNames ...string) ([]RunResult, error) {
	s.mu.RLock()
	names := jobNames
	if len(names) == 0 {
		names = append([]string(nil), s.jobOrder...)
	}

	tasksToRun := make([]*taskState, 0, len(names))
	for _, name := range names {
		state, ok := s.jobs[name]
		if !ok {
			s.mu.RUnlock()
			return nil, fmt.Errorf("unknown worker %q", name)
		}
		tasksToRun = append(tasksToRun, state)
	}
	s.mu.RUnlock()

	results := make([]RunResult, 0, len(tasksToRun))
	for _, task := range tasksToRun {
		select {
		case <-ctx.Done():
			return results, ctx.Err()
		default:
		}

		result, err := task.RunOnce(ctx, "once", s.logger, s.metrics, s.exitOnError)
		results = append(results, result)
		if err != nil && s.exitOnError {
			return results, err
		}
	}

	return results, nil
}

// Start begins scheduling and background execution for all registered workers.
func (s *TaskScheduler) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return errors.New("scheduler already started")
	}
	if s.stopping.Load() || s.stopped.Load() {
		s.mu.Unlock()
		return errors.New("cannot start stopped scheduler")
	}
	s.started = true

	// Take snapshot of task states to schedule
	states := make([]*taskState, 0, len(s.jobOrder))
	for _, name := range s.jobOrder {
		states = append(states, s.jobs[name])
	}
	s.mu.Unlock()

	for _, task := range states {
		cfg := task.job.Config()
		s.logger.Info("worker-runner-worker-started",
			"worker", task.job.Name(),
			"scheduleMode", cfg.Mode.String(),
			"cronExpression", cfg.CronExpression,
			"intervalMs", cfg.Interval.Milliseconds(),
			"runOnStart", cfg.RunOnStart,
			"exitOnError", s.exitOnError,
		)

		if cfg.Mode == ModeCron {
			s.startCronTask(task, cfg)
		} else {
			s.startIntervalTask(task, cfg)
		}
	}

	return nil
}

func (s *TaskScheduler) invoke(task *taskState, trigger string, cfg ScheduleConfig, onSettled func()) {
	if s.stopping.Load() || s.stopped.Load() {
		return
	}

	s.activeRunsWG.Add(1)
	s.activeCount.Add(1)

	go func() {
		defer s.activeRunsWG.Done()
		defer s.activeCount.Add(-1)
		defer func() {
			if !s.stopping.Load() && onSettled != nil {
				onSettled()
			}
		}()

		result, err := task.RunOnce(s.ctx, trigger, s.logger, s.metrics, s.exitOnError)
		if err != nil && s.exitOnError {
			s.logger.Error("worker-runner-exit-on-error",
				"worker", task.job.Name(),
				"error", err.Error(),
			)
			go func() {
				_ = s.Stop(context.Background())
			}()
			return
		}

		if !s.stopping.Load() && result.Status != StatusSkipped && cfg.Mode == ModeInterval {
			s.logNextIntervalRun(task.job.Name(), cfg)
		}
	}()
}

func (s *TaskScheduler) logNextIntervalRun(workerName string, cfg ScheduleConfig) {
	nextRunAt := s.now().Add(cfg.Interval)
	s.logger.Info("worker-runner-next-run",
		"worker", workerName,
		"scheduleMode", "interval",
		"nextRunAt", nextRunAt.UTC().Format(time.RFC3339Nano),
		"intervalMs", cfg.Interval.Milliseconds(),
	)
}

func (s *TaskScheduler) startIntervalTask(task *taskState, cfg ScheduleConfig) {
	if cfg.RunOnStart {
		s.invoke(task, "startup", cfg, nil)
	} else {
		s.logNextIntervalRun(task.job.Name(), cfg)
	}

	go func() {
		ticker := time.NewTicker(cfg.Interval)
		defer ticker.Stop()

		for {
			select {
			case <-s.stopCh:
				return
			case <-ticker.C:
				if s.stopping.Load() {
					return
				}
				s.invoke(task, "interval", cfg, nil)
			}
		}
	}()
}

func (s *TaskScheduler) startCronTask(task *taskState, cfg ScheduleConfig) {
	if cfg.RunOnStart {
		s.invoke(task, "startup", cfg, func() {
			s.scheduleNextCronRun(task, cfg)
		})
	} else {
		s.scheduleNextCronRun(task, cfg)
	}
}

func (s *TaskScheduler) scheduleNextCronRun(task *taskState, cfg ScheduleConfig) {
	if s.stopping.Load() || s.stopped.Load() {
		return
	}

	currentTime := s.now()
	nextRunAt, err := GetNextCronRunAt(task.parsedCron, currentTime)
	if err != nil {
		s.logger.Error("worker-runner-cron-error",
			"worker", task.job.Name(),
			"error", err.Error(),
		)
		return
	}

	delay := nextRunAt.Sub(currentTime)
	if delay < 0 {
		delay = 0
	}

	s.logger.Info("worker-runner-next-run",
		"worker", task.job.Name(),
		"scheduleMode", "cron",
		"cronExpression", cfg.CronExpression,
		"nextRunAt", nextRunAt.UTC().Format(time.RFC3339Nano),
		"delayMs", delay.Milliseconds(),
	)

	go func() {
		timer := time.NewTimer(delay)
		defer timer.Stop()

		select {
		case <-s.stopCh:
			return
		case <-timer.C:
			if s.stopping.Load() {
				return
			}
			s.invoke(task, "cron", cfg, func() {
				s.scheduleNextCronRun(task, cfg)
			})
		}
	}()
}

// Stop initiates graceful shutdown of the scheduler, matching runner.js stop.
// It stops accepting new work, cancels active timers, waits up to shutdownTimeout
// (default 30s) for in-flight tasks to drain, and closes the database pool.
// It is concurrency-safe and idempotent.
func (s *TaskScheduler) Stop(ctx context.Context) error {
	s.stopOnce.Do(func() {
		s.stopping.Store(true)
		s.logger.Info("worker-runner-stopping", "signal", "manual")

		// Signal timers and loops to terminate immediately
		close(s.stopCh)
		s.cancel()

		// Wait for active in-flight worker executions
		drainDone := make(chan struct{})
		go func() {
			s.activeRunsWG.Wait()
			close(drainDone)
		}()

		timeout := s.shutdownTimeout
		timer := time.NewTimer(timeout)
		defer timer.Stop()

		var timedOut bool
		select {
		case <-drainDone:
			// Drained cleanly
		case <-timer.C:
			timedOut = true
		case <-ctx.Done():
			timedOut = true
		}

		if timedOut {
			active := s.activeCount.Load()
			s.logger.Error("worker-runner-shutdown-timeout",
				"activeRuns", active,
				"timeoutMs", timeout.Milliseconds(),
			)
		}

		// Close database connection pool if registered
		if s.dbCloser != nil {
			if err := s.dbCloser(); err != nil {
				s.logger.Error("worker-runner-pool-close-failure",
					"error", err.Error(),
				)
				s.stopErr = err
			}
		}

		s.stopped.Store(true)
		close(s.stoppedCh)
	})

	select {
	case <-s.stoppedCh:
		return s.stopErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

// StopWithSignal triggers graceful shutdown while recording the triggering signal (e.g. "SIGINT", "SIGTERM").
func (s *TaskScheduler) StopWithSignal(ctx context.Context, signal string) error {
	s.mu.Lock()
	if !s.stopping.Load() {
		s.logger.Info("worker-runner-stopping", "signal", signal)
	}
	s.mu.Unlock()
	return s.Stop(ctx)
}

// ActiveRuns returns the count of currently executing tasks.
func (s *TaskScheduler) ActiveRuns() int {
	return int(s.activeCount.Load())
}

// IsStopping reports whether the scheduler has initiated shutdown.
func (s *TaskScheduler) IsStopping() bool {
	return s.stopping.Load()
}

// IsStopped reports whether the scheduler has completely shut down.
func (s *TaskScheduler) IsStopped() bool {
	return s.stopped.Load()
}
