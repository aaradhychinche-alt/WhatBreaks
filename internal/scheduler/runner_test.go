package scheduler

import (
	"context"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestParseRunnerArgs(t *testing.T) {
	known := []string{"discovery", "sync"}

	// Default args ("all")
	cfg, err := ParseRunnerArgs([]string{}, nil, known)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.WorkerNames) != 2 || cfg.RunOnce || cfg.SafeLocalDefaults {
		t.Errorf("unexpected config: %+v", cfg)
	}

	// Specific worker
	cfgWorker, err := ParseRunnerArgs([]string{"discovery"}, nil, known)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfgWorker.WorkerNames) != 1 || cfgWorker.WorkerNames[0] != "discovery" {
		t.Errorf("unexpected worker names: %v", cfgWorker.WorkerNames)
	}

	// Flags --once and --safe-local-defaults
	cfgFlags, err := ParseRunnerArgs([]string{"--once", "--safe-local-defaults"}, nil, known)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cfgFlags.RunOnce || !cfgFlags.SafeLocalDefaults {
		t.Errorf("flags not set: %+v", cfgFlags)
	}

	// --help flag
	cfgHelp, err := ParseRunnerArgs([]string{"--help"}, nil, known)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cfgHelp.Help {
		t.Errorf("expected Help=true")
	}

	// Unknown option
	_, errUnknownOpt := ParseRunnerArgs([]string{"--invalid-flag"}, nil, known)
	if errUnknownOpt == nil || !strings.Contains(errUnknownOpt.Error(), "Unknown option") {
		t.Errorf("expected unknown option error, got: %v", errUnknownOpt)
	}

	// Unknown worker
	_, errUnknownWorker := ParseRunnerArgs([]string{"non-existent"}, nil, known)
	if errUnknownWorker == nil || !strings.Contains(errUnknownWorker.Error(), "Unknown worker") {
		t.Errorf("expected unknown worker error, got: %v", errUnknownWorker)
	}

	// Multiple positional args
	_, errMultiple := ParseRunnerArgs([]string{"discovery", "sync"}, nil, known)
	if errMultiple == nil || !strings.Contains(errMultiple.Error(), "Expected at most one worker name") {
		t.Errorf("expected multiple worker error, got: %v", errMultiple)
	}

	// Environment variable overrides
	env := map[string]string{
		"WORKER_RUN_ONCE":      "true",
		"WORKER_EXIT_ON_ERROR": "1",
	}
	cfgEnv, err := ParseRunnerArgs([]string{}, env, known)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cfgEnv.RunOnce || !cfgEnv.ExitOnError {
		t.Errorf("env flags not parsed: %+v", cfgEnv)
	}
}

func TestRunWithSignals_GracefulShutdown(t *testing.T) {
	s := New(WithShutdownTimeout(time.Second))
	_ = s.Register(&SimpleJob{
		JobName:   "signal-job",
		JobConfig: ScheduleConfig{Mode: ModeInterval, Interval: time.Hour},
	})

	errCh := make(chan error, 1)
	go func() {
		errCh <- RunWithSignals(context.Background(), s)
	}()

	// Allow scheduler to start
	time.Sleep(50 * time.Millisecond)

	// Send SIGINT to own process to trigger graceful shutdown
	_ = syscall.Kill(syscall.Getpid(), syscall.SIGINT)

	select {
	case err := <-errCh:
		if err != nil {
			t.Errorf("unexpected error from RunWithSignals: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("RunWithSignals did not terminate after SIGINT")
	}

	if !s.IsStopped() {
		t.Errorf("expected scheduler to be stopped")
	}
}
