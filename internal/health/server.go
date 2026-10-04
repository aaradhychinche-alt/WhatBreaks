package health

import (
	"context"
	"net/http"
	"sync/atomic"
)

const (
	PathHealthz = "/healthz"
	PathReadyz  = "/readyz"

	BodyHealthy     = "ok"
	BodyUnavailable = "unavailable"
	BodyReady       = "ready"
	BodyNotReady    = "not_ready"
)

// Checker evaluates liveness and readiness of the platform process.
type Checker interface {
	IsHealthy() bool
	IsReady() bool
}

// State provides a lock-free, atomic state holder for health and readiness probes.
type State struct {
	healthy atomic.Bool
	ready   atomic.Bool
}

// NewState creates a State with healthy=true and ready=false by default.
func NewState() *State {
	s := &State{}
	s.healthy.Store(true)
	s.ready.Store(false)
	return s
}

func (s *State) IsHealthy() bool {
	return s.healthy.Load()
}

func (s *State) IsReady() bool {
	return s.ready.Load()
}

func (s *State) SetHealthy(val bool) {
	s.healthy.Store(val)
}

func (s *State) SetReady(val bool) {
	s.ready.Store(val)
}

// Handler returns an http.Handler implementing the /healthz and /readyz probe contract.
func Handler(checker Checker) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(PathHealthz, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			_, _ = w.Write([]byte("method_not_allowed"))
			return
		}
		if checker != nil && checker.IsHealthy() {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(BodyHealthy))
		} else {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(BodyUnavailable))
		}
	})

	mux.HandleFunc(PathReadyz, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			_, _ = w.Write([]byte("method_not_allowed"))
			return
		}
		if checker != nil && checker.IsReady() {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(BodyReady))
		} else {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(BodyNotReady))
		}
	})

	return mux
}

// Server defines the HTTP health probe listener contract.
type Server interface {
	Start(ctx context.Context) error
	Shutdown(ctx context.Context) error
}
