package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHealthHandler(t *testing.T) {
	start := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	now := func() time.Time { return start.Add(90 * time.Second) }

	mux := http.NewServeMux()
	mux.Handle("GET /healthz", HealthHandler("node1", start, now))

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Fatalf("content-type = %q", ct)
	}
	var h Health
	if err := json.Unmarshal(rec.Body.Bytes(), &h); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	want := Health{Status: "ok", NodeID: "node1", UptimeSeconds: 90, Time: "2026-10-01T12:01:30Z"}
	if h != want {
		t.Fatalf("got %+v, want %+v", h, want)
	}
}

func TestHealthRejectsWrongMethod(t *testing.T) {
	mux := http.NewServeMux()
	mux.Handle("GET /healthz", HealthHandler("node1", time.Now(), time.Now))

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/healthz", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}
