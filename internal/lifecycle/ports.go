package lifecycle

import (
	"context"
)

// Port defines the operational contract for controller dependencies (Kubernetes client and reporter),
// matching the contract exercised in kubernetes/controller/runtime.js and ports.js.
type Port interface {
	Start(ctx context.Context) error
	StopAcceptingWork(ctx context.Context) error
	Close(ctx context.Context) error
	IsAlive() bool
	IsReady() bool
}

// UnavailablePort provides a default stub port matching createUnavailableKubernetesClientPort
// and createUnavailableReporterPort from kubernetes/controller/ports.js.
// Default behavior: isAlive is true, isReady is false.
type UnavailablePort struct{}

func (UnavailablePort) Start(ctx context.Context) error             { return nil }
func (UnavailablePort) StopAcceptingWork(ctx context.Context) error { return nil }
func (UnavailablePort) Close(ctx context.Context) error             { return nil }
func (UnavailablePort) IsAlive() bool                               { return true }
func (UnavailablePort) IsReady() bool                               { return false }

// NewUnavailablePort creates an UnavailablePort.
func NewUnavailablePort() Port {
	return UnavailablePort{}
}

// BasePort provides a configurable in-memory Port implementation for testing or simple adapters.
type BasePort struct {
	StartFunc             func(ctx context.Context) error
	StopAcceptingWorkFunc func(ctx context.Context) error
	CloseFunc             func(ctx context.Context) error
	Alive                 bool
	Ready                 bool
}

func (p *BasePort) Start(ctx context.Context) error {
	if p.StartFunc != nil {
		return p.StartFunc(ctx)
	}
	return nil
}

func (p *BasePort) StopAcceptingWork(ctx context.Context) error {
	if p.StopAcceptingWorkFunc != nil {
		return p.StopAcceptingWorkFunc(ctx)
	}
	return nil
}

func (p *BasePort) Close(ctx context.Context) error {
	if p.CloseFunc != nil {
		return p.CloseFunc(ctx)
	}
	return nil
}

func (p *BasePort) IsAlive() bool {
	return p.Alive
}

func (p *BasePort) IsReady() bool {
	return p.Ready
}
