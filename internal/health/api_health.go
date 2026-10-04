package health

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/aaradhychinche-alt/WhatBreaks/internal/logging"
)

// DBPinger defines the interface for database connectivity verification.
type DBPinger interface {
	Ping(ctx context.Context) error
}

// PingFunc adapts a function to the DBPinger interface.
type PingFunc func(ctx context.Context) error

func (f PingFunc) Ping(ctx context.Context) error {
	return f(ctx)
}

// APIHealthConfig provides dependencies and runtime metadata for the API health routes.
type APIHealthConfig struct {
	Pinger      DBPinger
	Logger      logging.Logger
	Environment string
	StartTime   time.Time
	Now         func() time.Time
	Uptime      func() float64
}

// APIHealthResponse defines the JSON schema for the GET /health API endpoint.
type APIHealthResponse struct {
	Status      string  `json:"status"`
	Timestamp   string  `json:"timestamp"`
	Uptime      float64 `json:"uptime,omitempty"`
	Environment string  `json:"environment,omitempty"`
	Error       string  `json:"error,omitempty"`
}

// NewAPIHealthHandler constructs an http.Handler implementing the two endpoints from
// infrastructure/api/routes/health.js:
//   - GET /       -> "API running" (text/plain)
//   - GET /health -> database ping with uptime, environment, and error reporting
func NewAPIHealthHandler(cfg APIHealthConfig) http.Handler {
	if cfg.Logger == nil {
		cfg.Logger = logging.NewStandardLogger(nil, logging.LevelInfo)
	}
	if cfg.Environment == "" {
		if env := os.Getenv("NODE_ENV"); env != "" {
			cfg.Environment = env
		} else if env := os.Getenv("WB_ENV"); env != "" {
			cfg.Environment = env
		}
	}
	if cfg.StartTime.IsZero() {
		cfg.StartTime = time.Now()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Uptime == nil {
		cfg.Uptime = func() float64 {
			return cfg.Now().Sub(cfg.StartTime).Seconds()
		}
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		// Normalize trailing slashes for routing
		if path != "/" && strings.HasSuffix(path, "/") {
			path = strings.TrimSuffix(path, "/")
		}

		if r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}

		switch path {
		case "", "/":
			// Matching JS: router.get("/", (req, res) => res.send("API running"));
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("API running"))

		case "/health":
			// Matching JS: router.get("/health", async (req, res) => { await pool.query("SELECT 1"); ... })
			ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
			defer cancel()

			var pingErr error
			if cfg.Pinger != nil {
				pingErr = cfg.Pinger.Ping(ctx)
			}

			timestamp := cfg.Now().UTC().Format(time.RFC3339Nano)

			if pingErr != nil {
				cfg.Logger.Error("Health check failed", "error", pingErr.Error())
				resp := APIHealthResponse{
					Status:    "unhealthy",
					Timestamp: timestamp,
					Error:     pingErr.Error(),
				}
				writeJSONResponse(w, http.StatusServiceUnavailable, resp)
				return
			}

			resp := APIHealthResponse{
				Status:      "healthy",
				Timestamp:   timestamp,
				Uptime:      cfg.Uptime(),
				Environment: cfg.Environment,
			}
			writeJSONResponse(w, http.StatusOK, resp)

		default:
			http.NotFound(w, r)
		}
	})
}

func writeJSONResponse(w http.ResponseWriter, statusCode int, payload any) {
	body, err := json.Marshal(payload)
	if err != nil {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"status":"unhealthy","error":"failed to serialize response"}`))
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(statusCode)
	_, _ = w.Write(body)
}
