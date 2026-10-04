package config

import (
	"errors"
	"time"
)

// Forbidden environment variables to enforce security invariants
const (
	EnvWbApiToken         = "WB_API_TOKEN"
	EnvTtApiToken         = "TT_API_TOKEN"
	EnvKubeconfig         = "KUBECONFIG"
	EnvWbApiTokenFile     = "WB_API_TOKEN_FILE"
	EnvTtApiTokenFile     = "TT_API_TOKEN_FILE"
	EnvWbApiUrl           = "WB_API_URL"
	EnvApiUrl             = "API_URL"
	EnvAllowInsecurePerms = "ALLOW_INSECURE_TOKEN_FILE_PERMISSIONS"
)

// Default platform operational parameters
const (
	DefaultHealthPort         = 8081
	DefaultApiPort            = 4000
	DefaultControllerInterval = 60 * time.Second
	DefaultShutdownTimeout    = 30 * time.Second
	DefaultKubeClientTimeout  = 10 * time.Second
	DefaultLogLevel           = "info"
	DefaultCoreEngineAddress  = "127.0.0.1:50051"
	DefaultCoreEngineTimeout  = 5 * time.Second
)

var (
	ErrForbiddenRawTokenEnv   = errors.New("raw API tokens in environment variables are forbidden; mount token as secret and use WB_API_TOKEN_FILE")
	ErrForbiddenKubeconfigEnv = errors.New("explicit KUBECONFIG environment variable is forbidden; controller must use in-cluster config or default kubeconfig")
	ErrMissingTokenFile       = errors.New("token file path is required")
	ErrInvalidPort            = errors.New("port must be between 1 and 65535")
)

// PlatformConfig defines the top-level configuration for the WhatBreaks Go platform.
type PlatformConfig struct {
	Controller ControllerConfig
	Database   DatabaseConfig
	Health     HealthConfig
	Logging    LoggingConfig
	CoreEngine CoreEngineConfig
}

// ControllerConfig holds settings for controller operation and K8s integration.
type ControllerConfig struct {
	TokenFilePath                     string
	AllowInsecureTokenFilePermissions bool
	ApiURL                            string
	Interval                          time.Duration
	ShutdownTimeout                   time.Duration
	KubeClientTimeout                 time.Duration
}

// DatabaseConfig holds connection and pooling options for PostgreSQL.
type DatabaseConfig struct {
	Host              string
	Port              int
	Name              string
	User              string
	Password          string
	DatabaseURL       string
	SSLMode           string // verify, require, require-no-verify, disable
	CACertPath        string
	MaxConnections    int
	MinConnections    int
	IdleTimeout       time.Duration
	ConnectionTimeout time.Duration
	AcquireTimeout    time.Duration
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

// ValidatePort checks whether a port number is valid within the TCP range [1, 65535].
func ValidatePort(port int) error {
	if port < 1 || port > 65535 {
		return ErrInvalidPort
	}
	return nil
}
