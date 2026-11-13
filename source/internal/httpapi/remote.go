package httpapi

import (
	"context"
	"net/http"

	"p2pfs/internal/state"
)

// PeerOps is what the REST layer needs to reach *other* nodes. It's an
// interface so httpapi stays independent of gRPC and raw sockets, and so
// tests can substitute a fake.
type PeerOps interface {
	ProbePeer(ctx context.Context, id string) (ProbeResult, error)
	RemoteFiles(ctx context.Context, id string) ([]state.FileMeta, error)
}

// TransportResult is the outcome of contacting a peer over one transport.
type TransportResult struct {
	OK        bool    `json:"ok"`
	RTTMillis float64 `json:"rttMs"`
	Error     string  `json:"error,omitempty"`
}

// ProbeResult reports every transport a peer advertises.
type ProbeResult struct {
	PeerID string           `json:"peerId"`
	TCP    *TransportResult `json:"tcp,omitempty"`
	UDP    *TransportResult `json:"udp,omitempty"`
	GRPC   *TransportResult `json:"grpc,omitempty"`
}

// AnyOK reports whether at least one transport succeeded.
func (p ProbeResult) AnyOK() bool {
	for _, r := range []*TransportResult{p.TCP, p.UDP, p.GRPC} {
		if r != nil && r.OK {
			return true
		}
	}
	return false
}

type remoteFileList struct {
	PeerID string           `json:"peerId"`
	Count  int              `json:"count"`
	Files  []state.FileMeta `json:"files"`
}

// probePeer: POST, not GET, because it has side effects (network traffic,
// lastSeen updates) and must never be cached or prefetched.
func (a *api) probePeer(w http.ResponseWriter, r *http.Request) {
	res, err := a.Peers.ProbePeer(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (a *api) remoteFiles(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	files, err := a.Peers.RemoteFiles(r.Context(), id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, remoteFileList{PeerID: id, Count: len(files), Files: files})
}
