package api

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestBodyLimit_AllowedSize(t *testing.T) {
	limiter := BodyLimit(1024) // 1 KB limit
	handler := limiter(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("failed to read body: %v", err)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}))

	data := bytes.Repeat([]byte("a"), 500)
	req := httptest.NewRequest(http.MethodPost, "/upload", bytes.NewReader(data))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	res := rec.Result()
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", res.StatusCode)
	}
	body, _ := io.ReadAll(res.Body)
	if len(body) != 500 {
		t.Errorf("expected 500 bytes, got %d", len(body))
	}
}

func TestBodyLimit_ContentLengthPrecheck_Exceeded(t *testing.T) {
	limiter := BodyLimit(1024) // 1 KB limit
	handler := limiter(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler should not have been reached")
	}))

	req := httptest.NewRequest(http.MethodPost, "/upload", strings.NewReader("ignored"))
	req.Header.Set("Content-Length", strconv.Itoa(2048)) // 2 KB declares exceeding limit
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	res := rec.Result()
	defer res.Body.Close()

	if res.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413 Payload Too Large, got %d", res.StatusCode)
	}

	body, _ := io.ReadAll(res.Body)
	if !strings.Contains(string(body), "PAYLOAD_TOO_LARGE") {
		t.Errorf("expected PAYLOAD_TOO_LARGE in response body, got: %s", string(body))
	}
}

func TestBodyLimit_StreamExceededDuringRead(t *testing.T) {
	limiter := BodyLimit(100) // 100 byte limit
	handler := limiter(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := io.ReadAll(r.Body)
		if err != nil {
			writePayloadTooLarge(w)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))

	largeData := bytes.Repeat([]byte("x"), 200)
	req := httptest.NewRequest(http.MethodPost, "/upload", bytes.NewReader(largeData))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	res := rec.Result()
	defer res.Body.Close()

	if res.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d", res.StatusCode)
	}
}
