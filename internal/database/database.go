package database

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/aaradhychinche-alt/WhatBreaks/internal/config"
	"github.com/aaradhychinche-alt/WhatBreaks/internal/logging"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Option configures Database runtime settings.
type Option func(*dbOptions)

type dbOptions struct {
	logger      logging.Logger
	poolOptions []PoolOption
}

// WithLogger sets the structured logger for database events and connection errors.
func WithLogger(logger logging.Logger) Option {
	return func(o *dbOptions) {
		o.logger = logger
	}
}

// WithPoolOption adds low-level pool options like MaxUses.
func WithPoolOption(opt PoolOption) Option {
	return func(o *dbOptions) {
		o.poolOptions = append(o.poolOptions, opt)
	}
}

// Database implements DB wrapping pgxpool.Pool with connection affinity,
// advisory locking, health check integration, and graceful teardown.
type Database struct {
	pool      *pgxpool.Pool
	cfg       *config.DatabaseConfig
	logger    logging.Logger
	closeOnce sync.Once
	closed    bool
	mu        sync.RWMutex
}

// New creates and initializes a PostgreSQL connection pool from DatabaseConfig.
func New(ctx context.Context, cfg *config.DatabaseConfig, opts ...Option) (*Database, error) {
	if cfg == nil {
		return nil, fmt.Errorf("database configuration cannot be nil")
	}

	options := dbOptions{
		logger: logging.NewJSONLogger(nil, logging.LevelInfo, "database"),
	}
	for _, opt := range opts {
		opt(&options)
	}

	poolCfg, err := BuildPgxPoolConfig(cfg, options.poolOptions...)
	if err != nil {
		return nil, err
	}

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize postgres connection pool: %w", RedactError(err))
	}

	return &Database{
		pool:   pool,
		cfg:    cfg,
		logger: options.logger,
	}, nil
}

// NewFromPool wraps an existing pgxpool.Pool (e.g. for testing).
func NewFromPool(pool *pgxpool.Pool, logger logging.Logger) *Database {
	if logger == nil {
		logger = logging.NewJSONLogger(nil, logging.LevelInfo, "database")
	}
	return &Database{
		pool:   pool,
		logger: logger,
	}
}

// Ping checks pool liveness directly satisfying health.DBPinger.
func (d *Database) Ping(ctx context.Context) error {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.closed || d.pool == nil {
		return ErrDatabaseClosed
	}
	return d.pool.Ping(ctx)
}

// TestConnection executes "SELECT 1" on an acquired client, matching
// infrastructure/database/database.js lines 140-163.
func (d *Database) TestConnection(ctx context.Context) error {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.closed || d.pool == nil {
		return ErrDatabaseClosed
	}

	conn, err := d.pool.Acquire(ctx)
	if err != nil {
		d.logger.Error("Database connection test failed", "error", RedactError(err).Error())
		return err
	}
	defer conn.Release()

	var one int
	if err := conn.QueryRow(ctx, "SELECT 1").Scan(&one); err != nil {
		d.logger.Error("Database connection test failed", "error", RedactError(err).Error())
		return err
	}

	d.logger.Info("Database connection test successful")
	return nil
}

// WaitForDatabase polls TestConnection until the database is ready, matching
// infrastructure/database/database.js lines 166-190.
func (d *Database) WaitForDatabase(ctx context.Context, maxAttempts int, delay ...any) error {
	if maxAttempts <= 0 {
		maxAttempts = 90
	}
	pollDelay := 2 * time.Second
	if len(delay) > 0 {
		if dur, ok := delay[0].(time.Duration); ok && dur > 0 {
			pollDelay = dur
		}
	}

	var host, port, dbName, user string
	if d.cfg != nil {
		host = d.cfg.Host
		port = fmt.Sprintf("%d", d.cfg.Port)
		dbName = d.cfg.Database
		user = d.cfg.User
	}

	d.logger.Info("Waiting for database to be ready...",
		"host", host,
		"port", port,
		"database", dbName,
		"user", user,
	)

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		d.logger.Info(fmt.Sprintf("Database connection attempt %d/%d", attempt, maxAttempts))

		if err := d.TestConnection(ctx); err == nil {
			d.logger.Info("Database is ready!")
			return nil
		}

		if attempt < maxAttempts {
			d.logger.Info(fmt.Sprintf("Waiting %dms before next attempt...", pollDelay.Milliseconds()))
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(pollDelay):
			}
		}
	}

	d.logger.Error(fmt.Sprintf("Database connection failed after %d attempts", maxAttempts))
	return ErrDatabaseNotReady
}

// Query executes a query on the pool.
func (d *Database) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.closed || d.pool == nil {
		return nil, ErrDatabaseClosed
	}
	return d.pool.Query(ctx, sql, args...)
}

// QueryRow executes a query on the pool expecting at most one row.
func (d *Database) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.closed || d.pool == nil {
		return &closedRow{}
	}
	return d.pool.QueryRow(ctx, sql, args...)
}

// Exec executes a command on the pool.
func (d *Database) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.closed || d.pool == nil {
		return pgconn.CommandTag{}, ErrDatabaseClosed
	}
	return d.pool.Exec(ctx, sql, args...)
}

// WithConnection acquires a dedicated connection, executes fn, and releases the connection in defer,
// faithfully implementing workers/runtime/db.js lines 45-52 withClient.
func (d *Database) WithConnection(ctx context.Context, fn func(ctx context.Context, conn Connection) error) error {
	d.mu.RLock()
	if d.closed || d.pool == nil {
		d.mu.RUnlock()
		return ErrDatabaseClosed
	}
	pool := d.pool
	d.mu.RUnlock()

	conn, err := pool.Acquire(ctx)
	if err != nil {
		return RedactError(err)
	}
	defer conn.Release()

	wrapper := &connWrapper{rawConn: conn}
	return fn(ctx, wrapper)
}

// Begin begins a new transaction on the pool.
func (d *Database) Begin(ctx context.Context) (Tx, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.closed || d.pool == nil {
		return nil, ErrDatabaseClosed
	}
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return nil, RedactError(err)
	}
	return &txWrapper{rawTx: tx}, nil
}

// WithTransaction runs fn inside a transaction, automatically committing on success
// and rolling back on error or panic.
func (d *Database) WithTransaction(ctx context.Context, fn func(ctx context.Context, tx Tx) error) (err error) {
	tx, err := d.Begin(ctx)
	if err != nil {
		return err
	}

	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback(ctx)
			panic(p)
		} else if err != nil {
			_ = tx.Rollback(ctx)
		}
	}()

	if err = fn(ctx, tx); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// AcquireLock acquires a PostgreSQL advisory lock on a dedicated connection, returning
// a SessionLock that guarantees connection affinity until Unlock is called.
func (d *Database) AcquireLock(ctx context.Context, key string) (SessionLock, error) {
	d.mu.RLock()
	if d.closed || d.pool == nil {
		d.mu.RUnlock()
		return nil, ErrDatabaseClosed
	}
	pool := d.pool
	d.mu.RUnlock()

	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, RedactError(err)
	}

	lockID := AdvisoryLockKey(key)
	var acquired bool
	err = conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", lockID).Scan(&acquired)
	if err != nil {
		conn.Release()
		return nil, RedactError(err)
	}

	if !acquired {
		conn.Release()
		return nil, ErrLockAcquisitionFailed
	}

	return &sessionLock{
		conn:   conn,
		key:    key,
		lockID: lockID,
	}, nil
}

// Stat returns pool statistics matching infrastructure/database/database.js metrics.
func (d *Database) Stat() PoolStat {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.pool == nil {
		return PoolStat{}
	}
	stat := d.pool.Stat()
	return PoolStat{
		TotalConns:        stat.TotalConns(),
		IdleConns:         stat.IdleConns(),
		AcquiredConns:     stat.AcquiredConns(),
		ConstructingConns: stat.ConstructingConns(),
		MaxConns:          stat.MaxConns(),
	}
}

// Close gracefully closes all connections in the pool. It is safe for concurrent
// and repeated execution (idempotent).
func (d *Database) Close() error {
	d.closeOnce.Do(func() {
		d.mu.Lock()
		d.closed = true
		pool := d.pool
		d.mu.Unlock()

		if pool != nil {
			pool.Close()
		}
	})
	return nil
}

// connWrapper implements Connection wrapping a dedicated *pgxpool.Conn.
type connWrapper struct {
	rawConn *pgxpool.Conn
}

func (c *connWrapper) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return c.rawConn.Query(ctx, sql, args...)
}

func (c *connWrapper) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return c.rawConn.QueryRow(ctx, sql, args...)
}

func (c *connWrapper) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return c.rawConn.Exec(ctx, sql, args...)
}

func (c *connWrapper) TryAdvisoryLock(ctx context.Context, key string) (bool, error) {
	lockID := AdvisoryLockKey(key)
	var acquired bool
	err := c.rawConn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", lockID).Scan(&acquired)
	if err != nil {
		return false, RedactError(err)
	}
	return acquired, nil
}

func (c *connWrapper) AdvisoryUnlock(ctx context.Context, key string) (bool, error) {
	lockID := AdvisoryLockKey(key)
	var released bool
	err := c.rawConn.QueryRow(ctx, "SELECT pg_advisory_unlock($1)", lockID).Scan(&released)
	if err != nil {
		// Matching workers/runtime/db.js: .catch(() => {})
		return false, nil
	}
	return released, nil
}

func (c *connWrapper) RawConn() *pgxpool.Conn {
	return c.rawConn
}

// txWrapper implements Tx wrapping a *pgx.Tx.
type txWrapper struct {
	rawTx pgx.Tx
}

func (t *txWrapper) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return t.rawTx.Query(ctx, sql, args...)
}

func (t *txWrapper) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return t.rawTx.QueryRow(ctx, sql, args...)
}

func (t *txWrapper) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return t.rawTx.Exec(ctx, sql, args...)
}

func (t *txWrapper) Commit(ctx context.Context) error {
	return t.rawTx.Commit(ctx)
}

func (t *txWrapper) Rollback(ctx context.Context) error {
	return t.rawTx.Rollback(ctx)
}

// sessionLock implements SessionLock holding an advisory lock on a dedicated connection.
type sessionLock struct {
	conn     *pgxpool.Conn
	key      string
	lockID   int64
	mu       sync.Mutex
	unlocked bool
}

func (s *sessionLock) Key() string {
	return s.key
}

func (s *sessionLock) LockID() int64 {
	return s.lockID
}

func (s *sessionLock) Unlock(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.unlocked || s.conn == nil {
		return nil
	}
	s.unlocked = true
	defer s.conn.Release()

	var released bool
	_ = s.conn.QueryRow(ctx, "SELECT pg_advisory_unlock($1)", s.lockID).Scan(&released)
	return nil
}

type closedRow struct{}

func (c *closedRow) Scan(dest ...any) error {
	return ErrDatabaseClosed
}
