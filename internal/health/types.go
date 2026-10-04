package health

import (
	"sync/atomic"
)

const (
	// PathHealthz is the standard Kubernetes liveness probe route.
	PathHealthz = "/healthz"
	// PathReadyz is the standard Kubernetes readiness probe route.
	PathReadyz = "/readyz"

	// Probe response status values matching kubernetes/controller/health-server.js
	StatusOk          = "ok"
	StatusUnavailable = "unavailable"
	StatusReady       = "ready"
	StatusNotReady    = "not_ready"
	StatusNotFound    = "not_found"

	// Default host and port matching kubernetes/controller/health-server.js
	DefaultHost = "0.0.0.0"
	DefaultPort = 8080
)

// ProbeResponse defines the JSON schema returned by controller health and readiness probes.
type ProbeResponse struct {
	Status string `json:"status"`
}

// ControllerStatus encapsulates the observable health, readiness, and phase of the controller.
type ControllerStatus struct {
	Healthy bool   `json:"healthy"`
	Ready   bool   `json:"ready"`
	Phase   string `json:"phase,omitempty"`
}

// Checker evaluates liveness and readiness of the platform process.
// Maintained for backward compatibility with Step 5A scaffolding.
type Checker interface {
	IsHealthy() bool
	IsReady() bool
}

// StatusProvider supplies current controller status for health endpoints.
type StatusProvider interface {
	Status() ControllerStatus
}

// StatusFunc is an adapter allowing a plain function to act as a StatusProvider.
type StatusFunc func() ControllerStatus

func (f StatusFunc) Status() ControllerStatus {
	return f()
}

// PortChecker checks whether an internal component or port is alive and ready.
type PortChecker interface {
	IsAlive() bool
	IsReady() bool
}

// State provides a lock-free, atomic state holder for health and readiness probes.
// Implements both Checker and StatusProvider.
type State struct {
	healthy atomic.Bool
	ready   atomic.Bool
	phase   atomic.Pointer[string]
}

// NewState creates a State with healthy=true and ready=false by default.
func NewState() *State {
	s := &State{}
	s.healthy.Store(true)
	s.ready.Store(false)
	defaultPhase := "starting"
	s.phase.Store(&defaultPhase)
	return s
}

func (s *State) IsHealthy() bool {
	return s.healthy.Load()
}

func (s *State) IsReady() bool {
	return s.ready.Load()
}

func (s *State) Phase() string {
	p := s.phase.Load()
	if p == nil {
		return ""
	}
	return *p
}

func (s *State) SetHealthy(val bool) {
	s.healthy.Store(val)
}

func (s *State) SetReady(val bool) {
	s.ready.Store(val)
}

func (s *State) SetPhase(val string) {
	s.phase.Store(&val)
}

// Status returns a snapshot of ControllerStatus.
func (s *State) Status() ControllerStatus {
	return ControllerStatus{
		Healthy: s.IsHealthy(),
		Ready:   s.IsReady(),
		Phase:   s.Phase(),
	}
}

// CheckerStatusAdapter adapts a Checker to the StatusProvider interface.
type CheckerStatusAdapter struct {
	Checker Checker
}

func (a CheckerStatusAdapter) Status() ControllerStatus {
	if a.Checker == nil {
		return ControllerStatus{Healthy: false, Ready: false}
	}
	return ControllerStatus{
		Healthy: a.Checker.IsHealthy(),
		Ready:   a.Checker.IsReady(),
	}
}

// ControllerLifecycleChecker calculates health and readiness based on controller phase,
// acceptingWork state, and dependent port statuses, exactly matching
// kubernetes/controller/lifecycle.js and runtime.js.
type ControllerLifecycleChecker struct {
	PhaseFn       func() string
	AcceptingWork func() bool
	ClientPort    PortChecker
	ReporterPort  PortChecker
}

func (c *ControllerLifecycleChecker) Status() ControllerStatus {
	phase := "starting"
	if c.PhaseFn != nil {
		phase = c.PhaseFn()
	}

	clientAlive := c.ClientPort == nil || c.ClientPort.IsAlive()
	reporterAlive := c.ReporterPort == nil || c.ReporterPort.IsAlive()
	healthy := phase == "running" && clientAlive && reporterAlive

	accepting := c.AcceptingWork != nil && c.AcceptingWork()
	clientReady := c.ClientPort != nil && c.ClientPort.IsReady()
	reporterReady := c.ReporterPort != nil && c.ReporterPort.IsReady()
	ready := healthy && accepting && clientReady && reporterReady

	return ControllerStatus{
		Healthy: healthy,
		Ready:   ready,
		Phase:   phase,
	}
}
