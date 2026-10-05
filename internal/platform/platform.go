package platform

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/aaradhychinche-alt/WhatBreaks/internal/api"
	"github.com/aaradhychinche-alt/WhatBreaks/internal/collector/k8s"
	"github.com/aaradhychinche-alt/WhatBreaks/internal/config"
	"github.com/aaradhychinche-alt/WhatBreaks/internal/coreclient"
	"github.com/aaradhychinche-alt/WhatBreaks/internal/database"
	"github.com/aaradhychinche-alt/WhatBreaks/internal/health"
	"github.com/aaradhychinche-alt/WhatBreaks/internal/lifecycle"
	"github.com/aaradhychinche-alt/WhatBreaks/internal/logging"
	"github.com/aaradhychinche-alt/WhatBreaks/internal/scheduler"
)

// Platform encapsulates the top-level composition root and lifecycle coordination of WhatBreaks.
type Platform struct {
	Config       *config.PlatformConfig
	Logger       logging.Logger
	Database     *database.Database
	Scheduler    *scheduler.TaskScheduler
	APIServer    *api.Server
	HealthServer *health.Server
	K8sCollector *k8s.Collector
	CoreClient   *coreclient.Client
	Lifecycle    *lifecycle.ControllerLifecycle
	Runtime      *lifecycle.Runtime
	HealthState  *health.State
	Tracker      lifecycle.WorkTracker

	mu      sync.Mutex
	running bool
	stopped bool
}

// Option configures optional Platform dependencies.
type Option func(*Platform)

// WithDatabase assigns an initialized Database instance to Platform.
func WithDatabase(db *database.Database) Option {
	return func(p *Platform) {
		p.Database = db
	}
}

// WithScheduler assigns a TaskScheduler instance to Platform.
func WithScheduler(s *scheduler.TaskScheduler) Option {
	return func(p *Platform) {
		p.Scheduler = s
	}
}

// WithAPIServer assigns an API Server instance to Platform.
func WithAPIServer(srv *api.Server) Option {
	return func(p *Platform) {
		p.APIServer = srv
	}
}

// WithHealthServer assigns an HTTP probe HealthServer instance to Platform.
func WithHealthServer(hs *health.Server) Option {
	return func(p *Platform) {
		p.HealthServer = hs
	}
}

// WithHealthState assigns a shared health.State instance to Platform.
func WithHealthState(hs *health.State) Option {
	return func(p *Platform) {
		if hs != nil {
			p.HealthState = hs
		}
	}
}

// WithLifecycle assigns a ControllerLifecycle coordinator to Platform.
func WithLifecycle(lc *lifecycle.ControllerLifecycle) Option {
	return func(p *Platform) {
		p.Lifecycle = lc
	}
}

// WithK8sCollector assigns an initialized Kubernetes Collector instance to Platform.
func WithK8sCollector(col *k8s.Collector) Option {
	return func(p *Platform) {
		p.K8sCollector = col
	}
}

// New constructs a Platform instance assembling the subsystem dependencies.
func New(
	cfg *config.PlatformConfig,
	logger logging.Logger,
	coreClient *coreclient.Client,
	opts ...Option,
) *Platform {
	if cfg == nil {
		cfg = &config.PlatformConfig{}
	}
	if logger == nil {
		logger = logging.NewStandardLogger(nil, logging.LevelInfo)
	}

	p := &Platform{
		Config:      cfg,
		Logger:      logger,
		HealthState: health.NewState(),
		Tracker:     lifecycle.NewSimpleTracker(),
		CoreClient:  coreClient,
	}

	for _, opt := range opts {
		opt(p)
	}

	return p
}

// Start begins platform services in order: health probes, API server, and scheduler.
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

	// 1. Start Health Probe Server if configured
	if p.HealthServer != nil {
		if err := p.HealthServer.Start(ctx); err != nil {
			p.Logger.Error("Failed to start health probe server", "error", err.Error())
			return fmt.Errorf("platform: health server failed: %w", err)
		}
	}

	// 2. Start API Server if configured
	if p.APIServer != nil {
		if err := p.APIServer.Start(); err != nil {
			p.Logger.Error("Failed to start API server", "error", err.Error())
			if p.HealthServer != nil {
				_ = p.HealthServer.Close()
			}
			return fmt.Errorf("platform: api server failed: %w", err)
		}
	}

	// 3. Start Scheduler if configured
	if p.Scheduler != nil {
		if err := p.Scheduler.Start(ctx); err != nil {
			p.Logger.Error("Failed to start worker scheduler", "error", err.Error())
			if p.APIServer != nil {
				_ = p.APIServer.Close()
			}
			if p.HealthServer != nil {
				_ = p.HealthServer.Close()
			}
			return fmt.Errorf("platform: scheduler failed: %w", err)
		}
	}

	// 4. Start Kubernetes Collector if configured
	if p.K8sCollector != nil {
		if err := p.K8sCollector.Start(ctx); err != nil {
			p.Logger.Error("Failed to start Kubernetes collector", "error", err.Error())
			if p.Scheduler != nil {
				_ = p.Scheduler.Stop(ctx)
			}
			if p.APIServer != nil {
				_ = p.APIServer.Close()
			}
			if p.HealthServer != nil {
				_ = p.HealthServer.Close()
			}
			return fmt.Errorf("platform: k8s collector failed: %w", err)
		}
	}

	// 5. Update health state
	p.HealthState.SetHealthy(true)
	p.HealthState.SetReady(true)
	p.running = true
	p.stopped = false

	p.Logger.Info("WhatBreaks platform started successfully")
	return nil
}

// Stop executes graceful shutdown across all assembled subsystems in deterministic order:
// 1. Set readiness to false (stop receiving external traffic)
// 2. Stop scheduler from initiating new tasks
// 3. Drain in-flight work and active HTTP connections
// 4. Shutdown API server
// 5. Close database connection pool
// 6. Close core engine client gRPC connection
// 7. Close health probe server
// 8. Transition health state to stopped
func (p *Platform) Stop(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.running || p.stopped {
		return nil
	}

	p.Logger.Info("WhatBreaks platform stopping")
	p.HealthState.SetReady(false)

	// 1. If ControllerLifecycle was configured, coordinate through lifecycle
	if p.Lifecycle != nil {
		_, _ = p.Lifecycle.Shutdown(ctx, "manual")
	}

	// 2. Stop Scheduler
	if p.Scheduler != nil {
		if err := p.Scheduler.Stop(ctx); err != nil {
			p.Logger.Warn("Scheduler stop returned error", "error", err.Error())
		}
	}

	// 3. Stop Kubernetes Collector
	if p.K8sCollector != nil {
		if err := p.K8sCollector.Stop(ctx); err != nil {
			p.Logger.Warn("Kubernetes collector stop returned error", "error", err.Error())
		}
	}

	// 4. Drain in-flight tasks through WorkTracker
	if p.Tracker != nil {
		if err := p.Tracker.WaitForIdle(ctx); err != nil {
			p.Logger.Warn("Platform shutdown wait completed with error", "error", err.Error())
		}
	}

	// 4. Shutdown API Server
	if p.APIServer != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := p.APIServer.Shutdown(shutdownCtx); err != nil {
			p.Logger.Warn("API server shutdown returned error", "error", err.Error())
		}
		cancel()
	}

	// 5. Close Database Pool
	if p.Database != nil {
		if err := p.Database.Close(); err != nil {
			p.Logger.Warn("Database pool close returned error", "error", err.Error())
		}
	}

	// 6. Close Core Engine gRPC Client
	if p.CoreClient != nil {
		if err := p.CoreClient.Close(); err != nil {
			p.Logger.Warn("Failed to close core engine client", "error", err.Error())
		}
	}

	// 7. Shutdown Health Probe Server
	if p.HealthServer != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		if err := p.HealthServer.Shutdown(shutdownCtx); err != nil {
			p.Logger.Warn("Health probe server shutdown returned error", "error", err.Error())
		}
		cancel()
	}

	p.HealthState.SetHealthy(false)
	p.running = false
	p.stopped = true
	p.Logger.Info("WhatBreaks platform stopped")
	return nil
}

// IsRunning reports whether the platform is currently active.
func (p *Platform) IsRunning() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.running
}
