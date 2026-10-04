package health

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthHandler_Endpoints(t *testing.T) {
	state := NewState()
	handler := Handler(state)

	// Initially healthy=true, ready=false
	reqHealth := httptest.NewRequest(http.MethodGet, PathHealthz, nil)
	recHealth := httptest.NewRecorder()
	handler.ServeHTTP(recHealth, reqHealth)

	if recHealth.Code != http.StatusOK {
		t.Fatalf("expected 200 on /healthz, got %d", recHealth.Code)
	}
	if recHealth.Body.String() != BodyHealthy {
		t.Fatalf("expected body %q, got %q", BodyHealthy, recHealth.Body.String())
	}

	reqReady := httptest.NewRequest(http.MethodGet, PathReadyz, nil)
	recReady := httptest.NewRecorder()
	handler.ServeHTTP(recReady, reqReady)

	if recReady.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 on /readyz when not ready, got %d", recReady.Code)
	}
	if recReady.Body.String() != BodyNotReady {
		t.Fatalf("expected body %q, got %q", BodyNotReady, recReady.Body.String())
	}

	// Flip ready to true
	state.SetReady(true)
	recReady2 := httptest.NewRecorder()
	handler.ServeHTTP(recReady2, reqReady)

	if recReady2.Code != http.StatusOK {
		t.Fatalf("expected 200 on /readyz when ready, got %d", recReady2.Code)
	}
	if recReady2.Body.String() != BodyReady {
		t.Fatalf("expected body %q, got %q", BodyReady, recReady2.Body.String())
	}

	// Flip healthy to false
	state.SetHealthy(false)
	recHealth2 := httptest.NewRecorder()
	handler.ServeHTTP(recHealth2, reqHealth)

	if recHealth2.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 on /healthz when unhealthy, got %d", recHealth2.Code)
	}
	if recHealth2.Body.String() != BodyUnavailable {
		t.Fatalf("expected body %q, got %q", BodyUnavailable, recHealth2.Body.String())
	}
}

func TestHealthHandler_MethodAndPathRejection(t *testing.T) {
	state := NewState()
	handler := Handler(state)

	// POST not allowed
	reqPost := httptest.NewRequest(http.MethodPost, PathHealthz, nil)
	recPost := httptest.NewRecorder()
	handler.ServeHTTP(recPost, reqPost)

	if recPost.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405 on POST, got %d", recPost.Code)
	}

	// 404 on unknown path
	req404 := httptest.NewRequest(http.MethodGet, "/unknown", nil)
	rec404 := httptest.NewRecorder()
	handler.ServeHTTP(rec404, req404)

	if rec404.Code != http.StatusNotFound {
		t.Fatalf("expected 404 on unknown path, got %d", rec404.Code)
	}
}
