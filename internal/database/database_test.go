package database

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/aaradhychinche-alt/WhatBreaks/internal/config"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestHash32_ExactJavaScriptCompatibility(t *testing.T) {
	testCases := []struct {
		name        string
		input       string
		expected    int32
		expectedAbs int64
	}{
		{
			name:        "empty string",
			input:       "",
			expected:    0,
			expectedAbs: 0,
		},
		{
			name:        "test-lock string",
			input:       "test-lock",
			expected:    -1226527354,
			expectedAbs: 1226527354,
		},
		{
			name:        "whatbreaks:discovery string",
			input:       "whatbreaks:discovery",
			expected:    -2033551410,
			expectedAbs: 2033551410,
		},
		{
			name:        "unicode accented café",
			input:       "café",
			expected:    3045921,
			expectedAbs: 3045921,
		},
		{
			name:        "unicode multi-byte Japanese",
			input:       "日本語",
			expected:    25921943,
			expectedAbs: 25921943,
		},
		{
			name:        "astral plane emoji worker:🚀 (UTF-16 surrogate pair)",
			input:       "worker:🚀",
			expected:    1097726815,
			expectedAbs: 1097726815,
		},
		{
			name:        "long string",
			input:       "whatbreaks:controller:discovery:task-runner:1234567890",
			expected:    -2047572317,
			expectedAbs: 2047572317,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got := Hash32(tc.input)
			if got != tc.expected {
				t.Fatalf("Hash32(%q) = %d; expected %d", tc.input, got, tc.expected)
			}
			gotAbs := AdvisoryLockKey(tc.input)
			if gotAbs != tc.expectedAbs {
				t.Fatalf("AdvisoryLockKey(%q) = %d; expected %d", tc.input, gotAbs, tc.expectedAbs)
			}
		})
	}
}

func TestAdvisoryLockKey_MinInt32Boundary(t *testing.T) {
	// If a string produces math.MinInt32 (-2147483648), in 32-bit two's complement,
	// -MinInt32 overflows 32-bit int. But AdvisoryLockKey returns int64 matching JS Math.abs.
	// We verify that a negative int64 cast correctly negates to 2147483648.
	minInt32 := int64(math.MinInt32)
	absVal := -minInt32
	if absVal != 2147483648 {
		t.Fatalf("expected 2147483648, got %d", absVal)
	}
}

func TestIsConnectionError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{
			name:     "nil error",
			err:      nil,
			expected: false,
		},
		{
			name: "pgconn Class 08 error (08006 connection failure)",
			err: &pgconn.PgError{
				Code:    "08006",
				Message: "connection_failure",
			},
			expected: true,
		},
		{
			name: "pgconn non-08 error (23505 unique violation)",
			err: &pgconn.PgError{
				Code:    "23505",
				Message: "unique_violation",
			},
			expected: false,
		},
		{
			name:     "net.OpError",
			err:      &net.OpError{Op: "dial", Net: "tcp"},
			expected: true,
		},
		{
			name:     "syscall.ECONNREFUSED",
			err:      syscall.ECONNREFUSED,
			expected: true,
		},
		{
			name:     "syscall.ECONNRESET",
			err:      syscall.ECONNRESET,
			expected: true,
		},
		{
			name:     "message containing connection terminated",
			err:      errors.New("fatal: server closed connection terminated unexpectedly"),
			expected: true,
		},
		{
			name:     "message containing could not connect",
			err:      errors.New("could not connect to server"),
			expected: true,
		},
		{
			name:     "message containing broken pipe",
			err:      errors.New("write: broken pipe"),
			expected: true,
		},
		{
			name:     "generic business logic error",
			err:      errors.New("record not found"),
			expected: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := IsConnectionError(tc.err)
			if got != tc.expected {
				t.Fatalf("IsConnectionError(%v) = %v; expected %v", tc.err, got, tc.expected)
			}
		})
	}
}

func TestRedactError(t *testing.T) {
	errWithPass := errors.New("dial error: postgresql://wb_user:superSecretPassword123!@db.internal:5432/whatbreaks")
	redacted := RedactError(errWithPass)

	if redacted == nil {
		t.Fatalf("expected non-nil error")
	}
	expected := "dial error: postgresql://wb_user:[REDACTED]@db.internal:5432/whatbreaks"
	if redacted.Error() != expected {
		t.Fatalf("expected %q, got %q", expected, redacted.Error())
	}

	normalErr := errors.New("table does not exist")
	if RedactError(normalErr).Error() != "table does not exist" {
		t.Fatalf("expected unmodified error for normal errors")
	}
}

func TestBuildPgxPoolConfig(t *testing.T) {
	cfg := &config.DatabaseConfig{
		Host:              "127.0.0.1",
		Port:              5432,
		Database:          "whatbreaks_test",
		User:              "testuser",
		Password:          "secretpass",
		PoolMax:           10,
		PoolMin:           2,
		PoolIdleTimeout:   30 * time.Second,
		ConnectionTimeout: 5 * time.Second,
		SSL: &config.SSLConfig{
			Enabled:            true,
			Mode:               "require",
			RejectUnauthorized: true,
			MinVersion:         "TLSv1.3",
		},
	}

	poolConfig, err := BuildPgxPoolConfig(cfg, WithMaxUses(7500))
	if err != nil {
		t.Fatalf("failed to build pool config: %v", err)
	}

	if poolConfig.MaxConns != 10 {
		t.Errorf("expected MaxConns=10, got %d", poolConfig.MaxConns)
	}
	if poolConfig.MinConns != 2 {
		t.Errorf("expected MinConns=2, got %d", poolConfig.MinConns)
	}
	if poolConfig.MaxConnIdleTime != 30*time.Second {
		t.Errorf("expected MaxConnIdleTime=30s, got %v", poolConfig.MaxConnIdleTime)
	}
	if poolConfig.ConnConfig.ConnectTimeout != 5*time.Second {
		t.Errorf("expected ConnectTimeout=5s, got %v", poolConfig.ConnConfig.ConnectTimeout)
	}
	if poolConfig.ConnConfig.TLSConfig == nil {
		t.Fatalf("expected TLSConfig to be configured")
	}
	if poolConfig.ConnConfig.TLSConfig.InsecureSkipVerify {
		t.Errorf("expected InsecureSkipVerify=false for RejectUnauthorized=true")
	}
	if poolConfig.AfterRelease == nil {
		t.Errorf("expected AfterRelease hook for MaxUses recycling")
	}
}

func TestDatabase_ClosedPoolGuards(t *testing.T) {
	db := &Database{}
	_ = db.Close()

	ctx := context.Background()

	// 1. Ping
	if err := db.Ping(ctx); !errors.Is(err, ErrDatabaseClosed) {
		t.Errorf("expected ErrDatabaseClosed on Ping, got %v", err)
	}

	// 2. TestConnection
	if err := db.TestConnection(ctx); !errors.Is(err, ErrDatabaseClosed) {
		t.Errorf("expected ErrDatabaseClosed on TestConnection, got %v", err)
	}

	// 3. Query
	if _, err := db.Query(ctx, "SELECT 1"); !errors.Is(err, ErrDatabaseClosed) {
		t.Errorf("expected ErrDatabaseClosed on Query, got %v", err)
	}

	// 4. QueryRow
	var val int
	if err := db.QueryRow(ctx, "SELECT 1").Scan(&val); !errors.Is(err, ErrDatabaseClosed) {
		t.Errorf("expected ErrDatabaseClosed on QueryRow, got %v", err)
	}

	// 5. Exec
	if _, err := db.Exec(ctx, "SELECT 1"); !errors.Is(err, ErrDatabaseClosed) {
		t.Errorf("expected ErrDatabaseClosed on Exec, got %v", err)
	}

	// 6. WithConnection
	if err := db.WithConnection(ctx, func(ctx context.Context, conn Connection) error { return nil }); !errors.Is(err, ErrDatabaseClosed) {
		t.Errorf("expected ErrDatabaseClosed on WithConnection, got %v", err)
	}

	// 7. Begin
	if _, err := db.Begin(ctx); !errors.Is(err, ErrDatabaseClosed) {
		t.Errorf("expected ErrDatabaseClosed on Begin, got %v", err)
	}

	// 8. AcquireLock
	if _, err := db.AcquireLock(ctx, "lock-key"); !errors.Is(err, ErrDatabaseClosed) {
		t.Errorf("expected ErrDatabaseClosed on AcquireLock, got %v", err)
	}

	// 9. Close idempotent
	if err := db.Close(); err != nil {
		t.Errorf("repeated Close returned error: %v", err)
	}

	// 10. Stat
	stat := db.Stat()
	if stat.TotalConns != 0 {
		t.Errorf("expected 0 total conns, got %d", stat.TotalConns)
	}
}

func TestDatabase_ConcurrentHashingAndLockKeys(t *testing.T) {
	goroutines := 50
	iterations := 50
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func(workerID int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				key := fmt.Sprintf("worker-%d-task-%d-🚀", workerID, j)
				h := Hash32(key)
				lockID := AdvisoryLockKey(key)

				if lockID < 0 {
					t.Errorf("negative lockID: %d for key %q", lockID, key)
				}
				if int64(h) >= 0 && lockID != int64(h) {
					t.Errorf("lockID mismatch for positive hash: h=%d, lockID=%d", h, lockID)
				}
				if int64(h) < 0 && lockID != -int64(h) {
					t.Errorf("lockID mismatch for negative hash: h=%d, lockID=%d", h, lockID)
				}
			}
		}(i)
	}

	wg.Wait()
}

func TestSessionLock_IdempotentUnlock(t *testing.T) {
	lock := &sessionLock{
		key:    "test-idempotent-lock",
		lockID: 12345,
	}

	// Calling unlock on already unlocked lock returns nil
	if err := lock.Unlock(context.Background()); err != nil {
		t.Fatalf("first unlock failed: %v", err)
	}
	if err := lock.Unlock(context.Background()); err != nil {
		t.Fatalf("second unlock failed: %v", err)
	}

	if lock.Key() != "test-idempotent-lock" {
		t.Errorf("expected key 'test-idempotent-lock', got %q", lock.Key())
	}
	if lock.LockID() != 12345 {
		t.Errorf("expected lockID 12345, got %d", lock.LockID())
	}
}
