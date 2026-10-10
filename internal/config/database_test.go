package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDatabase_Defaults(t *testing.T) {
	cfg, err := LoadDatabaseConfig(MapEnv(map[string]string{}))
	if err != nil {
		t.Fatalf("unexpected error loading default db config: %v", err)
	}

	if cfg.Host != DefaultDbHost {
		t.Errorf("expected default host %s, got %s", DefaultDbHost, cfg.Host)
	}
	if cfg.Port != DefaultDbPort {
		t.Errorf("expected default port %d, got %d", DefaultDbPort, cfg.Port)
	}
	if cfg.Database != DefaultDbName {
		t.Errorf("expected default database %s, got %s", DefaultDbName, cfg.Database)
	}
	if cfg.User != DefaultDbUser {
		t.Errorf("expected default user %s, got %s", DefaultDbUser, cfg.User)
	}
	if cfg.Password != "" {
		t.Errorf("expected empty password, got %s", cfg.Password)
	}
	if cfg.SSL != nil {
		t.Errorf("expected nil SSL by default, got %+v", cfg.SSL)
	}
	if cfg.PoolMax != DefaultDbPoolMax {
		t.Errorf("expected poolMax %d, got %d", DefaultDbPoolMax, cfg.PoolMax)
	}
	if cfg.PoolMin != DefaultDbPoolMin {
		t.Errorf("expected poolMin %d, got %d", DefaultDbPoolMin, cfg.PoolMin)
	}
	if cfg.PoolIdleTimeout != DefaultDbPoolIdleTimeout {
		t.Errorf("expected poolIdleTimeout %v, got %v", DefaultDbPoolIdleTimeout, cfg.PoolIdleTimeout)
	}
	if cfg.ConnectionTimeout != DefaultDbConnectionTimeout {
		t.Errorf("expected connectionTimeout %v, got %v", DefaultDbConnectionTimeout, cfg.ConnectionTimeout)
	}
}

func TestDatabase_OverridesAndParsing(t *testing.T) {
	env := map[string]string{
		"DB_HOST":               "postgres.corp.internal",
		"DB_PORT":               "5433",
		"DB_NAME":               "wb_production",
		"DB_USER":               "wb_admin",
		"DB_PASSWORD":           "super-secret-password-123",
		"DB_POOL_MAX":           "25",
		"DB_POOL_MIN":           "5",
		"DB_POOL_IDLE_TIMEOUT":  "45000",
		"DB_CONNECTION_TIMEOUT": "10000",
	}

	cfg, err := LoadDatabaseConfig(MapEnv(env))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Host != "postgres.corp.internal" {
		t.Errorf("host mismatch: %s", cfg.Host)
	}
	if cfg.Port != 5433 {
		t.Errorf("port mismatch: %d", cfg.Port)
	}
	if cfg.Database != "wb_production" {
		t.Errorf("database mismatch: %s", cfg.Database)
	}
	if cfg.User != "wb_admin" {
		t.Errorf("user mismatch: %s", cfg.User)
	}
	if cfg.Password != "super-secret-password-123" {
		t.Errorf("password mismatch: %s", cfg.Password)
	}
	if cfg.PoolMax != 25 {
		t.Errorf("poolMax mismatch: %d", cfg.PoolMax)
	}
	if cfg.PoolMin != 5 {
		t.Errorf("poolMin mismatch: %d", cfg.PoolMin)
	}
	if cfg.PoolIdleTimeout != 45*time.Second {
		t.Errorf("poolIdleTimeout mismatch: %v", cfg.PoolIdleTimeout)
	}
	if cfg.ConnectionTimeout != 10*time.Second {
		t.Errorf("connectionTimeout mismatch: %v", cfg.ConnectionTimeout)
	}
}

func TestDatabase_InvalidPort(t *testing.T) {
	env := map[string]string{
		"DB_PORT": "not-a-port",
	}

	_, err := LoadDatabaseConfig(MapEnv(env))
	if err == nil {
		t.Fatal("expected error for invalid DB_PORT, got nil")
	}
	cfgErr, ok := err.(*ControllerConfigError)
	if !ok || cfgErr.Code != CodeInvalidPort {
		t.Fatalf("expected CodeInvalidPort, got %v", err)
	}
}

func TestDatabase_SSLModes(t *testing.T) {
	t.Run("sslMode verify with CA cert", func(t *testing.T) {
		tempDir := t.TempDir()
		caFile := filepath.Join(tempDir, "root.crt")
		_ = os.WriteFile(caFile, []byte("-----BEGIN CERTIFICATE-----\nMIIB..."), 0644)

		env := map[string]string{
			"DB_SSL":        "verify",
			"PGSSLROOTCERT": caFile,
		}

		cfg, err := LoadDatabaseConfig(MapEnv(env))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.SSL == nil || !cfg.SSL.Enabled {
			t.Fatalf("expected enabled SSL")
		}
		if !cfg.SSL.RejectUnauthorized {
			t.Errorf("expected RejectUnauthorized=true")
		}
		if cfg.SSL.MinVersion != "TLSv1.3" {
			t.Errorf("expected MinVersion TLSv1.3, got %s", cfg.SSL.MinVersion)
		}
		if string(cfg.SSL.CACertData) != "-----BEGIN CERTIFICATE-----\nMIIB..." {
			t.Errorf("CA cert data mismatch")
		}
	})

	t.Run("sslMode require in production verifies server identity", func(t *testing.T) {
		env := map[string]string{
			"DB_SSL":   "require",
			"NODE_ENV": "production",
		}
		cfg, err := LoadDatabaseConfig(MapEnv(env))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.SSL == nil || !cfg.SSL.Enabled {
			t.Fatalf("expected enabled SSL")
		}
		if !cfg.SSL.RejectUnauthorized {
			t.Errorf("expected RejectUnauthorized=true in production")
		}
	})

	t.Run("sslMode require in development does not verify server identity", func(t *testing.T) {
		env := map[string]string{
			"DB_SSL":   "require",
			"NODE_ENV": "development",
		}
		cfg, err := LoadDatabaseConfig(MapEnv(env))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.SSL == nil || !cfg.SSL.Enabled {
			t.Fatalf("expected enabled SSL")
		}
		if cfg.SSL.RejectUnauthorized {
			t.Errorf("expected RejectUnauthorized=false in development")
		}
	})

	t.Run("sslMode require-no-verify never verifies server identity", func(t *testing.T) {
		env := map[string]string{
			"DB_SSL":   "require-no-verify",
			"NODE_ENV": "production",
		}
		cfg, err := LoadDatabaseConfig(MapEnv(env))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.SSL == nil || !cfg.SSL.Enabled {
			t.Fatalf("expected enabled SSL")
		}
		if cfg.SSL.RejectUnauthorized {
			t.Errorf("expected RejectUnauthorized=false for require-no-verify")
		}
	})
}

func TestDatabase_ConnectionStringsAndSecretSafety(t *testing.T) {
	cfg := &DatabaseConfig{
		Host:     "db.prod.internal",
		Port:     5432,
		Database: "whatbreaks",
		User:     "wb_user",
		Password: "super-secret-password-xyz",
		SSL: &SSLConfig{
			Enabled: true,
		},
	}

	dsn := GetConnectionString(cfg)
	expected := "postgresql://wb_user:super-secret-password-xyz@db.prod.internal:5432/whatbreaks?sslmode=require"
	if dsn != expected {
		t.Fatalf("GetConnectionString mismatch:\ngot:  %s\nwant: %s", dsn, expected)
	}

	safeDsn := GetSafeConnectionString(cfg)
	if strings.Contains(safeDsn, "super-secret-password-xyz") {
		t.Fatalf("GetSafeConnectionString leaked password! Output: %s", safeDsn)
	}
	expectedSafe := "postgresql://wb_user:[REDACTED]@db.prod.internal:5432/whatbreaks?sslmode=require"
	if safeDsn != expectedSafe {
		t.Fatalf("GetSafeConnectionString mismatch:\ngot:  %s\nwant: %s", safeDsn, expectedSafe)
	}
}

func TestDatabase_Production_NoTestDBFallback(t *testing.T) {
	env := map[string]string{
		"NODE_ENV":         "production",
		"TEST_DB_USER":     "test_admin",
		"TEST_DB_NAME":     "test_database",
		"TEST_DB_PASSWORD": "test_secret_pass",
	}

	cfg, err := LoadDatabaseConfig(MapEnv(env))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// In production, TEST_DB_* must NOT override default database config
	if cfg.User != DefaultDbUser {
		t.Errorf("expected DefaultDbUser in production, got %q", cfg.User)
	}
	if cfg.Database != DefaultDbName {
		t.Errorf("expected DefaultDbName in production, got %q", cfg.Database)
	}
	if cfg.Password != "" {
		t.Errorf("expected empty password in production, got %q", cfg.Password)
	}
}
