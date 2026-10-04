package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Default database parameters from packages/config/src/database.js
const (
	DefaultDbHost              = "localhost"
	DefaultDbPort              = 5432
	DefaultDbName              = "whatbreaks"
	DefaultDbUser              = "whatbreaks"
	DefaultDbPoolMax           = 10
	DefaultDbPoolMin           = 2
	DefaultDbPoolIdleTimeout   = 30 * time.Second
	DefaultDbConnectionTimeout = 5 * time.Second
	DefaultDbAcquireTimeout    = 60 * time.Second
)

// SSLConfig captures the TLS configuration for the PostgreSQL connection.
type SSLConfig struct {
	Enabled            bool
	Mode               string // "verify", "require", "require-no-verify", or "disable"
	RejectUnauthorized bool
	MinVersion         string // "TLSv1.3"
	CACertPath         string
	CACertData         []byte
}

// DatabaseConfig holds validated PostgreSQL connection, pool, and TLS configuration.
type DatabaseConfig struct {
	Host              string
	Port              int
	Database          string
	User              string
	Password          string
	SSL               *SSLConfig
	PoolMax           int
	PoolMin           int
	PoolIdleTimeout   time.Duration
	ConnectionTimeout time.Duration
	AcquireTimeout    time.Duration
}

// LoadDatabaseConfig loads database configuration from the provided EnvLookup.
func LoadDatabaseConfig(env EnvLookup) (*DatabaseConfig, error) {
	if env == nil {
		env = OsEnv()
	}

	host, _ := LookupWithFallback(env, "DB_HOST")
	if strings.TrimSpace(host) == "" {
		host = DefaultDbHost
	}

	port := DefaultDbPort
	if portStr, ok := LookupWithFallback(env, "DB_PORT"); ok && strings.TrimSpace(portStr) != "" {
		p, err := strconv.Atoi(strings.TrimSpace(portStr))
		if err != nil || p < 1 || p > 65535 {
			return nil, &ControllerConfigError{
				Code:  CodeInvalidPort,
				Field: "DB_PORT",
			}
		}
		port = p
	}

	name, _ := LookupWithFallback(env, "DB_NAME")
	if strings.TrimSpace(name) == "" {
		name = DefaultDbName
	}

	user, _ := LookupWithFallback(env, "DB_USER")
	if strings.TrimSpace(user) == "" {
		user = DefaultDbUser
	}

	password, _ := LookupWithFallback(env, "DB_PASSWORD")

	// Pool settings
	poolMax := DefaultDbPoolMax
	if poolMaxStr, ok := LookupWithFallback(env, "DB_POOL_MAX"); ok && strings.TrimSpace(poolMaxStr) != "" {
		if p, err := strconv.Atoi(strings.TrimSpace(poolMaxStr)); err == nil && p > 0 {
			poolMax = p
		}
	}

	poolMin := DefaultDbPoolMin
	if poolMinStr, ok := LookupWithFallback(env, "DB_POOL_MIN"); ok && strings.TrimSpace(poolMinStr) != "" {
		if p, err := strconv.Atoi(strings.TrimSpace(poolMinStr)); err == nil && p >= 0 {
			poolMin = p
		}
	}

	idleTimeout := DefaultDbPoolIdleTimeout
	if idleStr, ok := LookupWithFallback(env, "DB_POOL_IDLE_TIMEOUT"); ok && strings.TrimSpace(idleStr) != "" {
		if ms, err := strconv.ParseInt(strings.TrimSpace(idleStr), 10, 64); err == nil && ms > 0 {
			idleTimeout = time.Duration(ms) * time.Millisecond
		}
	}

	connTimeout := DefaultDbConnectionTimeout
	if connStr, ok := LookupWithFallback(env, "DB_CONNECTION_TIMEOUT"); ok && strings.TrimSpace(connStr) != "" {
		if ms, err := strconv.ParseInt(strings.TrimSpace(connStr), 10, 64); err == nil && ms > 0 {
			connTimeout = time.Duration(ms) * time.Millisecond
		}
	}

	acqTimeout := DefaultDbAcquireTimeout
	if acqStr, ok := LookupWithFallback(env, "DB_ACQUIRE_TIMEOUT"); ok && strings.TrimSpace(acqStr) != "" {
		if ms, err := strconv.ParseInt(strings.TrimSpace(acqStr), 10, 64); err == nil && ms > 0 {
			acqTimeout = time.Duration(ms) * time.Millisecond
		}
	}

	// SSL configuration
	sslMode, _ := LookupWithFallback(env, "DB_SSL")
	caPath, _ := LookupWithFallback(env, "PGSSLROOTCERT")
	nodeEnv, _ := LookupWithFallback(env, "NODE_ENV")
	isProduction := strings.ToLower(strings.TrimSpace(nodeEnv)) == "production"

	var ssl *SSLConfig
	sslModeTrimmed := strings.TrimSpace(sslMode)
	caPathTrimmed := strings.TrimSpace(caPath)

	if sslModeTrimmed == "verify" || caPathTrimmed != "" {
		var caData []byte
		if caPathTrimmed != "" {
			if data, err := os.ReadFile(caPathTrimmed); err == nil {
				caData = data
			}
		}
		ssl = &SSLConfig{
			Enabled:            true,
			Mode:               "verify",
			RejectUnauthorized: true,
			MinVersion:         "TLSv1.3",
			CACertPath:         caPathTrimmed,
			CACertData:         caData,
		}
	} else if sslModeTrimmed == "require" {
		ssl = &SSLConfig{
			Enabled:            true,
			Mode:               "require",
			RejectUnauthorized: isProduction,
			MinVersion:         "TLSv1.3",
		}
	} else if sslModeTrimmed == "require-no-verify" {
		ssl = &SSLConfig{
			Enabled:            true,
			Mode:               "require-no-verify",
			RejectUnauthorized: false,
			MinVersion:         "TLSv1.3",
		}
	}

	return &DatabaseConfig{
		Host:              host,
		Port:              port,
		Database:          name,
		User:              user,
		Password:          password,
		SSL:               ssl,
		PoolMax:           poolMax,
		PoolMin:           poolMin,
		PoolIdleTimeout:   idleTimeout,
		ConnectionTimeout: connTimeout,
		AcquireTimeout:    acqTimeout,
	}, nil
}

// GetConnectionString builds a standard PostgreSQL connection URL matching packages/config/src/database.js.
func GetConnectionString(cfg *DatabaseConfig) string {
	if cfg == nil {
		return ""
	}
	var auth string
	if cfg.Password != "" {
		auth = fmt.Sprintf("%s:%s@", url.QueryEscape(cfg.User), url.QueryEscape(cfg.Password))
	} else {
		auth = fmt.Sprintf("%s@", url.QueryEscape(cfg.User))
	}

	sslParam := ""
	if cfg.SSL != nil && cfg.SSL.Enabled {
		sslParam = "?sslmode=require"
	}

	return fmt.Sprintf("postgresql://%s%s:%d/%s%s",
		auth,
		cfg.Host,
		cfg.Port,
		cfg.Database,
		sslParam,
	)
}

// GetSafeConnectionString formats the connection string with the password redacted for logging and errors.
func GetSafeConnectionString(cfg *DatabaseConfig) string {
	if cfg == nil {
		return ""
	}
	var auth string
	if cfg.Password != "" {
		auth = fmt.Sprintf("%s:[REDACTED]@", url.QueryEscape(cfg.User))
	} else {
		auth = fmt.Sprintf("%s@", url.QueryEscape(cfg.User))
	}

	sslParam := ""
	if cfg.SSL != nil && cfg.SSL.Enabled {
		sslParam = "?sslmode=require"
	}

	return fmt.Sprintf("postgresql://%s%s:%d/%s%s",
		auth,
		cfg.Host,
		cfg.Port,
		cfg.Database,
		sslParam,
	)
}
