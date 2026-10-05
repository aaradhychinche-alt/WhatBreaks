package api

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/aaradhychinche-alt/WhatBreaks/internal/health"
	"github.com/aaradhychinche-alt/WhatBreaks/internal/logging"
)

// Server encapsulates the HTTP API server, middleware stack, and routes.
type Server struct {
	cfg         Config
	logger      logging.Logger
	rateLimiter *RateLimiter
	csrfManager *CSRFManager
	mux         *http.ServeMux
	handler     http.Handler
	httpServer  *http.Server
	listener    net.Listener
	mu          sync.Mutex
	running     bool
	stopped     bool
}

// NewServer constructs an API Server configured with the exact middleware pipeline
// from infrastructure/api/index.js.
func NewServer(cfg Config) *Server {
	if cfg.Logger == nil {
		cfg.Logger = logging.NewStandardLogger(nil, logging.LevelInfo)
	}

	rateLimiter := NewRateLimiter(cfg.RateLimitWindow, cfg.RateLimitMax, cfg.IsDevelopment || cfg.IsTest, cfg.Logger)
	csrfManager := NewCSRFManager(cfg.SessionSecret, cfg.CsrfCookieName, cfg.SessionCookie, cfg.IsTest)

	mux := http.NewServeMux()

	s := &Server{
		cfg:         cfg,
		logger:      cfg.Logger,
		rateLimiter: rateLimiter,
		csrfManager: csrfManager,
		mux:         mux,
	}

	s.setupRoutes()
	s.buildHandlerPipeline()

	return s
}

func (s *Server) setupRoutes() {
	// 1. Health routes matching infrastructure/api/routes/health.js via internal/health
	healthHandler := health.NewAPIHealthHandler(health.APIHealthConfig{
		Pinger:      s.cfg.HealthPinger,
		Logger:      s.logger,
		Environment: s.cfg.Environment,
		StartTime:   time.Now(),
	})
	s.mux.Handle("GET /{$}", healthHandler)
	s.mux.Handle("GET /health", healthHandler)

	// 2. CSRF token generation route
	s.mux.Handle("GET /api/csrf-token", s.csrfManager.TokenHandler())

	// 3. Fallback 404 handler matching Express lines 109-114
	s.mux.Handle("/", NotFoundHandler())
}

func (s *Server) buildHandlerPipeline() {
	// Middleware composition pipeline in exact order:
	// 1. Recovery
	// 2. Security Headers (Helmet)
	// 3. CORS
	// 4. Body Limit (10MB)
	// 5. Rate Limiting
	// 6. CSRF Protection
	// 7. Routes (mux)

	h := http.Handler(s.mux)

	// CSRF validation
	h = s.csrfManager.Middleware()(h)

	// Rate limiting
	h = s.rateLimiter.Middleware()(h)

	// Body size limiter (10MB)
	h = BodyLimit(s.cfg.MaxBodyBytes)(h)

	// CORS
	h = CORS(s.cfg.AllowedOrigins)(h)

	// Security Headers
	h = SecurityHeaders(s.cfg.AppURL)(h)

	// Panic Recovery
	h = RecoveryMiddleware(s.logger, s.cfg.IsDevelopment)(h)

	s.handler = h
}

// RegisterRoute mounts a custom handler into the API router.
func (s *Server) RegisterRoute(pattern string, handler http.Handler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mux.Handle(pattern, handler)
}

// Handler returns the fully-wrapped HTTP handler pipeline (useful for testing).
func (s *Server) Handler() http.Handler {
	return s.handler
}

// RateLimiter returns the process-local rate limiter instance.
func (s *Server) RateLimiter() *RateLimiter {
	return s.rateLimiter
}

// CSRFManager returns the CSRF manager instance.
func (s *Server) CSRFManager() *CSRFManager {
	return s.csrfManager
}

// Start binds to the configured network interface and starts serving HTTP requests in the background.
func (s *Server) Start() error {
	return s.Listen()
}

// Listen starts serving in the background and returns immediately once the network listener is ready.
func (s *Server) Listen() error {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return fmt.Errorf("api server already running")
	}

	addr := fmt.Sprintf("%s:%d", s.cfg.Host, s.cfg.Port)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		s.mu.Unlock()
		return fmt.Errorf("failed to listen on %s: %w", addr, err)
	}

	s.listener = listener
	s.httpServer = &http.Server{
		Handler:      s.handler,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}
	s.running = true
	s.mu.Unlock()

	s.logger.Info(fmt.Sprintf("Server running on http://%s", s.listener.Addr().String()))
	s.logger.Info(fmt.Sprintf("Environment: %s", s.cfg.Environment))

	go func() {
		if err := s.httpServer.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.logger.Error("API server terminated unexpectedly", "error", err)
		}
	}()

	return nil
}

// Addr returns the bound TCP address (host:port).
func (s *Server) Addr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener != nil {
		return s.listener.Addr().String()
	}
	return fmt.Sprintf("%s:%d", s.cfg.Host, s.cfg.Port)
}

// Shutdown gracefully stops the HTTP server, draining active requests up to timeout.
func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	if !s.running || s.stopped {
		s.mu.Unlock()
		return nil
	}
	s.stopped = true
	srv := s.httpServer
	s.mu.Unlock()

	s.logger.Info("HTTP server closing")
	var err error
	if srv != nil {
		err = srv.Shutdown(ctx)
	}

	s.mu.Lock()
	s.running = false
	s.mu.Unlock()

	s.logger.Info("HTTP server closed")
	return err
}

// Close immediately closes the HTTP listener.
func (s *Server) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.httpServer != nil {
		return s.httpServer.Close()
	}
	return nil
}
