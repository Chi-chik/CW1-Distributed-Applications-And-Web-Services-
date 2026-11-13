package httpapi

import (
	"log/slog"
	"net/http"
	"time"

	"p2pfs/internal/state"
)

// DefaultMaxBodyBytes bounds every JSON request body.
const DefaultMaxBodyBytes = 64 << 10 // 64 KiB: metadata only, never file content

// Deps are the collaborators the REST layer needs. The router depends on
// the state owner; it holds no state of its own.
type Deps struct {
	NodeID       string
	Started      time.Time
	Now          func() time.Time
	Registry     *state.Registry
	Log          *slog.Logger
	MaxBodyBytes int64
}

type api struct{ Deps }

// NewRouter builds the full REST surface.
func NewRouter(d Deps) http.Handler {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.MaxBodyBytes <= 0 {
		d.MaxBodyBytes = DefaultMaxBodyBytes
	}
	a := &api{d}

	mux := http.NewServeMux()
	mux.Handle("GET /healthz", HealthHandler(d.NodeID, d.Started, d.Now))
	mux.HandleFunc("GET /v1/node", a.getNode)
	mux.HandleFunc("POST /v1/node/status", a.setStatus)
	mux.HandleFunc("GET /v1/state", a.getState)

	mux.HandleFunc("GET /v1/peers", a.listPeers)
	mux.HandleFunc("POST /v1/peers", a.addPeer)
	mux.HandleFunc("GET /v1/peers/{id}", a.getPeer)
	mux.HandleFunc("DELETE /v1/peers/{id}", a.deletePeer)

	mux.HandleFunc("GET /v1/files", a.listFiles)
	mux.HandleFunc("POST /v1/files", a.addFile)
	mux.HandleFunc("GET /v1/files/{name}", a.getFile)

	return withCommon(jsonMuxErrors(mux), d.Log)
}

// jsonMuxErrors rewrites the mux's built-in plain-text 404 and 405 replies
// into the standard JSON error body. A catch-all "/" route would not work:
// it would match every path and hide the mux's 405 Method Not Allowed.
func jsonMuxErrors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(&muxErrorWriter{ResponseWriter: w, r: r}, r)
	})
}

type muxErrorWriter struct {
	http.ResponseWriter
	r       *http.Request
	swallow bool
}

func (m *muxErrorWriter) WriteHeader(code int) {
	isPlain := m.Header().Get("Content-Type") == "text/plain; charset=utf-8"
	if isPlain && (code == http.StatusNotFound || code == http.StatusMethodNotAllowed) {
		m.swallow = true
		codeName := "not_found"
		if code == http.StatusMethodNotAllowed {
			codeName = "method_not_allowed"
		}
		writeError(m.ResponseWriter, m.r, newAPIError(code, codeName, "%s %s: %s", m.r.Method, m.r.URL.Path, http.StatusText(code)))
		return
	}
	m.ResponseWriter.WriteHeader(code)
}

func (m *muxErrorWriter) Write(b []byte) (int, error) {
	if m.swallow {
		return len(b), nil // drop the mux's plain-text body
	}
	return m.ResponseWriter.Write(b)
}

func (a *api) getNode(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.Registry.Self())
}

// StatusRequest is the body for POST /v1/node/status.
type StatusRequest struct {
	Status state.NodeStatus `json:"status"`
	Reason string           `json:"reason"`
}

func (a *api) setStatus(w http.ResponseWriter, r *http.Request) {
	var req StatusRequest
	if err := decodeJSON(w, r, &req, a.MaxBodyBytes); err != nil {
		writeError(w, r, err)
		return
	}
	self, err := a.Registry.SetStatus(req.Status, req.Reason)
	if err != nil {
		writeError(w, r, err)
		return
	}
	loggerFrom(r).Info("node status changed", "status", self.Status)
	writeJSON(w, http.StatusOK, self)
}
