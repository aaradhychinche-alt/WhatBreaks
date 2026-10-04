package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
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

// ParseLevel parses a level string (case-insensitive) into Level, defaulting to LevelInfo.
func ParseLevel(val string) Level {
	switch strings.ToLower(strings.TrimSpace(val)) {
	case "debug":
		return LevelDebug
	case "info":
		return LevelInfo
	case "warn", "warning":
		return LevelWarn
	case "error":
		return LevelError
	default:
		return LevelInfo
	}
}

// Scrubber defines the contract for stripping secrets, credentials, and cryptographic keys.
type Scrubber interface {
	ScrubText(input string) string
	RedactKey(key string) bool
}

// DefaultScrubber implements Scrubber using the central secret detection patterns.
type DefaultScrubber struct{}

func (s DefaultScrubber) ScrubText(input string) string {
	return ScrubLogString(input)
}

func (s DefaultScrubber) RedactKey(key string) bool {
	return IsSensitiveKey(key)
}

// Logger defines the structured platform logging contract.
type Logger interface {
	Debug(msg string, keysAndValues ...any)
	Info(msg string, keysAndValues ...any)
	Warn(msg string, keysAndValues ...any)
	Error(msg string, keysAndValues ...any)
	With(keysAndValues ...any) Logger
	Named(service string) Logger
	Level() Level
}

// JSONLogger provides concurrency-safe structured JSON logging with automatic secret scrubbing.
type JSONLogger struct {
	mu          sync.Mutex
	w           io.Writer
	level       Level
	service     string
	component   string
	extraFields map[string]any
	now         func() time.Time
}

// NewJSONLogger creates a JSONLogger emitting deterministic JSON lines to writer w.
func NewJSONLogger(w io.Writer, level Level, service string) *JSONLogger {
	if w == nil {
		w = os.Stderr
	}
	if service == "" {
		service = "whatbreaks"
	}
	return &JSONLogger{
		w:           w,
		level:       level,
		service:     service,
		extraFields: make(map[string]any),
		now:         time.Now,
	}
}

// NewStandardLogger creates a JSONLogger for backward compatibility with Step 5A.
func NewStandardLogger(w io.Writer, level Level) *JSONLogger {
	return NewJSONLogger(w, level, "whatbreaks")
}

func (l *JSONLogger) Level() Level {
	return l.level
}

func (l *JSONLogger) Named(service string) Logger {
	l.mu.Lock()
	defer l.mu.Unlock()

	newLogger := &JSONLogger{
		w:           l.w,
		level:       l.level,
		service:     service,
		component:   l.component,
		extraFields: make(map[string]any, len(l.extraFields)),
		now:         l.now,
	}
	for k, v := range l.extraFields {
		newLogger.extraFields[k] = v
	}
	return newLogger
}

func (l *JSONLogger) With(keysAndValues ...any) Logger {
	l.mu.Lock()
	defer l.mu.Unlock()

	newLogger := &JSONLogger{
		w:           l.w,
		level:       l.level,
		service:     l.service,
		component:   l.component,
		extraFields: make(map[string]any, len(l.extraFields)+len(keysAndValues)/2),
		now:         l.now,
	}
	for k, v := range l.extraFields {
		newLogger.extraFields[k] = v
	}

	for i := 0; i < len(keysAndValues); i += 2 {
		key := fmt.Sprintf("%v", keysAndValues[i])
		if key == "service" {
			if s, ok := keysAndValues[i+1].(string); ok && s != "" {
				newLogger.service = s
				continue
			}
		}
		if key == "component" {
			if c, ok := keysAndValues[i+1].(string); ok {
				newLogger.component = c
				continue
			}
		}
		if i+1 < len(keysAndValues) {
			newLogger.extraFields[key] = keysAndValues[i+1]
		} else {
			newLogger.extraFields[key] = nil
		}
	}

	return newLogger
}

func (l *JSONLogger) Debug(msg string, keysAndValues ...any) {
	if l.level <= LevelDebug {
		l.log(LevelDebug, msg, keysAndValues...)
	}
}

func (l *JSONLogger) Info(msg string, keysAndValues ...any) {
	if l.level <= LevelInfo {
		l.log(LevelInfo, msg, keysAndValues...)
	}
}

func (l *JSONLogger) Warn(msg string, keysAndValues ...any) {
	if l.level <= LevelWarn {
		l.log(LevelWarn, msg, keysAndValues...)
	}
}

func (l *JSONLogger) Error(msg string, keysAndValues ...any) {
	if l.level <= LevelError {
		l.log(LevelError, msg, keysAndValues...)
	}
}

func (l *JSONLogger) log(level Level, msg string, keysAndValues ...any) {
	rawRecord := make(map[string]any, len(l.extraFields)+len(keysAndValues)/2+4)
	for k, v := range l.extraFields {
		rawRecord[k] = v
	}
	for i := 0; i < len(keysAndValues); i += 2 {
		key := fmt.Sprintf("%v", keysAndValues[i])
		if i+1 < len(keysAndValues) {
			rawRecord[key] = keysAndValues[i+1]
		} else {
			rawRecord[key] = nil
		}
	}

	rawRecord["level"] = level.String()
	rawRecord["message"] = msg
	rawRecord["service"] = l.service
	rawRecord["timestamp"] = l.now().UTC().Format(time.RFC3339Nano)
	if l.component != "" {
		rawRecord["component"] = l.component
	}

	// Sanitize through central scrubber
	sanitized := SanitizeLogRecord(rawRecord)

	// Format deterministically in LOG_FIELD_ORDER: level, message, service, timestamp, then sorted keys
	formattedLine := formatOrderedJSONLine(sanitized)

	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = l.w.Write(formattedLine)
}

func formatOrderedJSONLine(record map[string]any) []byte {
	var buf bytes.Buffer
	buf.WriteByte('{')

	primaryOrder := []string{"level", "message", "service", "timestamp"}
	first := true

	for _, k := range primaryOrder {
		if val, exists := record[k]; exists {
			if !first {
				buf.WriteByte(',')
			}
			kBytes, _ := json.Marshal(k)
			vBytes, _ := json.Marshal(val)
			buf.Write(kBytes)
			buf.WriteByte(':')
			buf.Write(vBytes)
			first = false
		}
	}

	var remainingKeys []string
	for k := range record {
		isPrimary := false
		for _, pk := range primaryOrder {
			if k == pk {
				isPrimary = true
				break
			}
		}
		if !isPrimary {
			remainingKeys = append(remainingKeys, k)
		}
	}
	sort.Strings(remainingKeys)

	for _, k := range remainingKeys {
		if !first {
			buf.WriteByte(',')
		}
		kBytes, _ := json.Marshal(k)
		vBytes, _ := json.Marshal(record[k])
		buf.Write(kBytes)
		buf.WriteByte(':')
		buf.Write(vBytes)
		first = false
	}

	buf.WriteByte('}')
	buf.WriteByte('\n')
	return buf.Bytes()
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
	return NewJSONLogger(os.Stderr, LevelInfo, "whatbreaks")
}
