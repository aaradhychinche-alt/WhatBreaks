package config

import (
	"strings"
	"testing"
)

func TestConfig_TopLevelLoad(t *testing.T) {
	tokenFile := createTestTokenFile(t, validMachineToken, 0600)

	env := map[string]string{
		"WB_API_TOKEN_FILE": tokenFile,
		"WB_API_URL":        "https://api.whatbreaks.dev",
		"WB_CLUSTER_ID":     "k8s-us-west",
		"WB_WORKSPACE_ID":   validWorkspaceId,
		"WB_CLUSTER_WIDE":   "true",
		"DB_HOST":           "db.internal",
		"DB_PORT":           "5432",
		"DB_NAME":           "wb_test",
		"DB_USER":           "wb_app",
		"DB_PASSWORD":       "secret_db_pass_123",
		"SESSION_SECRET":    "super-secret-session-key",
		"NODE_ENV":          "production",
		"LOG_LEVEL":         "debug",
		"LOG_SERVICE_NAME":  "wb-test-service",
	}

	cfg, err := Load(MapEnv(env))
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	// Verify database config
	if cfg.Database.Host != "db.internal" {
		t.Errorf("expected db host db.internal, got %s", cfg.Database.Host)
	}
	if cfg.Database.Password != "secret_db_pass_123" {
		t.Errorf("db password mismatch")
	}

	// Verify controller config
	if cfg.Controller == nil {
		t.Fatalf("expected non-nil Controller config")
	}
	if cfg.Controller.ClusterId != "k8s-us-west" {
		t.Errorf("expected clusterId k8s-us-west, got %s", cfg.Controller.ClusterId)
	}

	// Verify app config
	if cfg.App.SessionSecret != "super-secret-session-key" {
		t.Errorf("session secret mismatch")
	}
	if cfg.App.Environment != "production" {
		t.Errorf("expected environment production, got %s", cfg.App.Environment)
	}

	// Verify logging
	if cfg.Logging.Level != "debug" {
		t.Errorf("expected log level debug, got %s", cfg.Logging.Level)
	}
	if cfg.Logging.ServiceName != "wb-test-service" {
		t.Errorf("expected service name wb-test-service, got %s", cfg.Logging.ServiceName)
	}
}

func TestConfig_ProductionRequiresSessionSecret(t *testing.T) {
	env := map[string]string{
		"NODE_ENV": "production",
		// SESSION_SECRET omitted
	}

	_, err := Load(MapEnv(env))
	if err == nil {
		t.Fatal("expected error in production when SESSION_SECRET is missing, got nil")
	}
	if !strings.Contains(err.Error(), "SESSION_SECRET is required in production") {
		t.Fatalf("expected SESSION_SECRET error, got %v", err)
	}
}

func TestConfig_ErrorSafety_NoSecretLeaks(t *testing.T) {
	secretVal := "VERY_SECRET_KEY_NOT_TO_BE_PRINTED_12345"

	// Create an invalid port error while a secret is present in the env
	env := map[string]string{
		"DB_PORT":        "invalid-port",
		"DB_PASSWORD":    secretVal,
		"SESSION_SECRET": secretVal,
	}

	_, err := Load(MapEnv(env))
	if err == nil {
		t.Fatal("expected error for invalid port, got nil")
	}

	errStr := err.Error()
	if strings.Contains(errStr, secretVal) {
		t.Fatalf("SECURITY VIOLATION: error message contains raw secret!\nError: %s", errStr)
	}
}
