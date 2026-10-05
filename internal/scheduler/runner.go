package scheduler

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
)

// RunnerConfig represents parsed CLI and environment options for worker execution.
type RunnerConfig struct {
	WorkerNames       []string
	RunOnce           bool
	SafeLocalDefaults bool
	ExitOnError       bool
	Help              bool
}

// ParseRunnerArgs parses command-line arguments and environment variables
// replicating parseRunnerArgs from runner.js.
func ParseRunnerArgs(args []string, env map[string]string, knownWorkers []string) (*RunnerConfig, error) {
	config := &RunnerConfig{
		WorkerNames: make([]string, 0),
	}

	if env != nil {
		if val := env["WORKER_RUN_ONCE"]; val != "" {
			config.RunOnce = parseBoolEnv(val)
		}
		if val := env["WORKER_EXIT_ON_ERROR"]; val != "" {
			config.ExitOnError = parseBoolEnv(val)
		}
	}

	var positional []string
	for _, arg := range args {
		switch {
		case arg == "--once":
			config.RunOnce = true
		case arg == "--safe-local-defaults":
			config.SafeLocalDefaults = true
		case arg == "--help" || arg == "-h":
			config.Help = true
			return config, nil
		case strings.HasPrefix(arg, "-"):
			return nil, fmt.Errorf("Unknown option %q", arg)
		default:
			positional = append(positional, arg)
		}
	}

	if len(positional) > 1 {
		return nil, fmt.Errorf("Expected at most one worker name")
	}

	selected := "all"
	if len(positional) == 1 && positional[0] != "" {
		selected = positional[0]
	}

	if selected == "all" {
		config.WorkerNames = append(config.WorkerNames, knownWorkers...)
	} else {
		found := false
		for _, w := range knownWorkers {
			if w == selected {
				found = true
				break
			}
		}
		if !found && len(knownWorkers) > 0 {
			knownList := append([]string{"all"}, knownWorkers...)
			return nil, fmt.Errorf("Unknown worker %q. Expected one of: %s", selected, strings.Join(knownList, ", "))
		}
		config.WorkerNames = []string{selected}
	}

	return config, nil
}

func parseBoolEnv(val string) bool {
	switch strings.ToLower(strings.TrimSpace(val)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// RunWithSignals starts the scheduler, attaches SIGINT and SIGTERM handlers,
// and initiates graceful shutdown upon receiving a termination signal.
func RunWithSignals(ctx context.Context, s *TaskScheduler) error {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	if err := s.Start(ctx); err != nil {
		return err
	}

	select {
	case <-ctx.Done():
		return s.Stop(context.Background())
	case sig := <-sigCh:
		sigName := sig.String()
		if sig == syscall.SIGINT {
			sigName = "SIGINT"
		} else if sig == syscall.SIGTERM {
			sigName = "SIGTERM"
		}
		return s.StopWithSignal(context.Background(), sigName)
	}
}
