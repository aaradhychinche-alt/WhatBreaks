package database

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AdvisoryLocker provides application-level concurrency control via PostgreSQL advisory locks.
// Maintained for backward compatibility with Step 5A scaffolding.
type AdvisoryLocker interface {
	TryAdvisoryLock(ctx context.Context, key string) (bool, error)
	AdvisoryUnlock(ctx context.Context, key string) (bool, error)
}

// Client defines database interaction contracts for health checks and queries.
// Maintained for backward compatibility with Step 5A scaffolding and health check integration.
type Client interface {
	Ping(ctx context.Context) error
	Close() error
}

// PoolStat provides observability into connection pool metrics matching
// infrastructure/database/database.js lines 55-66.
type PoolStat struct {
	TotalConns        int32
	IdleConns         int32
	AcquiredConns     int32
	ConstructingConns int32
	MaxConns          int32
}

// DB represents the full PostgreSQL pool runtime interface.
type DB interface {
	Client
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	WithConnection(ctx context.Context, fn func(ctx context.Context, conn Connection) error) error
	Begin(ctx context.Context) (Tx, error)
	WithTransaction(ctx context.Context, fn func(ctx context.Context, tx Tx) error) error
	AcquireLock(ctx context.Context, key string) (SessionLock, error)
	TestConnection(ctx context.Context) error
	WaitForDatabase(ctx context.Context, maxAttempts int, delay ...any) error
	Stat() PoolStat
}

// Connection represents a dedicated pooled connection session, guaranteeing
// connection affinity for advisory locks and session-scoped operations.
type Connection interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	TryAdvisoryLock(ctx context.Context, key string) (bool, error)
	AdvisoryUnlock(ctx context.Context, key string) (bool, error)
	RawConn() *pgxpool.Conn
}

// Tx defines the transaction interface for atomic database operations.
type Tx interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Commit(ctx context.Context) error
	Rollback(ctx context.Context) error
}

// SessionLock holds a dedicated PostgreSQL connection while an advisory lock is held,
// ensuring strict connection affinity until explicitly released.
type SessionLock interface {
	Key() string
	LockID() int64
	Unlock(ctx context.Context) error
}
