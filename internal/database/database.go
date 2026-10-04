package database

import (
	"context"
	"errors"
)

var (
	ErrLockAcquisitionFailed = errors.New("database: failed to acquire advisory lock")
	ErrDatabaseNotReady      = errors.New("database: database connection is not ready")
)

// Hash32 computes the 32-bit signed integer hash matching the legacy JavaScript hash32 algorithm:
//
//	let h = 0;
//	for (let i = 0; i < s.length; i++) {
//	    h = (h << 5) - h + s.charCodeAt(i);
//	    h |= 0;
//	}
//	return h;
//
// This ensures advisory lock compatibility across database operations.
func Hash32(s string) int32 {
	var h int32
	for i := 0; i < len(s); i++ {
		h = (h << 5) - h + int32(s[i])
	}
	return h
}

// AdvisoryLocker provides application-level concurrency control via PostgreSQL advisory locks.
type AdvisoryLocker interface {
	// TryAdvisoryLock attempts to acquire an advisory lock for the given key without blocking.
	TryAdvisoryLock(ctx context.Context, key string) (bool, error)
	// AdvisoryUnlock releases the advisory lock for the given key.
	AdvisoryUnlock(ctx context.Context, key string) (bool, error)
}

// Client defines database interaction contracts for health checks and queries.
type Client interface {
	Ping(ctx context.Context) error
	Close() error
}
