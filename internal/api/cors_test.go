package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCORS_AllowedOrigin(t *testing.T) {
	allowed := []string{"http://localhost:5173", "https://app.whatbreaks.dev"}
	corsHandler := CORS(allowed)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/data", nil)
	req.Header.Set("Origin", "http://localhost:5173")
	rec := httptest.NewRecorder()

	corsHandler.ServeHTTP(rec, req)

	res := rec.Result()
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", res.StatusCode)
	}

	if origin := res.Header.Get("Access-Control-Allow-Origin"); origin != "http://localhost:5173" {
		t.Errorf("expected Access-Control-Allow-Origin: http://localhost:5173, got %q", origin)
	}
	if creds := res.Header.Get("Access-Control-Allow-Credentials"); creds != "true" {
		t.Errorf("expected Access-Control-Allow-Credentials: true, got %q", creds)
	}
	if vary := res.Header.Get("Vary"); vary != "Origin" {
		t.Errorf("expected Vary: Origin, got %q", vary)
	}
}

func TestCORS_RejectedOrigin(t *testing.T) {
	allowed := []string{"http://localhost:5173"}
	corsHandler := CORS(allowed)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/data", nil)
	req.Header.Set("Origin", "https://evil-attacker.com")
	rec := httptest.NewRecorder()

	corsHandler.ServeHTTP(rec, req)

	res := rec.Result()
	defer res.Body.Close()

	if origin := res.Header.Get("Access-Control-Allow-Origin"); origin != "" {
		t.Errorf("expected NO Access-Control-Allow-Origin for rejected origin, got %q", origin)
	}
	if creds := res.Header.Get("Access-Control-Allow-Credentials"); creds != "" {
		t.Errorf("expected NO Access-Control-Allow-Credentials for rejected origin, got %q", creds)
	}
}

func TestCORS_PreflightOptions(t *testing.T) {
	allowed := []string{"http://localhost:5173"}
	corsHandler := CORS(allowed)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// 1. Allowed Preflight
	req := httptest.NewRequest(http.MethodOptions, "/api/resource", nil)
	req.Header.Set("Origin", "http://localhost:5173")
	rec := httptest.NewRecorder()

	corsHandler.ServeHTTP(rec, req)

	res := rec.Result()
	defer res.Body.Close()

	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("expected preflight 204 No Content, got %d", res.StatusCode)
	}
	if origin := res.Header.Get("Access-Control-Allow-Origin"); origin != "http://localhost:5173" {
		t.Errorf("expected Allow-Origin header, got %q", origin)
	}
	if methods := res.Header.Get("Access-Control-Allow-Methods"); methods == "" {
		t.Error("missing Access-Control-Allow-Methods")
	}
	if headers := res.Header.Get("Access-Control-Allow-Headers"); headers == "" {
		t.Error("missing Access-Control-Allow-Headers")
	}
	if maxAge := res.Header.Get("Access-Control-Max-Age"); maxAge != "86400" {
		t.Errorf("expected Max-Age 86400, got %q", maxAge)
	}

	// 2. Rejected Preflight
	reqReject := httptest.NewRequest(http.MethodOptions, "/api/resource", nil)
	reqReject.Header.Set("Origin", "https://unauthorized.org")
	recReject := httptest.NewRecorder()

	corsHandler.ServeHTTP(recReject, reqReject)

	resReject := recReject.Result()
	defer resReject.Body.Close()

	if resReject.StatusCode != http.StatusForbidden {
		t.Fatalf("expected preflight 403 Forbidden for rejected origin, got %d", resReject.StatusCode)
	}
	if origin := resReject.Header.Get("Access-Control-Allow-Origin"); origin != "" {
		t.Errorf("expected no Allow-Origin header on rejected preflight, got %q", origin)
	}
}

func TestCORS_NonCORSRequest(t *testing.T) {
	allowed := []string{"http://localhost:5173"}
	corsHandler := CORS(allowed)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/data", nil)
	rec := httptest.NewRecorder()

	corsHandler.ServeHTTP(rec, req)

	res := rec.Result()
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", res.StatusCode)
	}
	if origin := res.Header.Get("Access-Control-Allow-Origin"); origin != "" {
		t.Errorf("expected no Allow-Origin header for non-CORS request, got %q", origin)
	}
}
