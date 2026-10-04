package config

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Forbidden environment variable names to enforce Zero Secret Custody and in-cluster config invariants
const (
	EnvWbApiToken         = "WB_API_TOKEN"
	EnvTtApiToken         = "TT_API_TOKEN"
	EnvTokentimerApiToken = "TOKENTIMER_API_TOKEN"
	EnvKubeconfig         = "KUBECONFIG"

	EnvWbApiTokenFile         = "WB_API_TOKEN_FILE"
	EnvTtApiTokenFile         = "TT_API_TOKEN_FILE"
	EnvTokentimerApiTokenFile = "TOKENTIMER_API_TOKEN_FILE"

	EnvWbApiUrl           = "WB_API_URL"
	EnvTtApiUrl           = "TOKENTIMER_API_URL"
	EnvApiUrl             = "API_URL"
	EnvAppUrl             = "APP_URL"
	EnvAllowInsecurePerms = "ALLOW_INSECURE_TOKEN_FILE_PERMISSIONS"
)

// Platform-wide default operational constants
const (
	DefaultApiPort            = 4000
	DefaultControllerInterval = 60 * time.Second
	DefaultKubeClientTimeout  = 10 * time.Second
	DefaultLogLevel           = "info"
	DefaultCoreEngineAddress  = "127.0.0.1:50051"
	DefaultCoreEngineTimeout  = 5 * time.Second

	DefaultRateLimitWindow = 60 * time.Second
	DefaultRateLimitMax    = 100
)

var (
	ErrForbiddenRawTokenEnv   = errors.New("raw API tokens in environment variables are forbidden; mount token as secret and use WB_API_TOKEN_FILE")
	ErrForbiddenKubeconfigEnv = errors.New("explicit KUBECONFIG environment variable is forbidden; controller must use in-cluster config or default kubeconfig")
	ErrMissingTokenFile       = errors.New("token file path is required")
)

// PlatformConfig defines the top-level configuration for the WhatBreaks Go platform.
type PlatformConfig struct {
	App        AppConfig
	Controller *ControllerConfig
	Database   DatabaseConfig
	Network    NetworkConfig
	Health     HealthConfig
	Logging    LoggingConfig
	CoreEngine CoreEngineConfig
}

// AppConfig captures general web, authentication, and security parameters from packages/config/src/index.js.
type AppConfig struct {
	Mode            string // e.g. "oss", "enterprise"
	Environment     string // e.g. "development", "test", "production"
	BaseUrl         string
	ApiUrl          string
	SessionSecret   string
	CSRFEnabled     bool
	RateLimitWindow time.Duration
	RateLimitMax    int
}

// HealthConfig defines probe endpoint listener settings.
type HealthConfig struct {
	Port int
	Host string
}

// LoggingConfig defines structured logging parameters.
type LoggingConfig struct {
	Level       string // debug, info, warn, error
	ServiceName string
	Format      string // json, text
}

// CoreEngineConfig defines connection settings to the Rust Core Engine.
type CoreEngineConfig struct {
	Address string
	Timeout time.Duration
}

// Load loads and validates the entire PlatformConfig hierarchy from the given EnvLookup.
func Load(env EnvLookup) (*PlatformConfig, error) {
	if env == nil {
		env = OsEnv()
	}

	// 1. Load Database config
	dbCfg, err := LoadDatabaseConfig(env)
	if err != nil {
		return nil, fmt.Errorf("database config error: %w", err)
	}

	// 2. Load Network config
	netCfg := LoadNetworkConfig(env)

	// 3. Load App config (migrating packages/config/src/index.js)
	appCfg, err := LoadAppConfig(env)
	if err != nil {
		return nil, fmt.Errorf("app config error: %w", err)
	}

	// 4. Load Controller config (if controller token file is specified, or validate if required)
	var ctrlCfg *ControllerConfig
	if _, ok := LookupWithFallback(env, "WB_API_TOKEN_FILE", "TOKENTIMER_API_TOKEN_FILE", "TT_API_TOKEN_FILE"); ok {
		ctrl, err := LoadControllerConfig(env)
		if err != nil {
			return nil, err
		}
		ctrlCfg = ctrl
	}

	// 5. Health config
	healthPortVal, _ := LookupWithFallback(env, "WB_HEALTH_PORT", "CERTOPS_HEALTH_PORT", "HEALTH_PORT")
	healthPort, err := ParsePort(healthPortVal, "HEALTH_PORT", DefaultHealthPort)
	if err != nil {
		return nil, err
	}

	// 6. Logging config
	logLevel, _ := LookupWithFallback(env, "LOG_LEVEL")
	if strings.TrimSpace(logLevel) == "" {
		logLevel = DefaultLogLevel
	}
	serviceName, _ := LookupWithFallback(env, "LOG_SERVICE_NAME")
	if strings.TrimSpace(serviceName) == "" {
		serviceName = "whatbreaks"
	}

	// 7. CoreEngine config
	coreAddr, _ := LookupWithFallback(env, "WB_CORE_ENGINE_ADDR", "CORE_ENGINE_ADDR")
	if strings.TrimSpace(coreAddr) == "" {
		coreAddr = DefaultCoreEngineAddress
	}

	return &PlatformConfig{
		App:        *appCfg,
		Controller: ctrlCfg,
		Database:   *dbCfg,
		Network:    *netCfg,
		Health: HealthConfig{
			Port: healthPort,
		},
		Logging: LoggingConfig{
			Level:       logLevel,
			ServiceName: serviceName,
			Format:      "json",
		},
		CoreEngine: CoreEngineConfig{
			Address: coreAddr,
			Timeout: DefaultCoreEngineTimeout,
		},
	}, nil
}

// LoadFromEnv loads PlatformConfig from actual OS environment variables.
func LoadFromEnv() (*PlatformConfig, error) {
	return Load(OsEnv())
}

// LoadAppConfig loads application-level settings from packages/config/src/index.js.
func LoadAppConfig(env EnvLookup) (*AppConfig, error) {
	if env == nil {
		env = OsEnv()
	}

	mode, _ := LookupWithFallback(env, "WB_MODE", "TT_MODE")
	if strings.TrimSpace(mode) == "" {
		mode = "oss"
	}

	nodeEnv, _ := LookupWithFallback(env, "NODE_ENV")
	if strings.TrimSpace(nodeEnv) == "" {
		nodeEnv = "development"
	}

	baseUrl, _ := LookupWithFallback(env, "APP_URL")
	if strings.TrimSpace(baseUrl) == "" {
		baseUrl = "http://localhost:5173"
	}

	apiUrl, _ := LookupWithFallback(env, "API_URL")
	if strings.TrimSpace(apiUrl) == "" {
		apiUrl = "http://localhost:4000"
	}

	sessionSecret, _ := LookupWithFallback(env, "SESSION_SECRET")
	csrfVal, _ := LookupWithFallback(env, "CSRF_ENABLED")
	csrfEnabled := strings.TrimSpace(csrfVal) != "false"

	rateWindow := DefaultRateLimitWindow
	if windowStr, ok := LookupWithFallback(env, "RATE_LIMIT_WINDOW"); ok && strings.TrimSpace(windowStr) != "" {
		if ms, err := strconv.ParseInt(strings.TrimSpace(windowStr), 10, 64); err == nil && ms > 0 {
			rateWindow = time.Duration(ms) * time.Millisecond
		}
	}

	rateMax := DefaultRateLimitMax
	if maxStr, ok := LookupWithFallback(env, "RATE_LIMIT_MAX"); ok && strings.TrimSpace(maxStr) != "" {
		if m, err := strconv.Atoi(strings.TrimSpace(maxStr)); err == nil && m > 0 {
			rateMax = m
		}
	}

	// In production, SESSION_SECRET is required (matches validateConfig in packages/config/src/index.js)
	if strings.ToLower(strings.TrimSpace(nodeEnv)) == "production" {
		if strings.TrimSpace(sessionSecret) == "" {
			return nil, errors.New("SESSION_SECRET is required in production")
		}
	}

	return &AppConfig{
		Mode:            mode,
		Environment:     nodeEnv,
		BaseUrl:         baseUrl,
		ApiUrl:          apiUrl,
		SessionSecret:   sessionSecret,
		CSRFEnabled:     csrfEnabled,
		RateLimitWindow: rateWindow,
		RateLimitMax:    rateMax,
	}, nil
}
