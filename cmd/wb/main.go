package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/aaradhychinche-alt/WhatBreaks/internal/api"
	"github.com/aaradhychinche-alt/WhatBreaks/internal/collector/k8s"
	"github.com/aaradhychinche-alt/WhatBreaks/internal/config"
	"github.com/aaradhychinche-alt/WhatBreaks/internal/health"
	"github.com/aaradhychinche-alt/WhatBreaks/internal/logging"
	"github.com/aaradhychinche-alt/WhatBreaks/internal/platform"
	"github.com/aaradhychinche-alt/WhatBreaks/internal/scheduler"
)

var (
	version = "0.1.0-dev"
)

func main() {
	code := run(os.Args, os.Getenv, os.Stdout, os.Stderr, nil)
	os.Exit(code)
}

func run(args []string, getenv func(string) string, stdout, stderr io.Writer, testCtx context.Context) int {
	if len(args) > 1 && args[1] == "version" {
		fmt.Fprintf(stdout, "WhatBreaks Platform %s\n", version)
		return 0
	}

	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)

	showHelp := fs.Bool("help", false, "Show help message")
	showVersion := fs.Bool("version", false, "Show version")
	apiPort := fs.Int("api-port", api.DefaultPort, "HTTP API server port")
	healthPort := fs.Int("health-port", config.DefaultHealthPort, "HTTP health probe server port")
	coreAddr := fs.String("core-engine-addr", config.DefaultCoreEngineAddress, "Rust Core Engine gRPC endpoint")
	k8sEnable := fs.Bool("enable-k8s", false, "Enable Kubernetes collector")
	k8sAPIURL := fs.String("k8s-api-url", "", "Kubernetes API server URL")
	k8sClusterID := fs.String("k8s-cluster-id", "", "Kubernetes cluster identifier")
	k8sClusterWide := fs.Bool("k8s-cluster-wide", false, "Enable cluster-wide Kubernetes collection")
	k8sNamespaces := fs.String("k8s-namespaces", "", "Comma-separated list of namespaces to observe")

	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}

	if *showHelp {
		printUsage(fs, stdout)
		return 0
	}

	if *showVersion {
		fmt.Fprintf(stdout, "WhatBreaks Platform %s\n", version)
		return 0
	}

	logger := logging.NewStandardLogger(stderr, logging.LevelInfo)
	logger.Info("Initializing WhatBreaks Platform", "version", version)

	if getenv == nil {
		getenv = os.Getenv
	}

	// Verify forbidden environment variable invariants (anti-credential leakage)
	if getenv(config.EnvWbApiToken) != "" || getenv(config.EnvTtApiToken) != "" {
		logger.Error(config.ErrForbiddenRawTokenEnv.Error())
		return 1
	}
	if getenv(config.EnvKubeconfig) != "" {
		logger.Error(config.ErrForbiddenKubeconfigEnv.Error())
		return 1
	}

	envLookup := func(k string) (string, bool) {
		v := getenv(k)
		return v, v != ""
	}

	cfg := &config.PlatformConfig{
		Health: config.HealthConfig{
			Port: *healthPort,
		},
		CoreEngine: config.CoreEngineConfig{
			Address: *coreAddr,
			Timeout: config.DefaultCoreEngineTimeout,
		},
		Logging: config.LoggingConfig{
			Level:       config.DefaultLogLevel,
			ServiceName: "whatbreaks-platform",
		},
	}

	// 1. Health State and Probe Server
	healthState := health.NewState()
	healthSrv := health.NewServer("0.0.0.0", *healthPort, healthState, logger)

	// 2. HTTP API Server
	apiCfg := api.NewConfigFromEnv(envLookup, logger)
	if *apiPort != api.DefaultPort {
		apiCfg.Port = *apiPort
	}
	apiSrv := api.NewServer(apiCfg)

	// 3. Worker Task Scheduler
	sched := scheduler.New(scheduler.WithLogger(logger))

	// 4. Assembled Platform
	var platformOpts []platform.Option
	platformOpts = append(platformOpts,
		platform.WithAPIServer(apiSrv),
		platform.WithHealthServer(healthSrv),
		platform.WithHealthState(healthState),
		platform.WithScheduler(sched),
	)

	if *k8sEnable || *k8sAPIURL != "" {
		k8sClient, err := k8s.NewRESTClient(k8s.ClientConfig{
			BaseURL: *k8sAPIURL,
			Logger:  logger,
		})
		if err != nil {
			logger.Error("Failed to initialize Kubernetes client", "error", err)
			return 1
		}
		var namespaces []string
		if *k8sNamespaces != "" {
			for _, ns := range strings.Split(*k8sNamespaces, ",") {
				if t := strings.TrimSpace(ns); t != "" {
					namespaces = append(namespaces, t)
				}
			}
		}
		k8sCol, err := k8s.New(
			k8s.WithClient(k8sClient),
			k8s.WithClusterID(*k8sClusterID),
			k8s.WithClusterWide(*k8sClusterWide),
			k8s.WithNamespaces(namespaces...),
			k8s.WithLogger(logger),
		)
		if err != nil {
			logger.Error("Failed to initialize Kubernetes collector", "error", err)
			return 1
		}
		platformOpts = append(platformOpts, platform.WithK8sCollector(k8sCol))
	}

	app := platform.New(cfg, logger, nil, platformOpts...)

	startCtx, startCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer startCancel()

	if err := app.Start(startCtx); err != nil {
		logger.Error("Failed to start platform", "error", err)
		return 1
	}

	// Wait for shutdown signal or context cancellation
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	if testCtx != nil {
		select {
		case sig := <-sigCh:
			logger.Info("Received signal, initiating shutdown", "signal", sig.String())
		case <-testCtx.Done():
			logger.Info("Context cancelled, initiating shutdown")
		}
	} else {
		sig := <-sigCh
		logger.Info("Received signal, initiating shutdown", "signal", sig.String())
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), config.DefaultShutdownTimeout)
	defer shutdownCancel()

	if err := app.Stop(shutdownCtx); err != nil {
		logger.Error("Shutdown completed with error", "error", err)
		return 1
	}

	logger.Info("WhatBreaks shutdown complete")
	return 0
}

func printUsage(fs *flag.FlagSet, out io.Writer) {
	fmt.Fprintln(out, "Usage: wb [options] [command]")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Commands:")
	fmt.Fprintln(out, "  version           Print WhatBreaks version")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Options:")
	fs.SetOutput(out)
	fs.PrintDefaults()
}
