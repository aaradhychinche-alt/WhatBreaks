package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func createTestTokenFile(t *testing.T, token string, perm os.FileMode) string {
	t.Helper()
	dir := t.TempDir()
	filePath := filepath.Join(dir, "token.txt")
	err := os.WriteFile(filePath, []byte(token), perm)
	if err != nil {
		t.Fatalf("failed to create temp token file: %v", err)
	}
	if err := os.Chmod(filePath, perm); err != nil {
		t.Fatalf("failed to chmod temp token file: %v", err)
	}
	return filePath
}

const (
	validMachineToken = "ttx_0123456789abcdef_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	validWbToken      = "wbx_0123456789abcdef_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	validWorkspaceId  = "12345678-1234-4234-8234-123456789abc"
)

func validControllerEnv(tokenFile string) map[string]string {
	return map[string]string{
		"WB_API_TOKEN_FILE": tokenFile,
		"WB_API_URL":        "https://api.whatbreaks.dev",
		"WB_CLUSTER_ID":     "k8s-prod-us-east-1",
		"WB_WORKSPACE_ID":   validWorkspaceId,
		"WB_CLUSTER_WIDE":   "true",
	}
}

func TestController_ForbiddenEnvironmentVariables(t *testing.T) {
	cases := []struct {
		name       string
		key        string
		expectCode string
	}{
		{"TOKENTIMER_API_TOKEN forbidden", "TOKENTIMER_API_TOKEN", CodeRawTokenForbidden},
		{"WB_API_TOKEN forbidden", "WB_API_TOKEN", CodeRawTokenForbidden},
		{"TT_API_TOKEN forbidden", "TT_API_TOKEN", CodeRawTokenForbidden},
		{"KUBECONFIG forbidden", "KUBECONFIG", CodeKubeconfigForbidden},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tokenFile := createTestTokenFile(t, validMachineToken, 0600)
			envMap := validControllerEnv(tokenFile)
			envMap[tc.key] = "some-value"

			_, err := LoadControllerConfig(MapEnv(envMap))
			if err == nil {
				t.Fatalf("expected error for forbidden key %s, got nil", tc.key)
			}
			cfgErr, ok := err.(*ControllerConfigError)
			if !ok {
				t.Fatalf("expected *ControllerConfigError, got %T: %v", err, err)
			}
			if cfgErr.Code != tc.expectCode {
				t.Fatalf("expected error code %s, got %s", tc.expectCode, cfgErr.Code)
			}
			if cfgErr.Field != tc.key {
				t.Fatalf("expected field %s, got %s", tc.key, cfgErr.Field)
			}
		})
	}
}

func TestController_TokenFileValidation(t *testing.T) {
	t.Run("relative path rejected", func(t *testing.T) {
		envMap := validControllerEnv("relative/path/token.txt")
		_, err := LoadControllerConfig(MapEnv(envMap))
		if err == nil {
			t.Fatal("expected error for relative path, got nil")
		}
		cfgErr := err.(*ControllerConfigError)
		if cfgErr.Code != CodeInvalidTokenFile {
			t.Fatalf("expected CodeInvalidTokenFile, got %s", cfgErr.Code)
		}
	})

	t.Run("non-existent file", func(t *testing.T) {
		envMap := validControllerEnv("/non/existent/path/token.txt")
		_, err := LoadControllerConfig(MapEnv(envMap))
		if err == nil {
			t.Fatal("expected error for non-existent file, got nil")
		}
		cfgErr := err.(*ControllerConfigError)
		if cfgErr.Code != CodeTokenFileUnreadable {
			t.Fatalf("expected CodeTokenFileUnreadable, got %s", cfgErr.Code)
		}
	})

	t.Run("directory rejected as unsafe", func(t *testing.T) {
		dir := t.TempDir()
		envMap := validControllerEnv(dir)
		_, err := LoadControllerConfig(MapEnv(envMap))
		if err == nil {
			t.Fatal("expected error for directory, got nil")
		}
		cfgErr := err.(*ControllerConfigError)
		if cfgErr.Code != CodeUnsafeTokenFile {
			t.Fatalf("expected CodeUnsafeTokenFile, got %s", cfgErr.Code)
		}
	})

	t.Run("insecure permissions rejected on non-windows", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("permission bits not enforced on windows")
		}
		insecureFile := createTestTokenFile(t, validMachineToken, 0666)
		envMap := validControllerEnv(insecureFile)
		_, err := LoadControllerConfig(MapEnv(envMap))
		if err == nil {
			t.Fatal("expected error for world-writable file, got nil")
		}
		cfgErr := err.(*ControllerConfigError)
		if cfgErr.Code != CodeUnsafeTokenFile {
			t.Fatalf("expected CodeUnsafeTokenFile, got %s", cfgErr.Code)
		}
	})

	t.Run("secure permissions 0600 accepted", func(t *testing.T) {
		secureFile := createTestTokenFile(t, validMachineToken, 0600)
		envMap := validControllerEnv(secureFile)
		cfg, err := LoadControllerConfig(MapEnv(envMap))
		if err != nil {
			t.Fatalf("unexpected error for 0600 file: %v", err)
		}
		if cfg.ApiTokenFile != secureFile {
			t.Fatalf("expected token file %s, got %s", secureFile, cfg.ApiTokenFile)
		}
	})

	t.Run("malformed token length", func(t *testing.T) {
		badFile := createTestTokenFile(t, "too-short", 0600)
		envMap := validControllerEnv(badFile)
		_, err := LoadControllerConfig(MapEnv(envMap))
		if err == nil {
			t.Fatal("expected error for short token, got nil")
		}
		cfgErr := err.(*ControllerConfigError)
		if cfgErr.Code != CodeInvalidTokenFile {
			t.Fatalf("expected CodeInvalidTokenFile, got %s", cfgErr.Code)
		}
	})

	t.Run("token with null byte rejected", func(t *testing.T) {
		badToken := validMachineToken[:40] + "\x00" + validMachineToken[41:]
		badFile := createTestTokenFile(t, badToken, 0600)
		envMap := validControllerEnv(badFile)
		_, err := LoadControllerConfig(MapEnv(envMap))
		if err == nil {
			t.Fatal("expected error for null byte, got nil")
		}
		cfgErr := err.(*ControllerConfigError)
		if cfgErr.Code != CodeInvalidTokenFile {
			t.Fatalf("expected CodeInvalidTokenFile, got %s", cfgErr.Code)
		}
	})

	t.Run("token containing private key material rejected", func(t *testing.T) {
		badToken := "-----BEGIN RSA PRIVATE KEY-----" + strings.Repeat("a", 54)
		badFile := createTestTokenFile(t, badToken, 0600)
		envMap := validControllerEnv(badFile)
		_, err := LoadControllerConfig(MapEnv(envMap))
		if err == nil {
			t.Fatal("expected error for private key material, got nil")
		}
		cfgErr := err.(*ControllerConfigError)
		if cfgErr.Code != CodeInvalidTokenFile {
			t.Fatalf("expected CodeInvalidTokenFile, got %s", cfgErr.Code)
		}
	})

	t.Run("valid wbx_ token accepted", func(t *testing.T) {
		wbFile := createTestTokenFile(t, validWbToken, 0600)
		envMap := validControllerEnv(wbFile)
		cfg, err := LoadControllerConfig(MapEnv(envMap))
		if err != nil {
			t.Fatalf("unexpected error for wbx_ token: %v", err)
		}
		if cfg.ApiTokenFile != wbFile {
			t.Fatalf("expected token file %s, got %s", wbFile, cfg.ApiTokenFile)
		}
	})
}

func TestController_ClusterIdValidation(t *testing.T) {
	cases := []struct {
		name       string
		clusterId  string
		expectCode string
	}{
		{"valid standard", "k8s-prod-1", ""},
		{"valid single char", "a", ""},
		{"valid numbers", "123", ""},
		{"valid exact 63 chars", strings.Repeat("a", 63), ""},
		{"invalid 64 chars", strings.Repeat("a", 64), CodeInvalidClusterId},
		{"invalid uppercase", "K8s-Prod-1", CodeInvalidClusterId},
		{"invalid underscore", "k8s_prod", CodeInvalidClusterId},
		{"invalid leading hyphen", "-k8s-prod", CodeInvalidClusterId},
		{"invalid trailing hyphen", "k8s-prod-", CodeInvalidClusterId},
		{"invalid empty", "", CodeRequired},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tokenFile := createTestTokenFile(t, validMachineToken, 0600)
			envMap := validControllerEnv(tokenFile)
			envMap["WB_CLUSTER_ID"] = tc.clusterId

			cfg, err := LoadControllerConfig(MapEnv(envMap))
			if tc.expectCode != "" {
				if err == nil {
					t.Fatalf("expected error code %s for cluster ID %q, got nil", tc.expectCode, tc.clusterId)
				}
				cfgErr := err.(*ControllerConfigError)
				if cfgErr.Code != tc.expectCode {
					t.Fatalf("expected error code %s, got %s", tc.expectCode, cfgErr.Code)
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error for cluster ID %q: %v", tc.clusterId, err)
				}
				if cfg.ClusterId != tc.clusterId {
					t.Fatalf("expected cluster ID %s, got %s", tc.clusterId, cfg.ClusterId)
				}
			}
		})
	}
}

func TestController_WorkspaceIdValidation(t *testing.T) {
	cases := []struct {
		name        string
		workspaceId string
		expectCode  string
		expected    string
	}{
		{"valid lowercase UUID", "12345678-1234-4234-8234-123456789abc", "", "12345678-1234-4234-8234-123456789abc"},
		{"valid uppercase UUID normalized", "12345678-1234-4234-8234-123456789ABC", "", "12345678-1234-4234-8234-123456789abc"},
		{"invalid UUID format", "not-a-uuid", CodeInvalidWorkspaceId, ""},
		{"invalid UUID extra digits", "12345678-1234-4234-8234-123456789abcdef", CodeInvalidWorkspaceId, ""},
		{"missing workspace ID", "", CodeRequired, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tokenFile := createTestTokenFile(t, validMachineToken, 0600)
			envMap := validControllerEnv(tokenFile)
			envMap["WB_WORKSPACE_ID"] = tc.workspaceId

			cfg, err := LoadControllerConfig(MapEnv(envMap))
			if tc.expectCode != "" {
				if err == nil {
					t.Fatalf("expected error code %s for workspace ID %q, got nil", tc.expectCode, tc.workspaceId)
				}
				cfgErr := err.(*ControllerConfigError)
				if cfgErr.Code != tc.expectCode {
					t.Fatalf("expected error code %s, got %s", tc.expectCode, cfgErr.Code)
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error for workspace ID %q: %v", tc.workspaceId, err)
				}
				if cfg.WorkspaceId != tc.expected {
					t.Fatalf("expected workspace ID %s, got %s", tc.expected, cfg.WorkspaceId)
				}
			}
		})
	}
}

func TestController_NamespacesAndClusterScope(t *testing.T) {
	tokenFile := createTestTokenFile(t, validMachineToken, 0600)

	t.Run("cluster wide true and no watch namespaces is valid", func(t *testing.T) {
		envMap := validControllerEnv(tokenFile)
		envMap["WB_CLUSTER_WIDE"] = "true"
		delete(envMap, "WB_WATCH_NAMESPACES")

		cfg, err := LoadControllerConfig(MapEnv(envMap))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !cfg.ClusterWide {
			t.Errorf("expected ClusterWide true")
		}
		if len(cfg.WatchNamespaces) != 0 {
			t.Errorf("expected empty WatchNamespaces")
		}
	})

	t.Run("cluster wide true with watch namespaces causes conflict", func(t *testing.T) {
		envMap := validControllerEnv(tokenFile)
		envMap["WB_CLUSTER_WIDE"] = "true"
		envMap["WB_WATCH_NAMESPACES"] = "default,kube-system"

		_, err := LoadControllerConfig(MapEnv(envMap))
		if err == nil {
			t.Fatal("expected conflict error, got nil")
		}
		cfgErr := err.(*ControllerConfigError)
		if cfgErr.Code != CodeNamespacePolicyConflict {
			t.Fatalf("expected CodeNamespacePolicyConflict, got %s", cfgErr.Code)
		}
	})

	t.Run("cluster wide false with no watch namespaces causes required error", func(t *testing.T) {
		envMap := validControllerEnv(tokenFile)
		envMap["WB_CLUSTER_WIDE"] = "false"
		delete(envMap, "WB_WATCH_NAMESPACES")

		_, err := LoadControllerConfig(MapEnv(envMap))
		if err == nil {
			t.Fatal("expected required error, got nil")
		}
		cfgErr := err.(*ControllerConfigError)
		if cfgErr.Code != CodeNamespacesRequired {
			t.Fatalf("expected CodeNamespacesRequired, got %s", cfgErr.Code)
		}
	})

	t.Run("cluster wide false with valid watch namespaces succeeds", func(t *testing.T) {
		envMap := validControllerEnv(tokenFile)
		envMap["WB_CLUSTER_WIDE"] = "false"
		envMap["WB_WATCH_NAMESPACES"] = "default, production, staging "

		cfg, err := LoadControllerConfig(MapEnv(envMap))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.ClusterWide {
			t.Errorf("expected ClusterWide false")
		}
		if len(cfg.WatchNamespaces) != 3 {
			t.Fatalf("expected 3 namespaces, got %d", len(cfg.WatchNamespaces))
		}
		expected := []string{"default", "production", "staging"}
		for i, ns := range expected {
			if cfg.WatchNamespaces[i] != ns {
				t.Errorf("namespace[%d] = %s, expected %s", i, cfg.WatchNamespaces[i], ns)
			}
		}
	})

	t.Run("duplicate namespace causes error", func(t *testing.T) {
		envMap := validControllerEnv(tokenFile)
		envMap["WB_CLUSTER_WIDE"] = "false"
		envMap["WB_WATCH_NAMESPACES"] = "default, production, default"

		_, err := LoadControllerConfig(MapEnv(envMap))
		if err == nil {
			t.Fatal("expected error for duplicate namespace, got nil")
		}
		cfgErr := err.(*ControllerConfigError)
		if cfgErr.Code != CodeInvalidNamespaces {
			t.Fatalf("expected CodeInvalidNamespaces, got %s", cfgErr.Code)
		}
	})

	t.Run("invalid namespace label causes error", func(t *testing.T) {
		envMap := validControllerEnv(tokenFile)
		envMap["WB_CLUSTER_WIDE"] = "false"
		envMap["WB_WATCH_NAMESPACES"] = "default, INVALID_UPPER"

		_, err := LoadControllerConfig(MapEnv(envMap))
		if err == nil {
			t.Fatal("expected error for uppercase namespace, got nil")
		}
		cfgErr := err.(*ControllerConfigError)
		if cfgErr.Code != CodeInvalidNamespaces {
			t.Fatalf("expected CodeInvalidNamespaces, got %s", cfgErr.Code)
		}
	})
}

func TestController_IntervalAndModeParsing(t *testing.T) {
	tokenFile := createTestTokenFile(t, validMachineToken, 0600)

	t.Run("valid interval units", func(t *testing.T) {
		envMap := validControllerEnv(tokenFile)
		envMap["WB_RECONCILE_INTERVAL"] = "45s"
		envMap["WB_SHUTDOWN_TIMEOUT"] = "1500ms"
		envMap["WB_CONTROLLER_MODE"] = "provision"

		cfg, err := LoadControllerConfig(MapEnv(envMap))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.ReconcileInterval != 45*time.Second {
			t.Errorf("expected 45s, got %v", cfg.ReconcileInterval)
		}
		if cfg.ShutdownTimeout != 1500*time.Millisecond {
			t.Errorf("expected 1500ms, got %v", cfg.ShutdownTimeout)
		}
		if cfg.Mode != "provision" {
			t.Errorf("expected provision mode, got %s", cfg.Mode)
		}
	})

	t.Run("interval exceeding 24 hours fails", func(t *testing.T) {
		envMap := validControllerEnv(tokenFile)
		envMap["WB_RECONCILE_INTERVAL"] = "25h"

		_, err := LoadControllerConfig(MapEnv(envMap))
		if err == nil {
			t.Fatal("expected error for interval > 24h, got nil")
		}
		cfgErr := err.(*ControllerConfigError)
		if cfgErr.Code != CodeInvalidInterval {
			t.Fatalf("expected CodeInvalidInterval, got %s", cfgErr.Code)
		}
	})

	t.Run("zero interval fails", func(t *testing.T) {
		envMap := validControllerEnv(tokenFile)
		envMap["WB_RECONCILE_INTERVAL"] = "0s"

		_, err := LoadControllerConfig(MapEnv(envMap))
		if err == nil {
			t.Fatal("expected error for 0s interval, got nil")
		}
		cfgErr := err.(*ControllerConfigError)
		if cfgErr.Code != CodeInvalidInterval {
			t.Fatalf("expected CodeInvalidInterval, got %s", cfgErr.Code)
		}
	})

	t.Run("invalid mode fails", func(t *testing.T) {
		envMap := validControllerEnv(tokenFile)
		envMap["WB_CONTROLLER_MODE"] = "unknown-mode"

		_, err := LoadControllerConfig(MapEnv(envMap))
		if err == nil {
			t.Fatal("expected error for invalid mode, got nil")
		}
		cfgErr := err.(*ControllerConfigError)
		if cfgErr.Code != CodeInvalidMode {
			t.Fatalf("expected CodeInvalidMode, got %s", cfgErr.Code)
		}
	})
}

func TestController_ApiUrlValidation(t *testing.T) {
	cases := []struct {
		name       string
		url        string
		expectCode string
	}{
		{"valid http", "http://localhost:4000", ""},
		{"valid https", "https://api.whatbreaks.dev", ""},
		{"invalid scheme ftp", "ftp://api.whatbreaks.dev", CodeInvalidUrl},
		{"invalid user info", "https://user:pass@api.whatbreaks.dev", CodeInvalidUrl},
		{"missing host", "http://", CodeInvalidUrl},
		{"missing url", "", CodeRequired},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tokenFile := createTestTokenFile(t, validMachineToken, 0600)
			envMap := validControllerEnv(tokenFile)
			envMap["WB_API_URL"] = tc.url

			cfg, err := LoadControllerConfig(MapEnv(envMap))
			if tc.expectCode != "" {
				if err == nil {
					t.Fatalf("expected error code %s for URL %q, got nil", tc.expectCode, tc.url)
				}
				cfgErr := err.(*ControllerConfigError)
				if cfgErr.Code != tc.expectCode {
					t.Fatalf("expected error code %s, got %s", tc.expectCode, cfgErr.Code)
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error for URL %q: %v", tc.url, err)
				}
				if cfg.ApiUrl != tc.url {
					t.Fatalf("expected ApiUrl %s, got %s", tc.url, cfg.ApiUrl)
				}
			}
		})
	}
}
