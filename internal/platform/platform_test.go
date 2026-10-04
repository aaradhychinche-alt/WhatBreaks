package platform

import (
	"context"
	"testing"
	"time"

	"github.com/aaradhychinche-alt/WhatBreaks/internal/config"
	"github.com/aaradhychinche-alt/WhatBreaks/internal/logging"
)

func TestPlatform_Lifecycle(t *testing.T) {
	cfg := &config.PlatformConfig{
		Health: config.HealthConfig{
			Port: 8081,
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
