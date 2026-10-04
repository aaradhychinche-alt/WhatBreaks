package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/aaradhychinche-alt/WhatBreaks/internal/config"
	"github.com/aaradhychinche-alt/WhatBreaks/internal/logging"
	"github.com/aaradhychinche-alt/WhatBreaks/internal/platform"
)

var (
	version = "0.1.0-dev"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "version" {
		fmt.Printf("WhatBreaks Platform %s\n", version)
		os.Exit(0)
	}

	showHelp := flag.Bool("help", false, "Show help message")
	showVersion := flag.Bool("version", false, "Show version")
	healthPort := flag.Int("health-port", config.DefaultHealthPort, "HTTP health probe server port")
	coreAddr := flag.String("core-engine-addr", config.DefaultCoreEngineAddress, "Rust Core Engine gRPC endpoint")
	flag.Parse()

	if *showHelp {
		printUsage()
		os.Exit(0)
	}

	if *showVersion {
		fmt.Printf("WhatBreaks Platform %s\n", version)
		os.Exit(0)
	}

	logger := logging.NewStandardLogger(os.Stderr, logging.LevelInfo)
	logger.Info("Initializing WhatBreaks Platform", "version", version)

	// In Step 5A, verify forbidden environment variable invariants
	if os.Getenv(config.EnvWbApiToken) != "" || os.Getenv(config.EnvTtApiToken) != "" {
		logger.Error(config.ErrForbiddenRawTokenEnv.Error())
		os.Exit(1)
	}
	if os.Getenv(config.EnvKubeconfig) != "" {
		logger.Error(config.ErrForbiddenKubeconfigEnv.Error())
		os.Exit(1)
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

	// Initialize platform orchestration root
	app := platform.New(cfg, logger, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	if err := app.Start(ctx); err != nil {
		logger.Error("Failed to start platform", "error", err)
		os.Exit(1)
	}

	sig := <-sigCh
	logger.Info("Received signal, initiating shutdown", "signal", sig.String())

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), config.DefaultShutdownTimeout)
	defer shutdownCancel()

	if err := app.Stop(shutdownCtx); err != nil {
		logger.Error("Shutdown completed with error", "error", err)
		os.Exit(1)
	}

	logger.Info("WhatBreaks shutdown complete")
}

func printUsage() {
	fmt.Println("Usage: wb [options] [command]")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  version           Print WhatBreaks version")
	fmt.Println()
	fmt.Println("Options:")
	flag.PrintDefaults()
}
