package platform

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/aaradhychinche-alt/WhatBreaks/internal/api"
	"github.com/aaradhychinche-alt/WhatBreaks/internal/config"
	"github.com/aaradhychinche-alt/WhatBreaks/internal/health"
	"github.com/aaradhychinche-alt/WhatBreaks/internal/logging"
	"github.com/aaradhychinche-alt/WhatBreaks/internal/scheduler"
)

func getFreePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to find free port: %v", err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func TestPlatform_Lifecycle(t *testing.T) {
	cfg := &config.PlatformConfig{
		Health: config.HealthConfig{
			Port: getFreePort(t),
		},
		CoreEngine: config.CoreEngineConfig{
			Address: "127.0.0.1:50051",
			Timeout: 2 * time.Second,
		},
	}
	logger := logging.NewStandardLogger(nil, logging.LevelDebug)

	app := New(cfg, logger, nil)
	if app.IsRunning() {
		t.Fatalf("expected platform not to be running before start")
	}

	ctx := context.Background()
	if err := app.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	if !app.IsRunning() {
		t.Fatalf("expected platform to be running after start")
	}
	if !app.HealthState.IsHealthy() {
		t.Errorf("expected platform to be healthy after start")
	}
	if !app.HealthState.IsReady() {
		t.Errorf("expected platform to be ready after start")
	}

	// Double start should fail
	if err := app.Start(ctx); err == nil {
		t.Errorf("expected error on duplicate start")
	}

	stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := app.Stop(stopCtx); err != nil {
		t.Fatalf("Stop failed: %v", err)
	}

	if app.IsRunning() {
		t.Fatalf("expected platform not to be running after stop")
	}
	if app.HealthState.IsReady() {
		t.Errorf("expected platform readyz to be false after stop")
	}
	if app.HealthState.IsHealthy() {
		t.Errorf("expected platform healthz to be false after stop")
	}
}

func TestPlatform_AssembledDependencies(t *testing.T) {
	healthPort := getFreePort(t)
	apiPort := getFreePort(t)

	cfg := &config.PlatformConfig{
		Health: config.HealthConfig{
			Port: healthPort,
		},
		CoreEngine: config.CoreEngineConfig{
			Address: "127.0.0.1:50051",
			Timeout: 2 * time.Second,
		},
	}
	logger := logging.NewStandardLogger(nil, logging.LevelDebug)

	apiCfg := api.DefaultConfig()
	apiCfg.Port = apiPort
	apiCfg.Host = "127.0.0.1"
	apiCfg.SessionSecret = "0123456789abcdef0123456789abcdef"
	apiCfg.Logger = logger

	apiSrv := api.NewServer(apiCfg)

	healthState := health.NewState()
	healthSrv := health.NewServer("127.0.0.1", healthPort, healthState, logger)
	sched := scheduler.New(scheduler.WithLogger(logger))

	app := New(cfg, logger, nil,
		WithAPIServer(apiSrv),
		WithHealthServer(healthSrv),
		WithHealthState(healthState),
		WithScheduler(sched),
	)

	ctx := context.Background()
	if err := app.Start(ctx); err != nil {
		t.Fatalf("failed to start platform with assembled dependencies: %v", err)
	}

	// Test API server response
	apiURL := fmt.Sprintf("http://127.0.0.1:%d/health", apiPort)
	resp, err := http.Get(apiURL)
	if err != nil {
		t.Fatalf("failed to query API server health: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected API status 200, got %d", resp.StatusCode)
	}

	// Test Health server response
	healthURL := fmt.Sprintf("http://127.0.0.1:%d/healthz", healthPort)
	resp2, err := http.Get(healthURL)
	if err != nil {
		t.Fatalf("failed to query health probe server: %v", err)
	}
	defer resp2.Body.Close()
	body, _ := io.ReadAll(resp2.Body)
	if resp2.StatusCode != http.StatusOK {
		t.Errorf("expected health probe 200, got %d: %s", resp2.StatusCode, string(body))
	}

	// Stop platform
	stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := app.Stop(stopCtx); err != nil {
		t.Fatalf("failed to stop platform: %v", err)
	}

	if app.IsRunning() {
		t.Errorf("expected platform not to be running after stop")
	}
}

func TestPlatform_StartupFailureCleanup(t *testing.T) {
	// Occupy a port first to force a conflict
	occupiedPort := getFreePort(t)
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", occupiedPort))
	if err != nil {
		t.Fatalf("failed to occupy port: %v", err)
	}
	defer ln.Close()

	cfg := &config.PlatformConfig{
		Health: config.HealthConfig{
			Port: occupiedPort,
		},
	}
	logger := logging.NewStandardLogger(nil, logging.LevelDebug)

	// Configure health server on the occupied port
	healthSrv := health.NewServer("127.0.0.1", occupiedPort, nil, logger)
	app := New(cfg, logger, nil, WithHealthServer(healthSrv))

	ctx := context.Background()
	err = app.Start(ctx)
	if err == nil {
		t.Fatalf("expected platform.Start to fail on port conflict")
	}

	if app.IsRunning() {
		t.Errorf("platform should not be marked running after startup failure")
	}
}

func TestPlatform_RepeatedAndConcurrentShutdown(t *testing.T) {
	cfg := &config.PlatformConfig{
		Health: config.HealthConfig{
			Port: getFreePort(t),
		},
	}
	logger := logging.NewStandardLogger(nil, logging.LevelDebug)

	app := New(cfg, logger, nil)
	ctx := context.Background()
	if err := app.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	var wg sync.WaitGroup
	errs := make(chan error, 10)

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if err := app.Stop(stopCtx); err != nil {
				errs <- err
			}
		}()
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("concurrent Stop failed: %v", err)
	}

	if app.IsRunning() {
		t.Errorf("expected platform to be stopped")
	}

	// Another sequential Stop should be a no-op
	if err := app.Stop(ctx); err != nil {
		t.Errorf("repeated Stop failed: %v", err)
	}
}
