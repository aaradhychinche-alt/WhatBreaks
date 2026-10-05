package scheduler

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/aaradhychinche-alt/WhatBreaks/internal/logging"
)

// taskState manages the in-memory execution state, overlap prevention lock,
// and configuration for a registered worker task.
type taskState struct {
	mu         sync.Mutex
	running    bool
	job        Job
	parsedCron *ParsedCron
}

func newTaskState(job Job) (*taskState, error) {
	if job == nil {
		return nil, fmt.Errorf("job cannot be nil")
	}
	name := job.Name()
	if name == "" {
		return nil, fmt.Errorf("job name cannot be empty")
	}

	cfg := job.Config()
	var parsedCron *ParsedCron

	switch cfg.Mode {
	case ModeInterval:
		if cfg.Interval <= 0 {
			return nil, fmt.Errorf("worker %q interval must be a positive duration", name)
		}
	case ModeCron:
		if cfg.CronExpression == "" {
			return nil, fmt.Errorf("worker %q cron expression cannot be empty", name)
		}
		var err error
		parsedCron, err = ParseCronExpression(cfg.CronExpression, name+" cron")
		if err != nil {
			return nil, err
		}
		if err := ValidateCronFeasibility(parsedCron); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unknown schedule mode: %v", cfg.Mode)
	}

	return &taskState{
		job:        job,
		parsedCron: parsedCron,
	}, nil
}

// IsRunning reports whether the task is currently executing.
func (t *taskState) IsRunning() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.running
}

// RunOnce executes the job once with overlap prevention, logging, duration tracking,
// and metric recording, faithfully replicating runner.js runWorkerOnce.
func (t *taskState) RunOnce(
	ctx context.Context,
	trigger string,
	logger logging.Logger,
	metrics MetricsRecorder,
	exitOnError bool,
) (RunResult, error) {
	t.mu.Lock()
	if t.running {
		t.mu.Unlock()
		if logger != nil {
			logger.Warn("worker-runner-skip-overlap",
				"worker", t.job.Name(),
				"trigger", trigger,
			)
		}
		if metrics != nil {
			metrics.RecordJobOverlap(t.job.Name(), trigger)
		}
		return RunResult{
			Worker:  t.job.Name(),
			Status:  StatusSkipped,
			Trigger: trigger,
		}, nil
	}

	t.running = true
	t.mu.Unlock()

	startedAt := time.Now()
	if logger != nil {
		logger.Info("worker-runner-job-start",
			"worker", t.job.Name(),
			"trigger", trigger,
		)
	}
	if metrics != nil {
		metrics.RecordJobStart(t.job.Name(), trigger)
	}

	var runErr error
	defer func() {
		t.mu.Lock()
		t.running = false
		t.mu.Unlock()
	}()

	runErr = t.job.Run(ctx)
	duration := time.Since(startedAt)
	durationMs := duration.Milliseconds()

	if runErr != nil {
		if logger != nil {
			logger.Error("worker-runner-job-failure",
				"worker", t.job.Name(),
				"trigger", trigger,
				"durationMs", durationMs,
				"error", runErr.Error(),
			)
		}
		if metrics != nil {
			metrics.RecordJobFailure(t.job.Name(), trigger, duration, runErr)
		}

		result := RunResult{
			Worker:   t.job.Name(),
			Status:   StatusFailed,
			Trigger:  trigger,
			Duration: duration,
			Error:    runErr,
		}

		if exitOnError {
			return result, runErr
		}
		return result, nil
	}

	if logger != nil {
		logger.Info("worker-runner-job-finish",
			"worker", t.job.Name(),
			"trigger", trigger,
			"durationMs", durationMs,
		)
	}
	if metrics != nil {
		metrics.RecordJobFinish(t.job.Name(), trigger, duration)
	}

	return RunResult{
		Worker:   t.job.Name(),
		Status:   StatusSuccess,
		Trigger:  trigger,
		Duration: duration,
	}, nil
}
