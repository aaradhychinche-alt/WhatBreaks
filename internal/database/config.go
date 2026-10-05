package database

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/aaradhychinche-alt/WhatBreaks/internal/config"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// DefaultMaxUses matches infrastructure/database/database.js line 47.
	DefaultMaxUses int64 = 7500
)

// PoolOptions configures PostgreSQL connection pool parameters.
type PoolOptions struct {
	MaxUses int64
}

// PoolOption defines a functional option for customizing pool configuration.
type PoolOption func(*PoolOptions)

// WithMaxUses configures maximum query uses before a connection is recycled.
func WithMaxUses(maxUses int64) PoolOption {
	return func(o *PoolOptions) {
		o.MaxUses = maxUses
	}
}

// BuildPgxPoolConfig maps internal/config/database.go DatabaseConfig to pgxpool.Config,
// preserving all pool sizes, timeouts, TLS settings, and maxUses recycling behavior.
func BuildPgxPoolConfig(cfg *config.DatabaseConfig, opts ...PoolOption) (*pgxpool.Config, error) {
	if cfg == nil {
		return nil, fmt.Errorf("database configuration cannot be nil")
	}

	options := PoolOptions{
		MaxUses: DefaultMaxUses,
	}
	for _, opt := range opts {
		opt(&options)
	}

	connStr := config.GetConnectionString(cfg)
	poolConfig, err := pgxpool.ParseConfig(connStr)
	if err != nil {
		return nil, fmt.Errorf("failed to parse connection string: %w", RedactError(err))
	}

	// 1. Connection pool bounds matching JS defaults
	if cfg.PoolMax > 0 {
		poolConfig.MaxConns = int32(cfg.PoolMax)
	}
	if cfg.PoolMin > 0 {
		poolConfig.MinConns = int32(cfg.PoolMin)
	}
	if cfg.PoolIdleTimeout > 0 {
		poolConfig.MaxConnIdleTime = cfg.PoolIdleTimeout
	}
	if cfg.ConnectionTimeout > 0 {
		poolConfig.ConnConfig.ConnectTimeout = cfg.ConnectionTimeout
	}

	// 2. SSL/TLS configuration matching JS semantics
	if cfg.SSL != nil && cfg.SSL.Enabled {
		tlsConfig := &tls.Config{
			ServerName: cfg.Host,
		}

		if cfg.SSL.MinVersion == "TLSv1.3" {
			tlsConfig.MinVersion = tls.VersionTLS13
		} else {
			tlsConfig.MinVersion = tls.VersionTLS12
		}

		tlsConfig.InsecureSkipVerify = !cfg.SSL.RejectUnauthorized

		if len(cfg.SSL.CACertData) > 0 {
			certPool := x509.NewCertPool()
			if certPool.AppendCertsFromPEM(cfg.SSL.CACertData) {
				tlsConfig.RootCAs = certPool
			}
		}

		poolConfig.ConnConfig.TLSConfig = tlsConfig
	}

	// 3. Track connection usage to implement maxUses (7500) recycling from JS
	if options.MaxUses > 0 {
		var mu sync.Mutex
		useCounts := make(map[*pgx.Conn]*int64)

		prevBeforeAcquire := poolConfig.BeforeAcquire
		poolConfig.BeforeAcquire = func(ctx context.Context, conn *pgx.Conn) bool {
			if prevBeforeAcquire != nil && !prevBeforeAcquire(ctx, conn) {
				return false
			}
			mu.Lock()
			ptr, ok := useCounts[conn]
			if !ok {
				var count int64 = 1
				useCounts[conn] = &count
			} else {
				atomic.AddInt64(ptr, 1)
			}
			mu.Unlock()
			return true
		}

		poolConfig.AfterRelease = func(conn *pgx.Conn) bool {
			mu.Lock()
			ptr, ok := useCounts[conn]
			mu.Unlock()
			if ok && atomic.LoadInt64(ptr) >= options.MaxUses {
				mu.Lock()
				delete(useCounts, conn)
				mu.Unlock()
				// Return false to recycle and destroy the connection
				return false
			}
			return true
		}

		poolConfig.BeforeClose = func(conn *pgx.Conn) {
			mu.Lock()
			delete(useCounts, conn)
			mu.Unlock()
		}
	}

	return poolConfig, nil
}
