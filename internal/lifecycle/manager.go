package lifecycle

import (
	"context"
)

// Manager coordinates platform lifecycle transitions and graceful teardown.
// Maintained for backward compatibility with Step 5A scaffolding.
type Manager interface {
	Phase() Phase
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
	Wait() <-chan struct{}
}
