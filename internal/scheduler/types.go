package scheduler

import (
	"context"
	"time"
)

// ScheduleMode denotes whether a job executes on fixed intervals or calendar cron.
type ScheduleMode int

const (
	// ModeInterval executes the job repeatedly with a fixed duration between runs.
	ModeInterval ScheduleMode = iota

	// ModeCron executes the job according to a 5-field cron expression.
	ModeCron
)

func (m ScheduleMode) String() string {
	switch m {
	case ModeInterval:
		return "interval"
	case ModeCron:
		return "cron"
	default:
		return "unknown"
	}
}

// ScheduleConfig defines scheduling parameters for a background job.
type ScheduleConfig struct {
	Mode           ScheduleMode
	Interval       time.Duration
	CronExpression string
	RunOnStart     bool
}

// Job encapsulates a named unit of asynchronous or background work.
type Job interface {
	Name() string
	Run(ctx context.Context) error
	Config() ScheduleConfig
}

// SimpleJob is a convenience struct implementing Job.
type SimpleJob struct {
	JobName   string
	JobConfig ScheduleConfig
	RunFn     func(ctx context.Context) error
}

func (j *SimpleJob) Name() string {
	return j.JobName
}

func (j *SimpleJob) Config() ScheduleConfig {
	return j.JobConfig
}

func (j *SimpleJob) Run(ctx context.Context) error {
	if j.RunFn != nil {
		return j.RunFn(ctx)
	}
	return nil
}

// RunStatus defines the outcome of a worker run.
type RunStatus string

const (
	StatusSuccess RunStatus = "success"
	StatusFailed  RunStatus = "failed"
	StatusSkipped RunStatus = "skipped"
)

// RunResult describes the execution outcome of a job.
type RunResult struct {
	Worker   string
	Status   RunStatus
	Trigger  string
	Duration time.Duration
	Error    error
}

// MetricsRecorder provides telemetry hooks for worker runtime events.
type MetricsRecorder interface {
	RecordJobStart(worker, trigger string)
	RecordJobFinish(worker, trigger string, duration time.Duration)
	RecordJobFailure(worker, trigger string, duration time.Duration, err error)
	RecordJobOverlap(worker, trigger string)
	RecordHeartbeat(component string, up bool)
}

// NoopMetricsRecorder is a default empty metrics recorder.
type NoopMetricsRecorder struct{}

func (NoopMetricsRecorder) RecordJobStart(string, string)                         {}
func (NoopMetricsRecorder) RecordJobFinish(string, string, time.Duration)         {}
func (NoopMetricsRecorder) RecordJobFailure(string, string, time.Duration, error) {}
func (NoopMetricsRecorder) RecordJobOverlap(string, string)                       {}
func (NoopMetricsRecorder) RecordHeartbeat(string, bool)                          {}
