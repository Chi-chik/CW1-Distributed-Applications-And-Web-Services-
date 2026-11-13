package httpapi

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"p2pfs/internal/state"
)

var fixedNow = func() time.Time { return time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC) }

func newTestAPI(t *testing.T) (http.Handler, *state.Registry) {
	t.Helper()
	reg := state.NewRegistry("node1", fixedNow)
	h := NewRouter(Deps{
		NodeID: "node1", Started: fixedNow(), Now: fixedNow, Registry: reg,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), MaxBodyBytes: 256,
	})
	return h, reg
}

func do(h http.Handler, method, path, contentType, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func errorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var e ErrorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
		t.Fatalf("error body is not JSON: %q", rec.Body.String())
	}
	if e.Error.RequestID == "" {
		t.Fatal("error body missing requestId")
	}
	return e.Error.Code
}

func TestGetNode(t *testing.T) {
	h, _ := newTestAPI(t)
	rec := do(h, http.MethodGet, "/v1/node", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var s state.SelfInfo
	_ = json.Unmarshal(rec.Body.Bytes(), &s)
	if s.ID != "node1" || s.Status != state.StatusActive {
		t.Fatalf("unexpected %+v", s)
	}
	if rec.Header().Get("X-Request-ID") == "" || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("missing common headers")
	}
}

func TestSetStatusValid(t *testing.T) {
	h, reg := newTestAPI(t)
	rec := do(h, http.MethodPost, "/v1/node/status", "application/json", `{"status":"draining","reason":"upgrade"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if got := reg.Self(); got.Status != state.StatusDraining || got.Reason != "upgrade" {
		t.Fatalf("state not updated: %+v", got)
	}
}

// Every rejected request must leave state exactly as it was.
func TestSetStatusRejections(t *testing.T) {
	cases := []struct {
		name, ct, body string
		wantStatus     int
		wantCode       string
	}{
		{"wrong media type", "text/plain", `{"status":"draining"}`, 415, "unsupported_media_type"},
		{"missing media type", "", `{"status":"draining"}`, 415, "unsupported_media_type"},
		{"malformed json", "application/json", `{"status":`, 400, "invalid_json"},
		{"empty body", "application/json", ``, 400, "empty_body"},
		{"unknown field", "application/json", `{"status":"draining","admin":true}`, 400, "unknown_field"},
		{"wrong type", "application/json", `{"status":42}`, 400, "invalid_type"},
		{"two objects", "application/json", `{"status":"draining"}{"status":"active"}`, 400, "invalid_json"},
		{"invalid value", "application/json", `{"status":"sleeping"}`, 422, "invalid_value"},
		{"too large", "application/json", `{"status":"draining","reason":"` + strings.Repeat("x", 300) + `"}`, 413, "body_too_large"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, reg := newTestAPI(t)
			before := reg.Self()
			rec := do(h, http.MethodPost, "/v1/node/status", tc.ct, tc.body)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, tc.wantStatus, rec.Body)
			}
			if code := errorCode(t, rec); code != tc.wantCode {
				t.Fatalf("code = %q, want %q", code, tc.wantCode)
			}
			if after := reg.Self(); after != before {
				t.Fatalf("state changed on rejected request: %+v -> %+v", before, after)
			}
		})
	}
}

func TestUnknownRouteAndMethodGiveJSON(t *testing.T) {
	h, _ := newTestAPI(t)
	if rec := do(h, http.MethodGet, "/nope", "", ""); rec.Code != 404 || errorCode(t, rec) != "not_found" {
		t.Fatalf("404 case: %d %s", rec.Code, rec.Body)
	}
	rec := do(h, http.MethodDelete, "/v1/node", "", "")
	if rec.Code != 405 || errorCode(t, rec) != "method_not_allowed" {
		t.Fatalf("405 case: %d %s", rec.Code, rec.Body)
	}
	if rec.Header().Get("Allow") == "" {
		t.Fatal("405 must carry an Allow header")
	}
}

func TestClientRequestIDIsEchoedOnlyIfSafe(t *testing.T) {
	h, _ := newTestAPI(t)
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Header.Set("X-Request-ID", "abc-123")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if got := rec.Header().Get("X-Request-ID"); got != "abc-123" {
		t.Fatalf("got %q", got)
	}

	req.Header.Set("X-Request-ID", "bad id\nwith newline")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if got := rec.Header().Get("X-Request-ID"); got == "" || strings.ContainsAny(got, " \n") {
		t.Fatalf("unsafe id echoed: %q", got)
	}
}
