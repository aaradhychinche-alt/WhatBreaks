package api

import (
	"encoding/json"
	"net/http"
	"strconv"
)

// BodyLimit returns an HTTP middleware enforcing request body size limit (default 10MB).
func BodyLimit(maxBytes int64) func(http.Handler) http.Handler {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBodyBytes
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Body == nil {
				next.ServeHTTP(w, r)
				return
			}

			// Pre-check Content-Length header if present to avoid reading large bodies
			if clStr := r.Header.Get("Content-Length"); clStr != "" {
				if cl, err := strconv.ParseInt(clStr, 10, 64); err == nil && cl > maxBytes {
					writePayloadTooLarge(w)
					return
				}
			}

			r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
			next.ServeHTTP(w, r)
		})
	}
}

func writePayloadTooLarge(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusRequestEntityTooLarge)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error": "Request body too large",
		"code":  "PAYLOAD_TOO_LARGE",
	})
}
