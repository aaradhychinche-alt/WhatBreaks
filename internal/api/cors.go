package api

import (
	"net/http"
	"strings"
)

var (
	defaultAllowedMethods = []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"}
	defaultAllowedHeaders = []string{
		"Content-Type",
		"Authorization",
		"X-Requested-With",
		"X-CSRF-Token",
		"Idempotency-Key",
	}
)

// CORS returns an HTTP middleware enforcing CORS policy matching infrastructure/api/index.js.
func CORS(allowedOrigins []string) func(http.Handler) http.Handler {
	originsMap := make(map[string]struct{}, len(allowedOrigins))
	for _, o := range allowedOrigins {
		clean := strings.TrimRight(strings.TrimSpace(o), "/")
		if clean != "" {
			originsMap[clean] = struct{}{}
		}
	}

	methodsStr := strings.Join(defaultAllowedMethods, ", ")
	headersStr := strings.Join(defaultAllowedHeaders, ", ")

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := strings.TrimRight(strings.TrimSpace(r.Header.Get("Origin")), "/")

			if origin == "" {
				// Non-CORS request
				next.ServeHTTP(w, r)
				return
			}

			w.Header().Add("Vary", "Origin")

			_, allowed := originsMap[origin]
			if !allowed {
				if r.Method == http.MethodOptions {
					w.WriteHeader(http.StatusForbidden)
					return
				}
				// For non-OPTIONS requests from unauthorized origins, proceed without CORS headers
				// so browser security blocks cross-origin reading.
				next.ServeHTTP(w, r)
				return
			}

			// Origin is authorized
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")

			if r.Method == http.MethodOptions {
				w.Header().Set("Access-Control-Allow-Methods", methodsStr)
				w.Header().Set("Access-Control-Allow-Headers", headersStr)
				w.Header().Set("Access-Control-Max-Age", "86400")
				w.WriteHeader(http.StatusNoContent)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
