package api

import (
	"fmt"
	"net/http"
	"strings"
)

// SecurityHeaders returns an HTTP middleware that injects the security headers
// configured by Helmet in infrastructure/api/index.js.
func SecurityHeaders(appURL string) func(http.Handler) http.Handler {
	cleanAppURL := strings.TrimSpace(appURL)
	if cleanAppURL == "" {
		cleanAppURL = DefaultAppURL
	}

	csp := fmt.Sprintf(
		"default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self'; img-src 'self' data: https:; connect-src 'self' %s; font-src 'self'; object-src 'none'; media-src 'self'; frame-src 'none'; base-uri 'self'; form-action 'self'; frame-ancestors 'none'",
		cleanAppURL,
	)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()

			// 1. Content-Security-Policy
			h.Set("Content-Security-Policy", csp)

			// 2. Strict-Transport-Security (HSTS: 1 year, subdomains, preload)
			h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains; preload")

			// 3. X-Content-Type-Options: nosniff
			h.Set("X-Content-Type-Options", "nosniff")

			// 4. X-Frame-Options: DENY (matching frameguard { action: "deny" } and CSP frame-ancestors 'none')
			h.Set("X-Frame-Options", "DENY")

			// 5. X-XSS-Protection: 0 (modern standard disabling legacy buggy XSS auditors)
			h.Set("X-XSS-Protection", "0")

			// 6. Referrer-Policy: strict-origin-when-cross-origin
			h.Set("Referrer-Policy", "strict-origin-when-cross-origin")

			// 7. Cross-Origin-Resource-Policy: cross-origin
			h.Set("Cross-Origin-Resource-Policy", "cross-origin")

			next.ServeHTTP(w, r)
		})
	}
}
