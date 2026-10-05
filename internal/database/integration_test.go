package database

import (
	"context"
	"errors"
	"net"
	"os"
	"testing"
	"time"

	"github.com/aaradhychinche-alt/WhatBreaks/internal/config"
	"github.com/aaradhychinche-alt/WhatBreaks/internal/logging"
)

// getTestDatabaseConfig returns a test database configuration or skips the test if unavailable.
func getTestDatabaseConfig(t *testing.T) *config.DatabaseConfig {
	t.Helper()

	host := os.Getenv("TEST_DB_HOST")
	if host == "" {
		host = "127.0.0.1"
	}
	port := 5432

	// Check if port is open before attempting to connect
	timeout := 200 * time.Millisecond
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, "5432"), timeout)
	if err != nil {
		t.Skipf("PostgreSQL is not reachable at %s:%d (%v); skipping integration test", host, port, err)
	}
	_ = conn.Close()

	user := os.Getenv("TEST_DB_USER")
	if user == "" {
		user = "postgres"
	}
	dbName := os.Getenv("TEST_DB_NAME")
	if dbName == "" {
		dbName = "postgres"
	}
	password := os.Getenv("TEST_DB_PASSWORD")

	return &config.DatabaseConfig{
		Host:              host,
		Port:              port,
		Database:          dbName,
		User:              user,
		Password:          password,
		PoolMax:           5,
		PoolMin:           1,
		PoolIdleTimeout:   10 * time.Second,
		ConnectionTimeout: 2 * time.Second,
	}
}

func TestIntegration_RealPostgreSQL(t *testing.T) {
	cfg := getTestDatabaseConfig(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	logger := logging.NewJSONLogger(nil, logging.LevelDebug, "db-integration-test")
	db, err := New(ctx, cfg, WithLogger(logger))
	if err != nil {
		t.Skipf("Failed to initialize database pool: %v; skipping", err)
	}
	defer func() {
		_ = db.Close()
	}()

	// 1. Ping & TestConnection
	if err := db.Ping(ctx); err != nil {
		t.Skipf("PostgreSQL Ping failed: %v; skipping integration tests", err)
	}
	if err := db.TestConnection(ctx); err != nil {
		t.Fatalf("TestConnection failed: %v", err)
	}

	// 2. Query
	rows, err := db.Query(ctx, "SELECT $1::text AS greeting, $2::int AS answer", "hello", 42)
	if err != nil {
		t.Fatalf("Query failed: %v", err)
	}
	defer rows.Close()

	if !rows.Next() {
		t.Fatalf("expected at least one row")
	}
	var greeting string
	var answer int
	if err := rows.Scan(&greeting, &answer); err != nil {
		t.Fatalf("Scan failed: %v", err)
	}
	if greeting != "hello" || answer != 42 {
		t.Fatalf("unexpected query result: %s, %d", greeting, answer)
	}

	// 3. Transactions
	t.Run("TransactionCommit", func(t *testing.T) {
		err := db.WithTransaction(ctx, func(ctx context.Context, tx Tx) error {
			var res int
			return tx.QueryRow(ctx, "SELECT 100").Scan(&res)
		})
		if err != nil {
			t.Fatalf("WithTransaction commit failed: %v", err)
		}
	})

	t.Run("TransactionRollback", func(t *testing.T) {
		customErr := errors.New("aborted intentionally")
		err := db.WithTransaction(ctx, func(ctx context.Context, tx Tx) error {
			return customErr
		})
		if !errors.Is(err, customErr) {
			t.Fatalf("expected %v, got %v", customErr, err)
		}
	})

	// 4. Advisory Locks with Connection Affinity
	t.Run("AdvisoryLock_ConnectionAffinityAndContention", func(t *testing.T) {
		lockKey := "whatbreaks:integration:test-lock"

		// Connection 1 acquires lock
		lock1, err := db.AcquireLock(ctx, lockKey)
		if err != nil {
			t.Fatalf("failed to acquire first advisory lock: %v", err)
		}

		// Connection 2 attempts to acquire the same lock; MUST fail with ErrLockAcquisitionFailed
		_, err = db.AcquireLock(ctx, lockKey)
		if !errors.Is(err, ErrLockAcquisitionFailed) {
			t.Fatalf("expected ErrLockAcquisitionFailed on contested lock, got %v", err)
		}

		// Connection 1 releases lock
		if err := lock1.Unlock(ctx); err != nil {
			t.Fatalf("failed to unlock: %v", err)
		}

		// Connection 2 now successfully acquires lock
		lock2, err := db.AcquireLock(ctx, lockKey)
		if err != nil {
			t.Fatalf("failed to acquire lock after release: %v", err)
		}
		_ = lock2.Unlock(ctx)
	})
}
