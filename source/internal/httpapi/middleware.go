package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"regexp"
	"time"
)

type ctxKey int

const (
	keyRequestID ctxKey = iota
	keyLogger
)

var requestIDPattern = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)

func requestIDFrom(r *http.Request) string {
	id, _ := r.Context().Value(keyRequestID).(string)
	return id
}

func loggerFrom(r *http.Request) *slog.Logger {
	if l, ok := r.Context().Value(keyLogger).(*slog.Logger); ok {
		return l
	}
	return slog.Default()
}

func newRequestID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// statusRecorder captures the status code for the access log.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// withCommon wraps every handler with: request ID, security headers,
// panic recovery and one structured access-log line per request.
// Order matters: the request ID must exist before anything logs.
func withCommon(next http.Handler, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		id := r.Header.Get("X-Request-ID")
		if !requestIDPattern.MatchString(id) { // don't trust arbitrary client input into logs
			id = newRequestID()
		}
		reqLog := log.With("requestId", id)
		ctx := context.WithValue(r.Context(), keyRequestID, id)
		ctx = context.WithValue(ctx, keyLogger, reqLog)
		r = r.WithContext(ctx)

		h := w.Header()
		h.Set("X-Request-ID", id)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Content-Security-Policy", "default-src 'none'")
		h.Set("Cache-Control", "no-store")

		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		defer func() {
			if p := recover(); p != nil {
				reqLog.Error("panic in handler", "panic", p)
				writeError(rec, r, newAPIError(http.StatusInternalServerError, "internal", "internal server error"))
			}
			reqLog.Info("http request", "method", r.Method, "path", r.URL.Path,
				"status", rec.status, "durationMs", time.Since(start).Milliseconds())
		}()
		next.ServeHTTP(rec, r)
	})
}
