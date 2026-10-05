package lifecycle

// Phase represents the discrete lifecycle states of the controller, matching
// kubernetes/controller/lifecycle.js.
type Phase string

const (
	PhaseStarting Phase = "starting"
	PhaseRunning  Phase = "running"
	PhaseStopping Phase = "stopping"
	PhaseStopped  Phase = "stopped"
	PhaseFailed   Phase = "failed"
)

// String returns the string representation of Phase.
func (p Phase) String() string {
	return string(p)
}

// IsValid reports whether the phase is a recognized lifecycle state.
func (p Phase) IsValid() bool {
	switch p {
	case PhaseStarting, PhaseRunning, PhaseStopping, PhaseStopped, PhaseFailed:
		return true
	default:
		return false
	}
}

// IsTerminal reports whether the phase represents a completed lifecycle state.
func (p Phase) IsTerminal() bool {
	return p == PhaseStopped || p == PhaseFailed
}

// IsValidTransition validates whether transitioning from one phase to another is legal.
func IsValidTransition(from, to Phase) bool {
	if from == to {
		return true
	}
	switch from {
	case PhaseStarting:
		return to == PhaseRunning || to == PhaseStopping || to == PhaseFailed
	case PhaseRunning:
		return to == PhaseStopping || to == PhaseFailed
	case PhaseStopping:
		return to == PhaseStopped || to == PhaseFailed
	case PhaseStopped, PhaseFailed:
		return false // Terminal states
	default:
		return false
	}
}
