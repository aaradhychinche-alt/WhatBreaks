package logging

import (
	"context"
	"io"
	"log"
	"os"
)

// Level represents the severity level of a log entry.
type Level int

const (
	LevelDebug Level = iota
	LevelInfo
	LevelWarn
	LevelError
)

func (l Level) String() string {
	switch l {
	case LevelDebug:
		return "debug"
	case LevelInfo:
		return "info"
	case LevelWarn:
		return "warn"
	case LevelError:
		return "error"
	default:
		return "unknown"
	}
}

// Scrubber defines the contract for stripping secrets, credentials, and cryptographic keys.
type Scrubber interface {
	// ScrubText strips sensitive patterns (e.g. PEM private keys, tokens) from freeform text.
	ScrubText(input string) string
	// RedactKey reports whether a metadata key is sensitive and must have its value redacted.
	RedactKey(key string) bool
}

// Logger defines the platform logging contract.
type Logger interface {
	Debug(msg string, keysAndValues ...any)
	Info(msg string, keysAndValues ...any)
	Warn(msg string, keysAndValues ...any)
	Error(msg string, keysAndValues ...any)
	With(keysAndValues ...any) Logger
}

// StandardLogger is a minimal, thread-safe logger fulfilling Logger for the platform skeleton.
type StandardLogger struct {
	stdLogger *log.Logger
	level     Level
}

// NewStandardLogger creates a simple logger writing to the provided writer.
func NewStandardLogger(w io.Writer, level Level) *StandardLogger {
	if w == nil {
		w = os.Stderr
	}
	return &StandardLogger{
		stdLogger: log.New(w, "[wb] ", log.LstdFlags),
		level:     level,
	}
}

func (l *StandardLogger) Debug(msg string, keysAndValues ...any) {
	if l.level <= LevelDebug {
		l.stdLogger.Printf("DEBUG: %s", msg)
	}
}

func (l *StandardLogger) Info(msg string, keysAndValues ...any) {
	if l.level <= LevelInfo {
		l.stdLogger.Printf("INFO: %s", msg)
	}
}

func (l *StandardLogger) Warn(msg string, keysAndValues ...any) {
	if l.level <= LevelWarn {
		l.stdLogger.Printf("WARN: %s", msg)
	}
}

func (l *StandardLogger) Error(msg string, keysAndValues ...any) {
	if l.level <= LevelError {
		l.stdLogger.Printf("ERROR: %s", msg)
	}
}

func (l *StandardLogger) With(keysAndValues ...any) Logger {
	return l
}

type contextKey struct{}

// WithContext stores a logger in a context.
func WithContext(ctx context.Context, logger Logger) context.Context {
	return context.WithValue(ctx, contextKey{}, logger)
}

// FromContext retrieves a logger from context, returning a default stderr logger if absent.
func FromContext(ctx context.Context) Logger {
	if l, ok := ctx.Value(contextKey{}).(Logger); ok && l != nil {
		return l
	}
	return NewStandardLogger(os.Stderr, LevelInfo)
}
