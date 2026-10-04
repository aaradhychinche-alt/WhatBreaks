package health

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/aaradhychinche-alt/WhatBreaks/internal/logging"
)

var (
	ErrServerAlreadyRunning = errors.New("health server is already running")
	ErrServerNotRunning     = errors.New("health server is not running")
	ErrMissingStatusFunc    = errors.New("getStatus provider is required")
)

// WritePublicResponse writes a JSON response matching writePublicResponse from
// kubernetes/controller/health-server.js.
func WritePublicResponse(w http.ResponseWriter, statusCode int, payload any) {
	body, err := json.Marshal(payload)
	if err != nil {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Content-Length", "24")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"status":"unavailable"}`))
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(statusCode)
	_, _ = w.Write(body)
}

// ControllerHealthHandler returns an http.Handler implementing the exact routing
// and response behavior of kubernetes/controller/health-server.js.
func ControllerHealthHandler(provider StatusProvider) http.Handler {
	if provider == nil {
		provider = CheckerStatusAdapter{Checker: nil}
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Matching JS: if (req.method !== "GET") return writePublicResponse(res, 404, { status: "not_found" });
		if r.Method != http.MethodGet {
			WritePublicResponse(w, http.StatusNotFound, ProbeResponse{Status: StatusNotFound})
			return
		}

		status := provider.Status()
		switch r.URL.Path {
		case PathHealthz:
			if status.Healthy {
				WritePublicResponse(w, http.StatusOK, ProbeResponse{Status: StatusOk})
			} else {
				WritePublicResponse(w, http.StatusServiceUnavailable, ProbeResponse{Status: StatusUnavailable})
			}
		case PathReadyz:
			if status.Ready {
				WritePublicResponse(w, http.StatusOK, ProbeResponse{Status: StatusReady})
			} else {
				WritePublicResponse(w, http.StatusServiceUnavailable, ProbeResponse{Status: StatusNotReady})
			}
		default:
			WritePublicResponse(w, http.StatusNotFound, ProbeResponse{Status: StatusNotFound})
		}
	})
}

// Handler returns an http.Handler implementing probe endpoints.
// Maintained for backward compatibility with Step 5A scaffolding.
func Handler(checker Checker) http.Handler {
	return ControllerHealthHandler(CheckerStatusAdapter{Checker: checker})
}

// Server represents an HTTP probe listener for the Kubernetes controller.
type Server struct {
	mu         sync.Mutex
	host       string
	port       int
	provider   StatusProvider
	logger     logging.Logger
	listener   net.Listener
	httpServer *http.Server
	serving    bool
}

// NewServer constructs a Server configured with host, port, status provider, and logger.
func NewServer(host string, port int, provider StatusProvider, logger logging.Logger) *Server {
	if host == "" {
		host = DefaultHost
	}
	if port <= 0 {
		port = DefaultPort
	}
	if logger == nil {
		logger = logging.NewStandardLogger(nil, logging.LevelInfo)
	}

	return &Server{
		host:     host,
		port:     port,
		provider: provider,
		logger:   logger,
	}
}

// Listen binds the network listener on the configured host and port without starting the accept loop.
func (s *Server) Listen() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.listener != nil {
		return ErrServerAlreadyRunning
	}

	addr := fmt.Sprintf("%s:%d", s.host, s.port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("failed to bind health server on %s: %w", addr, err)
	}

	s.listener = ln
	s.httpServer = &http.Server{
		Handler:      ControllerHealthHandler(s.provider),
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
	}

	return nil
}

// Start binds and starts serving health requests in a background goroutine.
func (s *Server) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.serving {
		s.mu.Unlock()
		return ErrServerAlreadyRunning
	}

	if s.listener == nil {
		addr := fmt.Sprintf("%s:%d", s.host, s.port)
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			s.mu.Unlock()
			return fmt.Errorf("failed to bind health server on %s: %w", addr, err)
		}
		s.listener = ln
		s.httpServer = &http.Server{
			Handler:      ControllerHealthHandler(s.provider),
			ReadTimeout:  5 * time.Second,
			WriteTimeout: 5 * time.Second,
		}
	}

	s.serving = true
	ln := s.listener
	srv := s.httpServer
	s.mu.Unlock()

	s.logger.Info("health server listening",
		"host", s.host,
		"port", s.Port(),
		"addr", ln.Addr().String(),
	)

	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.logger.Error("health server serve error", "error", err)
		}
	}()

	return nil
}

// Addr returns the net.Addr of the active listener, or nil if not listening.
func (s *Server) Addr() net.Addr {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener == nil {
		return nil
	}
	return s.listener.Addr()
}

// Port returns the bound TCP port (useful when port 0 is used for ephemeral allocation).
func (s *Server) Port() int {
	addr := s.Addr()
	if addr == nil {
		return s.port
	}
	if tcpAddr, ok := addr.(*net.TCPAddr); ok {
		return tcpAddr.Port
	}
	return s.port
}

// Shutdown gracefully terminates the HTTP health server.
func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	srv := s.httpServer
	s.serving = false
	s.mu.Unlock()

	if srv == nil {
		return nil
	}

	return srv.Shutdown(ctx)
}

// Close immediately closes the HTTP health server and listener.
func (s *Server) Close() error {
	s.mu.Lock()
	srv := s.httpServer
	ln := s.listener
	s.httpServer = nil
	s.listener = nil
	s.serving = false
	s.mu.Unlock()

	var err error
	if srv != nil {
		err = srv.Close()
	}
	if ln != nil {
		if lnErr := ln.Close(); lnErr != nil && err == nil {
			err = lnErr
		}
	}
	return err
}
