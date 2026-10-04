package scheduler

import (
	"context"
	"time"
)

// ScheduleMode denotes whether a job executes on fixed millisecond intervals or calendar cron.
type ScheduleMode int

const (
	ModeInterval ScheduleMode = iota
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

// Scheduler coordinates the execution of background jobs, overlap prevention, and lifecycle shutdown.
type Scheduler interface {
	Register(job Job) error
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
}
