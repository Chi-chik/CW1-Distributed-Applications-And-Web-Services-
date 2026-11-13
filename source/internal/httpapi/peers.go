package httpapi

import (
	"net/http"

	"p2pfs/internal/state"
)

// PeerRequest is the body for POST /v1/peers. Server-owned fields
// (addedAt, lastSeen, source) are deliberately absent: a client can't set them,
// and DisallowUnknownFields rejects any attempt to.
type PeerRequest struct {
	ID    string          `json:"id"`
	Addrs state.Addresses `json:"addrs"`
}

// FileRequest is the body for POST /v1/files (metadata only in CW1).
type FileRequest struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type peerList struct {
	Count int          `json:"count"`
	Peers []state.Peer `json:"peers"`
}

type fileList struct {
	Count int              `json:"count"`
	Files []state.FileMeta `json:"files"`
}

func (a *api) listPeers(w http.ResponseWriter, r *http.Request) {
	ps := a.Registry.Peers()
	writeJSON(w, http.StatusOK, peerList{Count: len(ps), Peers: ps})
}

func (a *api) addPeer(w http.ResponseWriter, r *http.Request) {
	var req PeerRequest
	if err := decodeJSON(w, r, &req, a.MaxBodyBytes); err != nil {
		writeError(w, r, err)
		return
	}
	p, err := a.Registry.AddPeer(state.Peer{ID: req.ID, Addrs: req.Addrs, Source: "api"})
	if err != nil {
		writeError(w, r, err)
		return
	}
	loggerFrom(r).Info("peer added", "peer", p.ID)
	w.Header().Set("Location", "/v1/peers/"+p.ID)
	writeJSON(w, http.StatusCreated, p)
}

func (a *api) getPeer(w http.ResponseWriter, r *http.Request) {
	p, err := a.Registry.Peer(r.PathValue("id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (a *api) deletePeer(w http.ResponseWriter, r *http.Request) {
	if err := a.Registry.RemovePeer(r.PathValue("id")); err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *api) listFiles(w http.ResponseWriter, r *http.Request) {
	fs := a.Registry.Files()
	writeJSON(w, http.StatusOK, fileList{Count: len(fs), Files: fs})
}

func (a *api) addFile(w http.ResponseWriter, r *http.Request) {
	var req FileRequest
	if err := decodeJSON(w, r, &req, a.MaxBodyBytes); err != nil {
		writeError(w, r, err)
		return
	}
	f, err := a.Registry.AddFile(state.FileMeta{Name: req.Name, Size: req.Size, SHA256: req.SHA256})
	if err != nil {
		writeError(w, r, err)
		return
	}
	w.Header().Set("Location", "/v1/files/"+f.Name)
	writeJSON(w, http.StatusCreated, f)
}

func (a *api) getFile(w http.ResponseWriter, r *http.Request) {
	f, err := a.Registry.File(r.PathValue("name"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, f)
}

func (a *api) getState(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.Registry.Snapshot())
}
