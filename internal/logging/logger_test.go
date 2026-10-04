package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestLogger_LogLevelsAndFiltering(t *testing.T) {
	// ParseLevel tests
	if ParseLevel("debug") != LevelDebug {
		t.Errorf("expected LevelDebug, got %v", ParseLevel("debug"))
	}
	if ParseLevel("DEBUG") != LevelDebug {
		t.Errorf("expected LevelDebug, got %v", ParseLevel("DEBUG"))
	}
	if ParseLevel("info") != LevelInfo {
		t.Errorf("expected LevelInfo, got %v", ParseLevel("info"))
	}
	if ParseLevel("warn") != LevelWarn {
		t.Errorf("expected LevelWarn, got %v", ParseLevel("warn"))
	}
	if ParseLevel("warning") != LevelWarn {
		t.Errorf("expected LevelWarn, got %v", ParseLevel("warning"))
	}
	if ParseLevel("error") != LevelError {
		t.Errorf("expected LevelError, got %v", ParseLevel("error"))
	}
	if ParseLevel("invalid-level") != LevelInfo {
		t.Errorf("expected fallback to LevelInfo, got %v", ParseLevel("invalid-level"))
	}

	// Filtering test
	var buf bytes.Buffer
	logger := NewJSONLogger(&buf, LevelWarn, "test-service")

	logger.Debug("debug message")
	logger.Info("info message")
	if buf.Len() != 0 {
		t.Errorf("expected no output for debug and info when level is Warn, got: %s", buf.String())
	}

	logger.Warn("warn message")
	if !strings.Contains(buf.String(), "warn message") {
		t.Errorf("expected warn message in output, got: %s", buf.String())
	}

	buf.Reset()
	logger.Error("error message")
	if !strings.Contains(buf.String(), "error message") {
		t.Errorf("expected error message in output, got: %s", buf.String())
	}
}

func TestLogger_DeterministicFieldOrderAndDefaults(t *testing.T) {
	var buf bytes.Buffer
	fixedTime := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	logger := NewJSONLogger(&buf, LevelDebug, "")
	logger.now = func() time.Time { return fixedTime }

	// Default service name is "whatbreaks"
	if logger.service != "whatbreaks" {
		t.Errorf("expected default service whatbreaks, got %s", logger.service)
	}

	logger.Info("hello world", "zebra", "stripe", "apple", "fruit", "component", "discovery")

	line := buf.String()
	if !strings.HasSuffix(line, "\n") {
		t.Fatalf("expected newline termination, got: %s", line)
	}

	// Verify primary field order in JSON line
	levelIdx := strings.Index(line, `"level":"info"`)
	msgIdx := strings.Index(line, `"message":"hello world"`)
	serviceIdx := strings.Index(line, `"service":"whatbreaks"`)
	timestampIdx := strings.Index(line, `"timestamp":"2026-10-05T12:00:00Z"`)

	if levelIdx == -1 || msgIdx == -1 || serviceIdx == -1 || timestampIdx == -1 {
		t.Fatalf("missing required primary fields in log: %s", line)
	}
	if !(levelIdx < msgIdx && msgIdx < serviceIdx && serviceIdx < timestampIdx) {
		t.Fatalf("fields not in primary order: levelIdx=%d, msgIdx=%d, serviceIdx=%d, timestampIdx=%d",
			levelIdx, msgIdx, serviceIdx, timestampIdx)
	}

	// Verify remaining keys are sorted alphabetically: "apple", "component", "zebra"
	appleIdx := strings.Index(line, `"apple":"fruit"`)
	componentIdx := strings.Index(line, `"component":"discovery"`)
	zebraIdx := strings.Index(line, `"zebra":"stripe"`)

	if !(timestampIdx < appleIdx && appleIdx < componentIdx && componentIdx < zebraIdx) {
		t.Fatalf("secondary fields not sorted alphabetically: timestamp=%d, apple=%d, comp=%d, zebra=%d in %s",
			timestampIdx, appleIdx, componentIdx, zebraIdx, line)
	}

	// Verify JSON is fully valid and parseable
	var parsed map[string]any
	if err := json.Unmarshal([]byte(line), &parsed); err != nil {
		t.Fatalf("failed to unmarshal log line JSON: %v", err)
	}
	if parsed["level"] != "info" || parsed["message"] != "hello world" || parsed["service"] != "whatbreaks" {
		t.Fatalf("parsed json mismatch: %+v", parsed)
	}
}

func TestLogger_WithAndNamed(t *testing.T) {
	var buf bytes.Buffer
	baseLogger := NewJSONLogger(&buf, LevelInfo, "base-svc")

	// Named creates logger with new service identity
	namedLogger := baseLogger.Named("worker-svc")
	namedLogger.Info("worker started")

	var parsed map[string]any
	if err := json.Unmarshal(buf.Bytes(), &parsed); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if parsed["service"] != "worker-svc" {
		t.Errorf("expected service worker-svc, got %v", parsed["service"])
	}

	// With adds persistent context attributes
	buf.Reset()
	ctxLogger := baseLogger.With("traceId", "tr-12345", "component", "pipeline")
	ctxLogger.Info("processing step")

	parsed = nil
	if err := json.Unmarshal(buf.Bytes(), &parsed); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if parsed["traceId"] != "tr-12345" {
		t.Errorf("expected traceId tr-12345, got %v", parsed["traceId"])
	}
	if parsed["component"] != "pipeline" {
		t.Errorf("expected component pipeline, got %v", parsed["component"])
	}
}

func TestLogger_SecurityInvariants_NoSecretsInLogs(t *testing.T) {
	var buf bytes.Buffer
	logger := NewJSONLogger(&buf, LevelDebug, "security-test")

	// 1. Secret embedded in message
	rawPassword := "superSecretPassword123!"
	rawBearer := "eyJhGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.doNotLeakThis"
	logger.Info("Login attempt with password=" + rawPassword + " and Authorization: Bearer " + rawBearer)

	out1 := buf.String()
	if strings.Contains(out1, rawPassword) {
		t.Fatalf("CRITICAL SECURITY VIOLATION: raw password leaked in log output: %s", out1)
	}
	if strings.Contains(out1, rawBearer) {
		t.Fatalf("CRITICAL SECURITY VIOLATION: raw bearer token leaked in log output: %s", out1)
	}
	if !strings.Contains(out1, "[REDACTED]") {
		t.Errorf("expected [REDACTED] in output, got: %s", out1)
	}

	// 2. Secret in metadata keys
	buf.Reset()
	logger.Info("Request metadata",
		"token", "secret-token-abc-999",
		"api_key", "secret-api-key-456",
		"cookie", "session=active-session-id",
		"normalField", "harmless",
	)

	out2 := buf.String()
	for _, secret := range []string{"secret-token-abc-999", "secret-api-key-456", "session=active-session-id"} {
		if strings.Contains(out2, secret) {
			t.Fatalf("CRITICAL SECURITY VIOLATION: secret metadata %q leaked in log: %s", secret, out2)
		}
	}

	// 3. Private Key Material in log message or metadata
	buf.Reset()
	logger.Error("Failed to initialize cryptographic provider",
		"privateKey", testRsaPrivateKeyPEM,
		"rawPem", testEcPrivateKeyPEM,
		"err", errors.New("cannot read key: "+testPkcs8PrivateKeyPEM),
	)

	out3 := buf.String()
	for _, secretFrag := range []string{"MIIEowIBAAKCAQEA0Y1", "MHcCAQEEI", "MIIEvgIBADANBgkqhkiG9w0BAQEFAASCBKgwggSkAgEAAoIBAQD"} {
		if strings.Contains(out3, secretFrag) {
			t.Fatalf("CRITICAL SECURITY VIOLATION: private key material %q leaked in log: %s", secretFrag, out3)
		}
	}
	if !strings.Contains(out3, PrivateKeyRedactionPlaceholder) {
		t.Errorf("expected %s in output: %s", PrivateKeyRedactionPlaceholder, out3)
	}
}

func TestLogger_PublicCertificatesIntact(t *testing.T) {
	var buf bytes.Buffer
	logger := NewJSONLogger(&buf, LevelInfo, "cert-test")

	logger.Info("Loaded TLS certificate", "cert", testCertPEM)

	out := buf.String()
	if !strings.Contains(out, "BEGIN CERTIFICATE") {
		t.Errorf("public certificate should remain intact in log output, got: %s", out)
	}
}

func TestLogger_ConcurrentLogging(t *testing.T) {
	var buf bytes.Buffer
	logger := NewJSONLogger(&buf, LevelInfo, "concurrency-test")

	var wg sync.WaitGroup
	goroutines := 50
	iterations := 20

	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(gid int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				logger.Info("concurrent log record", "gid", gid, "iter", i)
			}
		}(g)
	}

	wg.Wait()

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	expectedTotal := goroutines * iterations
	if len(lines) != expectedTotal {
		t.Errorf("expected %d log lines, got %d", expectedTotal, len(lines))
	}

	// Verify all emitted lines are valid JSON
	for i, line := range lines {
		var parsed map[string]any
		if err := json.Unmarshal([]byte(line), &parsed); err != nil {
			t.Fatalf("line %d is malformed JSON: %s (error: %v)", i, line, err)
		}
	}
}

func TestLogger_ContextIntegration(t *testing.T) {
	var buf bytes.Buffer
	customLogger := NewJSONLogger(&buf, LevelDebug, "ctx-service")

	ctx := WithContext(context.Background(), customLogger)
	retrieved := FromContext(ctx)

	retrieved.Info("hello from context")
	if !strings.Contains(buf.String(), `"service":"ctx-service"`) {
		t.Errorf("expected ctx-service in output, got: %s", buf.String())
	}

	// FromContext with empty context returns fallback logger
	emptyLogger := FromContext(context.Background())
	if emptyLogger == nil {
		t.Errorf("expected fallback logger from empty context, got nil")
	}
}
