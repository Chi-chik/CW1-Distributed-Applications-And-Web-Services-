// Package httpapi is the REST transport. Handlers translate HTTP to calls on
// the node's state and back; they never own state themselves.
package httpapi

import (
	"encoding/json"
	"net/http"
	"time"
)

// Health is the JSON body returned by GET /healthz.
type Health struct {
	Status        string `json:"status"`
	NodeID        string `json:"nodeId"`
	UptimeSeconds int64  `json:"uptimeSeconds"`
	Time          string `json:"time"`
}

// HealthHandler reports liveness. now is injected so tests are deterministic.
func HealthHandler(nodeID string, started time.Time, now func() time.Time) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		t := now()
		writeJSON(w, http.StatusOK, Health{
			Status:        "ok",
			NodeID:        nodeID,
			UptimeSeconds: int64(t.Sub(started).Seconds()),
			Time:          t.UTC().Format(time.RFC3339),
		})
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v) // the status is already sent; nothing useful to do on error
}
