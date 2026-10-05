package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aaradhychinche-alt/WhatBreaks/internal/auth"
	"github.com/aaradhychinche-alt/WhatBreaks/internal/logging"
)

type rateLimitEntry struct {
	count     int
	resetTime time.Time
}

// RateLimiter implements an in-memory process-local rate limiter
// faithfully matching infrastructure/api/middleware/rateLimit.js.
type RateLimiter struct {
	mu           sync.Mutex
	window       time.Duration
	max          int
	entries      map[string]*rateLimitEntry
	logger       logging.Logger
	isDevOrTest  bool
	now          func() time.Time
	cleanupEvery time.Duration
	lastCleanup  time.Time
}

// NewRateLimiter creates a RateLimiter with specified window and limit.
func NewRateLimiter(window time.Duration, max int, isDevOrTest bool, logger logging.Logger) *RateLimiter {
	if window <= 0 {
		window = 1 * time.Minute
	}
	if max <= 0 {
		if isDevOrTest {
			max = 1000
		} else {
			max = 300
		}
	}

	return &RateLimiter{
		window:       window,
		max:          max,
		entries:      make(map[string]*rateLimitEntry),
		logger:       logger,
		isDevOrTest:  isDevOrTest,
		now:          time.Now,
		cleanupEvery: 5 * time.Minute,
		lastCleanup:  time.Now(),
	}
}

// SetNow allows overriding time.Now for deterministic testing.
func (rl *RateLimiter) SetNow(fn func() time.Time) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	rl.now = fn
}

// ResolveKey determines the rate-limiting key for the request.
// If an authenticated user is present in context, keys by user-<id>;
// otherwise keys by client IP.
func (rl *RateLimiter) ResolveKey(r *http.Request) string {
	if user, ok := auth.UserFromContext(r.Context()); ok && user.ID != "" {
		return "user-" + user.ID
	}
	ip := auth.ResolveClientIP(r)
	if ip == "" {
		ip = "127.0.0.1"
	}
	return ip
}

// IsExempt checks whether the request path is exempt from global rate limiting,
// matching lines 79-90 of infrastructure/api/middleware/rateLimit.js.
func (rl *RateLimiter) IsExempt(r *http.Request) bool {
	path := r.URL.Path

	// Health routes
	if path == "/" || path == "/health" || path == "/healthz" || path == "/readyz" {
		return true
	}

	// Session and CSRF token endpoints
	if path == "/api/session" || path == "/api/csrf-token" {
		return true
	}

	// Authenticated API requests
	if user, ok := auth.UserFromContext(r.Context()); ok && user.ID != "" && strings.HasPrefix(path, "/api/") {
		return true
	}

	// Development / test mode bypass for /api/ routes
	if rl.isDevOrTest && strings.HasPrefix(path, "/api/") {
		return true
	}

	return false
}

// Middleware returns an http.Handler middleware enforcing global rate limiting.
func (rl *RateLimiter) Middleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if rl.IsExempt(r) {
				next.ServeHTTP(w, r)
				return
			}

			key := rl.ResolveKey(r)
			currentTime := rl.now()

			rl.mu.Lock()
			// Periodic expired key cleanup
			if currentTime.Sub(rl.lastCleanup) > rl.cleanupEvery {
				for k, v := range rl.entries {
					if currentTime.After(v.resetTime) {
						delete(rl.entries, k)
					}
				}
				rl.lastCleanup = currentTime
			}

			entry, exists := rl.entries[key]
			if !exists || currentTime.After(entry.resetTime) {
				entry = &rateLimitEntry{
					count:     0,
					resetTime: currentTime.Add(rl.window),
				}
				rl.entries[key] = entry
			}

			entry.count++
			currentCount := entry.count
			resetSeconds := int(entry.resetTime.Sub(currentTime).Seconds())
			if resetSeconds < 0 {
				resetSeconds = 0
			}
			remaining := rl.max - currentCount
			if remaining < 0 {
				remaining = 0
			}
			rl.mu.Unlock()

			// Set standard rate limit headers
			w.Header().Set("RateLimit-Limit", strconv.Itoa(rl.max))
			w.Header().Set("RateLimit-Remaining", strconv.Itoa(remaining))
			w.Header().Set("RateLimit-Reset", strconv.Itoa(resetSeconds))

			if currentCount > rl.max {
				if rl.logger != nil {
					var userID string
					if user, ok := auth.UserFromContext(r.Context()); ok {
						userID = user.ID
					}
					rl.logger.Warn("RATE_LIMIT_EXCEEDED",
						"key", key,
						"userId", userID,
						"ip", auth.ResolveClientIP(r),
						"userAgent", r.Header.Get("User-Agent"),
					)
				}

				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				w.WriteHeader(http.StatusTooManyRequests)
				_ = json.NewEncoder(w).Encode(map[string]string{
					"error": "Too many requests from this IP, please try again later.",
				})
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
