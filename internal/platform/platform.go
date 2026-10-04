package platform

import (
	"context"
	"fmt"
	"sync"

	"github.com/aaradhychinche-alt/WhatBreaks/internal/config"
	"github.com/aaradhychinche-alt/WhatBreaks/internal/coreclient"
	"github.com/aaradhychinche-alt/WhatBreaks/internal/health"
	"github.com/aaradhychinche-alt/WhatBreaks/internal/lifecycle"
	"github.com/aaradhychinche-alt/WhatBreaks/internal/logging"
)

// Platform encapsulates the top-level orchestration and dependency composition of WhatBreaks.
type Platform struct {
	Config      *config.PlatformConfig
	Logger      logging.Logger
	HealthState *health.State
	Tracker     lifecycle.WorkTracker
	CoreClient  *coreclient.Client

	mu      sync.Mutex
	running bool
}

// New constructs a Platform instance with the provided configuration and infrastructure dependencies.
func New(
	cfg *config.PlatformConfig,
	logger logging.Logger,
	coreClient *coreclient.Client,
) *Platform {
	if cfg == nil {
		cfg = &config.PlatformConfig{}
	}
	if logger == nil {
		logger = logging.NewStandardLogger(nil, logging.LevelInfo)
	}

	return &Platform{
		Config:      cfg,
		Logger:      logger,
		HealthState: health.NewState(),
		Tracker:     lifecycle.NewSimpleTracker(),
		CoreClient:  coreClient,
	}
}

// Start transitions platform health to healthy and begins platform services.
func (p *Platform) Start(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.running {
		return fmt.Errorf("platform: already running")
	}

	p.Logger.Info("WhatBreaks platform starting",
		"healthPort", p.Config.Health.Port,
		"coreEngineAddr", p.Config.CoreEngine.Address,
	)

	p.HealthState.SetHealthy(true)
	p.HealthState.SetReady(true)
	p.running = true

	return nil
}

// Stop initiates graceful platform shutdown, sets readyz to false, and drains active tasks.
func (p *Platform) Stop(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.running {
		return nil
	}

	p.Logger.Info("WhatBreaks platform stopping")
	p.HealthState.SetReady(false)

	if err := p.Tracker.WaitForIdle(ctx); err != nil {
		p.Logger.Warn("Platform shutdown wait completed with error", "error", err)
	}

	if p.CoreClient != nil {
		if err := p.CoreClient.Close(); err != nil {
			p.Logger.Warn("Failed to close core engine client", "error", err)
		}
	}

	p.HealthState.SetHealthy(false)
	p.running = false
	p.Logger.Info("WhatBreaks platform stopped")
	return nil
}

// IsRunning reports whether the platform is currently active.
func (p *Platform) IsRunning() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.running
}
