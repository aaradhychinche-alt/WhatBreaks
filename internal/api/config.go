package api

import (
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/aaradhychinche-alt/WhatBreaks/internal/auth"
	"github.com/aaradhychinche-alt/WhatBreaks/internal/config"
	"github.com/aaradhychinche-alt/WhatBreaks/internal/database"
	"github.com/aaradhychinche-alt/WhatBreaks/internal/health"
	"github.com/aaradhychinche-alt/WhatBreaks/internal/logging"
)

const (
	// DefaultPort is the default HTTP port for the API server.
	DefaultPort = 4000

	// DefaultHost is the default listen interface for the API server.
	DefaultHost = "0.0.0.0"

	// DefaultMaxBodyBytes is the 10MB request body size limit from infrastructure/api/index.js.
	DefaultMaxBodyBytes = 10 * 1024 * 1024

	// DefaultAppURL is the default web client origin.
	DefaultAppURL = "http://localhost:5173"

	// DefaultShutdownTimeout is the graceful shutdown timeout for the HTTP server.
	DefaultShutdownTimeout = 30 * time.Second
)

// Config encapsulates runtime configuration for the API server.
type Config struct {
	Host              string
	Port              int
	AppURL            string
	MaxBodyBytes      int64
	ShutdownTimeout   time.Duration
	SessionSecret     string
	Environment       string
	IsDevelopment     bool
	IsTest            bool
	Logger            logging.Logger
	AllowedOrigins    []string
	Env               config.EnvLookup
	Database          *database.Database
	HealthPinger      health.DBPinger
	SessionCookie     auth.CookieConfig
	CsrfCookieName    string
	RateLimitWindow   time.Duration
	RateLimitMax      int
	SpeedLimitWindow  time.Duration
	SpeedLimitDelayAt int
	SpeedLimitDelayMs time.Duration
	ImpactHandler     http.Handler
}

// DefaultConfig returns an API Config populated with default settings.
func DefaultConfig() Config {
	return NewConfigFromEnv(nil, nil)
}

// NewConfigFromEnv initializes API Config from environment variables,
// faithfully implementing infrastructure/api/index.js defaults.
func NewConfigFromEnv(env config.EnvLookup, logger logging.Logger) Config {
	if env == nil {
		env = config.OsEnv()
	}
	if logger == nil {
		logger = logging.NewJSONLogger(os.Stderr, logging.LevelInfo, "api")
	}

	host, _ := env("HOST")
	if host == "" {
		host = DefaultHost
	}

	port := DefaultPort
	if pVal, _ := env("PORT"); pVal != "" {
		if p, err := strconv.Atoi(strings.TrimSpace(pVal)); err == nil && p > 0 && p <= 65535 {
			port = p
		}
	}

	appURL, _ := env("APP_URL")
	if appURL == "" {
		appURL = DefaultAppURL
	}

	nodeEnv, _ := env("NODE_ENV")
	if nodeEnv == "" {
		nodeEnv, _ = env("WB_ENV")
	}
	if nodeEnv == "" {
		nodeEnv = "development"
	}
	isDev := strings.ToLower(strings.TrimSpace(nodeEnv)) == "development"
	isTest := strings.ToLower(strings.TrimSpace(nodeEnv)) == "test"

	sessionSecret, _ := env("SESSION_SECRET")

	sessionCookie := auth.ResolveSessionCookieOptions(env)
	csrfCookieName := auth.ResolveCsrfCookieName(env, sessionCookie)
	allowedOrigins := auth.BuildCorsOrigins(env)

	// Rate limit configuration
	rateLimitWindow := 1 * time.Minute
	if wVal, _ := env("GLOBAL_RATE_LIMIT_WINDOW_MS"); wVal != "" {
		if ms, err := strconv.Atoi(strings.TrimSpace(wVal)); err == nil && ms > 0 {
			rateLimitWindow = time.Duration(ms) * time.Millisecond
		}
	}

	rateLimitMax := 300
	if isDev || isTest {
		rateLimitMax = 1000
	}
	if mVal, _ := env("GLOBAL_RATE_LIMIT_MAX"); mVal != "" {
		if m, err := strconv.Atoi(strings.TrimSpace(mVal)); err == nil && m > 0 {
			rateLimitMax = m
		}
	}

	speedLimitWindow := 15 * time.Minute
	if swVal, _ := env("GLOBAL_SLOWDOWN_WINDOW_MS"); swVal != "" {
		if ms, err := strconv.Atoi(strings.TrimSpace(swVal)); err == nil && ms > 0 {
			speedLimitWindow = time.Duration(ms) * time.Millisecond
		}
	}

	speedLimitDelayAt := 50
	if sdVal, _ := env("GLOBAL_SLOWDOWN_DELAY_AFTER"); sdVal != "" {
		if d, err := strconv.Atoi(strings.TrimSpace(sdVal)); err == nil && d > 0 {
			speedLimitDelayAt = d
		}
	}

	speedLimitDelayMs := 500 * time.Millisecond
	if smVal, _ := env("GLOBAL_SLOWDOWN_DELAY_MS"); smVal != "" {
		if ms, err := strconv.Atoi(strings.TrimSpace(smVal)); err == nil && ms > 0 {
			speedLimitDelayMs = time.Duration(ms) * time.Millisecond
		}
	}

	return Config{
		Host:              host,
		Port:              port,
		AppURL:            appURL,
		MaxBodyBytes:      DefaultMaxBodyBytes,
		ShutdownTimeout:   DefaultShutdownTimeout,
		SessionSecret:     sessionSecret,
		Environment:       nodeEnv,
		IsDevelopment:     isDev,
		IsTest:            isTest,
		Logger:            logger,
		AllowedOrigins:    allowedOrigins,
		Env:               env,
		SessionCookie:     sessionCookie,
		CsrfCookieName:    csrfCookieName,
		RateLimitWindow:   rateLimitWindow,
		RateLimitMax:      rateLimitMax,
		SpeedLimitWindow:  speedLimitWindow,
		SpeedLimitDelayAt: speedLimitDelayAt,
		SpeedLimitDelayMs: speedLimitDelayMs,
	}
}
