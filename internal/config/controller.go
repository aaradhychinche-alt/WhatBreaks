package config

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Controller error codes corresponding to ControllerConfigError in JavaScript
const (
	CodeRequired                = "CONTROLLER_CONFIG_REQUIRED"
	CodeInvalidUrl              = "CONTROLLER_CONFIG_INVALID_URL"
	CodeInvalidClusterId        = "CONTROLLER_CONFIG_INVALID_CLUSTER_ID"
	CodeInvalidWorkspaceId      = "CONTROLLER_CONFIG_INVALID_WORKSPACE_ID"
	CodeInvalidBoolean          = "CONTROLLER_CONFIG_INVALID_BOOLEAN"
	CodeInvalidInterval         = "CONTROLLER_CONFIG_INVALID_INTERVAL"
	CodeInvalidPort             = "CONTROLLER_CONFIG_INVALID_PORT"
	CodeInvalidNamespaces       = "CONTROLLER_CONFIG_INVALID_NAMESPACES"
	CodeInvalidMode             = "CONTROLLER_CONFIG_INVALID_MODE"
	CodeInvalidTokenFile        = "CONTROLLER_CONFIG_INVALID_TOKEN_FILE"
	CodeTokenFileUnreadable     = "CONTROLLER_CONFIG_TOKEN_FILE_UNREADABLE"
	CodeUnsafeTokenFile         = "CONTROLLER_CONFIG_UNSAFE_TOKEN_FILE"
	CodeRawTokenForbidden       = "CONTROLLER_CONFIG_RAW_TOKEN_FORBIDDEN"
	CodeKubeconfigForbidden     = "CONTROLLER_CONFIG_KUBECONFIG_FORBIDDEN"
	CodeNamespacePolicyConflict = "CONTROLLER_CONFIG_NAMESPACE_POLICY_CONFLICT"
	CodeNamespacesRequired      = "CONTROLLER_CONFIG_NAMESPACES_REQUIRED"
)

// Default values from JavaScript controller config
const (
	DefaultReconcileInterval = 30 * time.Second
	DefaultShutdownTimeout   = 10 * time.Second
	DefaultHealthPort        = 8080
	DefaultControllerMode    = "observe"
)

var (
	rfc1123LabelRegex    = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
	machineTokenRegex    = regexp.MustCompile(`^(ttx|wbx)_[a-f0-9]{16}_[a-f0-9]{64}$`)
	uuidRegex            = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	intervalPatternRegex = regexp.MustCompile(`^(\d+)(ms|s|m|h)?$`)
)

// ControllerConfigError represents an error in controller configuration validation.
type ControllerConfigError struct {
	Code  string
	Field string
}

func (e *ControllerConfigError) Error() string {
	return fmt.Sprintf("Invalid controller configuration: %s", e.Field)
}

// ControllerConfig holds validated configuration for the controller process.
type ControllerConfig struct {
	ApiUrl                string
	ApiTokenFile          string
	ClusterId             string
	ClusterWide           bool
	HealthPort            int
	Mode                  string
	ReconcileInterval     time.Duration
	SecretFallbackEnabled bool
	ShutdownTimeout       time.Duration
	WatchNamespaces       []string
	WorkspaceId           string
}

// LoadControllerConfig loads and validates controller configuration from the provided EnvLookup.
func LoadControllerConfig(env EnvLookup) (*ControllerConfig, error) {
	if env == nil {
		env = OsEnv()
	}

	// 1. Forbidden environment variables check
	if forbiddenKey, ok := HasKey(env, "TOKENTIMER_API_TOKEN", "WB_API_TOKEN", "TT_API_TOKEN"); ok {
		return nil, &ControllerConfigError{
			Code:  CodeRawTokenForbidden,
			Field: forbiddenKey,
		}
	}
	if forbiddenKey, ok := HasKey(env, "KUBECONFIG"); ok {
		return nil, &ControllerConfigError{
			Code:  CodeKubeconfigForbidden,
			Field: forbiddenKey,
		}
	}

	// 2. Token file configuration
	tokenFile, ok := LookupWithFallback(env, "WB_API_TOKEN_FILE", "TOKENTIMER_API_TOKEN_FILE", "TT_API_TOKEN_FILE")
	tokenField := "WB_API_TOKEN_FILE"
	if !ok || strings.TrimSpace(tokenFile) == "" {
		// Use the legacy name if set, else primary name
		if _, okLegacy := env("TOKENTIMER_API_TOKEN_FILE"); okLegacy {
			tokenField = "TOKENTIMER_API_TOKEN_FILE"
		}
		return nil, &ControllerConfigError{
			Code:  CodeRequired,
			Field: tokenField,
		}
	}
	tokenFile = strings.TrimSpace(tokenFile)
	if _, err := LoadApiTokenFromFile(tokenFile); err != nil {
		return nil, err
	}

	// 3. Cluster-wide vs Watch Namespaces
	clusterWideVal, _ := LookupWithFallback(env, "WB_CLUSTER_WIDE", "CERTOPS_CLUSTER_WIDE")
	clusterWideField := "WB_CLUSTER_WIDE"
	if _, okLegacy := env("CERTOPS_CLUSTER_WIDE"); okLegacy {
		clusterWideField = "CERTOPS_CLUSTER_WIDE"
	}
	clusterWide, err := ParseBoolean(clusterWideVal, clusterWideField, false)
	if err != nil {
		return nil, err
	}

	namespacesVal, _ := LookupWithFallback(env, "WB_WATCH_NAMESPACES", "CERTOPS_WATCH_NAMESPACES")
	namespacesField := "WB_WATCH_NAMESPACES"
	if _, okLegacy := env("CERTOPS_WATCH_NAMESPACES"); okLegacy {
		namespacesField = "CERTOPS_WATCH_NAMESPACES"
	}
	watchNamespaces, err := ParseNamespaces(namespacesVal, namespacesField)
	if err != nil {
		return nil, err
	}

	if clusterWide && len(watchNamespaces) > 0 {
		return nil, &ControllerConfigError{
			Code:  CodeNamespacePolicyConflict,
			Field: namespacesField,
		}
	}
	if !clusterWide && len(watchNamespaces) == 0 {
		return nil, &ControllerConfigError{
			Code:  CodeNamespacesRequired,
			Field: namespacesField,
		}
	}

	// 4. API URL
	apiUrlRaw, ok := LookupWithFallback(env, "WB_API_URL", "TOKENTIMER_API_URL", "API_URL")
	apiUrlField := "WB_API_URL"
	if _, okLegacy := env("TOKENTIMER_API_URL"); okLegacy {
		apiUrlField = "TOKENTIMER_API_URL"
	}
	if !ok || strings.TrimSpace(apiUrlRaw) == "" {
		return nil, &ControllerConfigError{
			Code:  CodeRequired,
			Field: apiUrlField,
		}
	}
	apiUrl, err := ParseApiUrl(strings.TrimSpace(apiUrlRaw), apiUrlField)
	if err != nil {
		return nil, err
	}

	// 5. Cluster ID
	clusterIdRaw, ok := LookupWithFallback(env, "WB_CLUSTER_ID", "TOKENTIMER_CLUSTER_ID")
	clusterIdField := "WB_CLUSTER_ID"
	if _, okLegacy := env("TOKENTIMER_CLUSTER_ID"); okLegacy {
		clusterIdField = "TOKENTIMER_CLUSTER_ID"
	}
	if !ok || strings.TrimSpace(clusterIdRaw) == "" {
		return nil, &ControllerConfigError{
			Code:  CodeRequired,
			Field: clusterIdField,
		}
	}
	clusterId, err := ParseClusterId(strings.TrimSpace(clusterIdRaw), clusterIdField)
	if err != nil {
		return nil, err
	}

	// 6. Workspace ID
	workspaceIdRaw, ok := LookupWithFallback(env, "WB_WORKSPACE_ID", "TOKENTIMER_WORKSPACE_ID")
	workspaceIdField := "WB_WORKSPACE_ID"
	if _, okLegacy := env("TOKENTIMER_WORKSPACE_ID"); okLegacy {
		workspaceIdField = "TOKENTIMER_WORKSPACE_ID"
	}
	if !ok || strings.TrimSpace(workspaceIdRaw) == "" {
		return nil, &ControllerConfigError{
			Code:  CodeRequired,
			Field: workspaceIdField,
		}
	}
	workspaceId, err := ParseWorkspaceId(strings.TrimSpace(workspaceIdRaw), workspaceIdField)
	if err != nil {
		return nil, err
	}

	// 7. Health Port
	healthPortVal, _ := LookupWithFallback(env, "WB_HEALTH_PORT", "CERTOPS_HEALTH_PORT", "HEALTH_PORT")
	healthPortField := "WB_HEALTH_PORT"
	if _, okLegacy := env("CERTOPS_HEALTH_PORT"); okLegacy {
		healthPortField = "CERTOPS_HEALTH_PORT"
	}
	healthPort, err := ParsePort(healthPortVal, healthPortField, DefaultHealthPort)
	if err != nil {
		return nil, err
	}

	// 8. Controller Mode
	modeVal, _ := LookupWithFallback(env, "WB_CONTROLLER_MODE", "CERTOPS_CONTROLLER_MODE")
	modeField := "WB_CONTROLLER_MODE"
	if _, okLegacy := env("CERTOPS_CONTROLLER_MODE"); okLegacy {
		modeField = "CERTOPS_CONTROLLER_MODE"
	}
	mode, err := ParseMode(modeVal, modeField)
	if err != nil {
		return nil, err
	}

	// 9. Reconcile Interval
	reconcileVal, _ := LookupWithFallback(env, "WB_RECONCILE_INTERVAL", "CERTOPS_RECONCILE_INTERVAL")
	reconcileField := "WB_RECONCILE_INTERVAL"
	if _, okLegacy := env("CERTOPS_RECONCILE_INTERVAL"); okLegacy {
		reconcileField = "CERTOPS_RECONCILE_INTERVAL"
	}
	reconcileInterval, err := ParseInterval(reconcileVal, reconcileField, DefaultReconcileInterval)
	if err != nil {
		return nil, err
	}

	// 10. Shutdown Timeout
	shutdownVal, _ := LookupWithFallback(env, "WB_SHUTDOWN_TIMEOUT", "CERTOPS_SHUTDOWN_TIMEOUT")
	shutdownField := "WB_SHUTDOWN_TIMEOUT"
	if _, okLegacy := env("CERTOPS_SHUTDOWN_TIMEOUT"); okLegacy {
		shutdownField = "CERTOPS_SHUTDOWN_TIMEOUT"
	}
	shutdownTimeout, err := ParseInterval(shutdownVal, shutdownField, DefaultShutdownTimeout)
	if err != nil {
		return nil, err
	}

	// 11. Secret Fallback Enabled
	secretFallbackVal, _ := LookupWithFallback(env, "WB_SECRET_FALLBACK_ENABLED", "CERTOPS_SECRET_FALLBACK_ENABLED")
	secretFallbackField := "WB_SECRET_FALLBACK_ENABLED"
	if _, okLegacy := env("CERTOPS_SECRET_FALLBACK_ENABLED"); okLegacy {
		secretFallbackField = "CERTOPS_SECRET_FALLBACK_ENABLED"
	}
	secretFallback, err := ParseBoolean(secretFallbackVal, secretFallbackField, false)
	if err != nil {
		return nil, err
	}

	return &ControllerConfig{
		ApiUrl:                apiUrl,
		ApiTokenFile:          tokenFile,
		ClusterId:             clusterId,
		ClusterWide:           clusterWide,
		HealthPort:            healthPort,
		Mode:                  mode,
		ReconcileInterval:     reconcileInterval,
		SecretFallbackEnabled: secretFallback,
		ShutdownTimeout:       shutdownTimeout,
		WatchNamespaces:       watchNamespaces,
		WorkspaceId:           workspaceId,
	}, nil
}

// LoadApiTokenFromFile validates the token file path, permissions, format, and content.
// It returns the token string or a ControllerConfigError.
func LoadApiTokenFromFile(tokenFile string) (string, error) {
	field := "WB_API_TOKEN_FILE"
	if !filepath.IsAbs(tokenFile) {
		return "", &ControllerConfigError{
			Code:  CodeInvalidTokenFile,
			Field: field,
		}
	}

	stat, err := os.Stat(tokenFile)
	if err != nil {
		return "", &ControllerConfigError{
			Code:  CodeTokenFileUnreadable,
			Field: field,
		}
	}

	if !stat.Mode().IsRegular() {
		return "", &ControllerConfigError{
			Code:  CodeUnsafeTokenFile,
			Field: field,
		}
	}

	// Permission check: on non-Windows platforms, file must not have group or world write bits (0o022)
	if runtime.GOOS != "windows" {
		if stat.Mode().Perm()&0o022 != 0 {
			return "", &ControllerConfigError{
				Code:  CodeUnsafeTokenFile,
				Field: field,
			}
		}
	}

	contents, err := os.ReadFile(tokenFile)
	if err != nil {
		return "", &ControllerConfigError{
			Code:  CodeTokenFileUnreadable,
			Field: field,
		}
	}

	token := strings.TrimSpace(string(contents))
	if token == "" || len(token) != 85 || !machineTokenRegex.MatchString(token) ||
		strings.Contains(token, "\x00") || containsPrivateKeyMaterial(token) {
		return "", &ControllerConfigError{
			Code:  CodeInvalidTokenFile,
			Field: field,
		}
	}

	return token, nil
}

// containsPrivateKeyMaterial checks if a token string contains private key markers.
func containsPrivateKeyMaterial(s string) bool {
	upper := strings.ToUpper(s)
	return strings.Contains(upper, "-----BEGIN") ||
		strings.Contains(upper, "PRIVATE KEY") ||
		strings.Contains(upper, "CERTIFICATE")
}

// ParseApiUrl parses and validates the API URL.
func ParseApiUrl(value, field string) (string, error) {
	parsed, err := url.Parse(value)
	if err != nil {
		return "", &ControllerConfigError{
			Code:  CodeInvalidUrl,
			Field: field,
		}
	}

	if (parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.Hostname() == "" ||
		parsed.User != nil {
		return "", &ControllerConfigError{
			Code:  CodeInvalidUrl,
			Field: field,
		}
	}

	return parsed.String(), nil
}

// ParseClusterId validates that the cluster ID is <= 63 chars and conforms to RFC 1123 DNS label.
func ParseClusterId(value, field string) (string, error) {
	if len(value) > 63 || !rfc1123LabelRegex.MatchString(value) {
		return "", &ControllerConfigError{
			Code:  CodeInvalidClusterId,
			Field: field,
		}
	}
	return value, nil
}

// ParseWorkspaceId validates that the workspace ID is a valid UUID and returns it lowercased.
func ParseWorkspaceId(value, field string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if !uuidRegex.MatchString(strings.ToLower(trimmed)) {
		return "", &ControllerConfigError{
			Code:  CodeInvalidWorkspaceId,
			Field: field,
		}
	}
	return strings.ToLower(trimmed), nil
}

// ParseBoolean parses a boolean string ("true" or "false"), returning defaultValue if unset/empty.
func ParseBoolean(value, field string, defaultValue bool) (bool, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return defaultValue, nil
	}
	if trimmed == "true" {
		return true, nil
	}
	if trimmed == "false" {
		return false, nil
	}
	return false, &ControllerConfigError{
		Code:  CodeInvalidBoolean,
		Field: field,
	}
}

// ParseInterval parses a duration string supporting units ms, s, m, h.
// Default multiplier is ms if no unit is provided. Max interval is 86,400,000 ms (24 hours).
func ParseInterval(value, field string, defaultValue time.Duration) (time.Duration, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return defaultValue, nil
	}

	match := intervalPatternRegex.FindStringSubmatch(trimmed)
	if len(match) == 0 {
		return 0, &ControllerConfigError{
			Code:  CodeInvalidInterval,
			Field: field,
		}
	}

	num, err := strconv.ParseInt(match[1], 10, 64)
	if err != nil || num <= 0 {
		return 0, &ControllerConfigError{
			Code:  CodeInvalidInterval,
			Field: field,
		}
	}

	unit := match[2]
	var multiplier time.Duration
	switch unit {
	case "", "ms":
		multiplier = time.Millisecond
	case "s":
		multiplier = time.Second
	case "m":
		multiplier = time.Minute
	case "h":
		multiplier = time.Hour
	default:
		multiplier = time.Millisecond
	}

	d := time.Duration(num) * multiplier
	if d > 24*time.Hour {
		return 0, &ControllerConfigError{
			Code:  CodeInvalidInterval,
			Field: field,
		}
	}

	return d, nil
}

// ParseNamespaces parses a comma-separated list of namespace names.
// Each namespace must be unique, <= 63 chars, and match RFC 1123 DNS label.
func ParseNamespaces(value, field string) ([]string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return []string{}, nil
	}

	parts := strings.Split(trimmed, ",")
	namespaces := make([]string, 0, len(parts))
	unique := make(map[string]struct{}, len(parts))

	for _, raw := range parts {
		ns := strings.TrimSpace(raw)
		if ns == "" || len(ns) > 63 || !rfc1123LabelRegex.MatchString(ns) {
			return nil, &ControllerConfigError{
				Code:  CodeInvalidNamespaces,
				Field: field,
			}
		}
		if _, exists := unique[ns]; exists {
			return nil, &ControllerConfigError{
				Code:  CodeInvalidNamespaces,
				Field: field,
			}
		}
		unique[ns] = struct{}{}
		namespaces = append(namespaces, ns)
	}

	return namespaces, nil
}

// ParseMode parses controller mode: "observe" (default) or "provision".
func ParseMode(value, field string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || trimmed == "observe" {
		return "observe", nil
	}
	if trimmed == "provision" {
		return "provision", nil
	}
	return "", &ControllerConfigError{
		Code:  CodeInvalidMode,
		Field: field,
	}
}
