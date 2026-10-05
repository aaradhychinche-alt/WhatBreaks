package database

import (
	"errors"
	"net"
	"regexp"
	"strings"
	"syscall"

	"github.com/jackc/pgx/v5/pgconn"
)

var (
	// ErrLockAcquisitionFailed is returned when an advisory lock cannot be acquired immediately.
	ErrLockAcquisitionFailed = errors.New("database: failed to acquire advisory lock")
	// ErrDatabaseNotReady is returned when connection checks fail to reach a healthy database state.
	ErrDatabaseNotReady = errors.New("database: database connection is not ready")
	// ErrDatabaseClosed is returned when operations are attempted on a closed database pool.
	ErrDatabaseClosed = errors.New("database: database pool is closed")
	// ErrTransactionRolledBack is returned when a transaction was aborted and rolled back.
	ErrTransactionRolledBack = errors.New("database: transaction rolled back")
)

var dsnPasswordRegex = regexp.MustCompile(`(?i)(postgres(?:ql)?://[^:]+:)([^@]+)(@)`)

// RedactError returns an error with any embedded database credentials or passwords scrubbed.
func RedactError(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	if dsnPasswordRegex.MatchString(msg) {
		redacted := dsnPasswordRegex.ReplaceAllString(msg, "${1}[REDACTED]${3}")
		return errors.New(redacted)
	}
	return err
}

// IsConnectionError determines whether an error is a fatal or transient PostgreSQL connection loss,
// faithfully implementing infrastructure/database/database.js lines 93-100:
// - error codes: ECONNREFUSED, ECONNRESET, ENOTFOUND, ETIMEDOUT
// - SQLSTATE starting with "08" (Class 08 — Connection Exception)
// - message contains "connection terminated" or "could not connect"
func IsConnectionError(err error) bool {
	if err == nil {
		return false
	}

	// 1. Check pgconn.PgError SQLSTATE
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		if strings.HasPrefix(pgErr.Code, "08") {
			return true
		}
	}

	// 2. Check standard network and system errors
	var netOpErr *net.OpError
	if errors.As(err, &netOpErr) {
		return true
	}
	if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ETIMEDOUT) {
		return true
	}

	// 3. Check message strings matching JS implementation
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "connection terminated") ||
		strings.Contains(msg, "could not connect") ||
		strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "broken pipe") ||
		strings.Contains(msg, "econnrefused") ||
		strings.Contains(msg, "econnreset") ||
		strings.Contains(msg, "enotfound") ||
		strings.Contains(msg, "etimedout") {
		return true
	}

	return false
}
