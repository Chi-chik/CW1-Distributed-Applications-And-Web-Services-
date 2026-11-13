// Package state holds the node's shared mutable state. It is the single owner:
// transports (REST, TCP, UDP, gRPC) call its methods and never touch the data
// directly. Every method takes the lock, so callers need no synchronisation.
//
// Ownership rule: maps live only inside Registry and are guarded by mu.
// Values go in and come out by copy, so no caller can hold a reference into
// shared state. Read methods return sorted slices, so output is deterministic.
package state

import (
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

// Sentinel errors let transports map failures to protocol-specific codes
// (HTTP 422/404/409, gRPC InvalidArgument/NotFound/AlreadyExists).
var (
	ErrInvalid  = errors.New("invalid")
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("conflict")
	// ErrUnavailable means a remote peer could not be reached.
	ErrUnavailable = errors.New("peer unavailable")
)

// NodeStatus is the operator-controlled mode of this node.
type NodeStatus string

const (
	StatusActive      NodeStatus = "active"
	StatusDraining    NodeStatus = "draining"
	StatusMaintenance NodeStatus = "maintenance"
)

func (s NodeStatus) valid() bool {
	switch s {
	case StatusActive, StatusDraining, StatusMaintenance:
		return true
	}
	return false
}

// SelfInfo describes this node.
type SelfInfo struct {
	ID        string     `json:"id"`
	Status    NodeStatus `json:"status"`
	Reason    string     `json:"reason,omitempty"`
	UpdatedAt time.Time  `json:"updatedAt"`
}

// Snapshot is an immutable, deterministic copy of the whole state.
type Snapshot struct {
	Self  SelfInfo   `json:"self"`
	Peers []Peer     `json:"peers"`
	Files []FileMeta `json:"files"`
}

// Registry is the node's state owner.
type Registry struct {
	mu    sync.RWMutex
	now   func() time.Time
	self  SelfInfo
	peers map[string]Peer
	files map[string]FileMeta
}

// NewRegistry creates state for the node with the given ID.
func NewRegistry(nodeID string, now func() time.Time) *Registry {
	if now == nil {
		now = time.Now
	}
	return &Registry{
		now:   now,
		self:  SelfInfo{ID: nodeID, Status: StatusActive, UpdatedAt: now().UTC()},
		peers: make(map[string]Peer),
		files: make(map[string]FileMeta),
	}
}

// ---- self ----

// Self returns a copy of this node's info.
func (r *Registry) Self() SelfInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.self
}

// SetStatus validates first, then writes, so a bad request changes nothing.
func (r *Registry) SetStatus(status NodeStatus, reason string) (SelfInfo, error) {
	if !status.valid() {
		return SelfInfo{}, fmt.Errorf("%w: status %q must be active, draining or maintenance", ErrInvalid, status)
	}
	if len(reason) > MaxReasonLen {
		return SelfInfo{}, fmt.Errorf("%w: reason longer than %d characters", ErrInvalid, MaxReasonLen)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.self.Status = status
	r.self.Reason = reason
	r.self.UpdatedAt = r.now().UTC()
	return r.self, nil
}

// ---- peers ----

// AddPeer inserts a new peer. Duplicates are a conflict, not a silent update:
// the API caller should know they're about to replace something.
func (r *Registry) AddPeer(p Peer) (Peer, error) {
	if err := p.Validate(); err != nil {
		return Peer{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if p.ID == r.self.ID {
		return Peer{}, fmt.Errorf("%w: a node cannot add itself as a peer", ErrInvalid)
	}
	if _, exists := r.peers[p.ID]; exists {
		return Peer{}, fmt.Errorf("%w: peer %q already registered", ErrConflict, p.ID)
	}
	if len(r.peers) >= MaxPeers {
		return Peer{}, fmt.Errorf("%w: peer table full (%d)", ErrConflict, MaxPeers)
	}
	now := r.now().UTC()
	p.AddedAt, p.LastSeen = now, now
	if p.Source == "" {
		p.Source = "api"
	}
	r.peers[p.ID] = p
	return p, nil
}

// TouchPeer records that we heard from a peer, extending its expiry.
func (r *Registry) TouchPeer(id string, ttl time.Duration) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.peers[id]
	if !ok {
		return fmt.Errorf("%w: peer %q", ErrNotFound, id)
	}
	p.LastSeen = r.now().UTC()
	if ttl > 0 {
		p.ExpiresAt = p.LastSeen.Add(ttl)
	}
	r.peers[id] = p
	return nil
}

// ExpirePeers removes peers whose ExpiresAt has passed and returns their IDs
// (sorted). Peers with a zero ExpiresAt (static config) never expire.
func (r *Registry) ExpirePeers() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	var gone []string
	for id, p := range r.peers {
		if !p.ExpiresAt.IsZero() && now.After(p.ExpiresAt) {
			delete(r.peers, id)
			gone = append(gone, id)
		}
	}
	sort.Strings(gone)
	return gone
}

// RemovePeer deletes a peer.
func (r *Registry) RemovePeer(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.peers[id]; !ok {
		return fmt.Errorf("%w: peer %q", ErrNotFound, id)
	}
	delete(r.peers, id)
	return nil
}

// Peer returns one peer by ID.
func (r *Registry) Peer(id string) (Peer, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.peers[id]
	if !ok {
		return Peer{}, fmt.Errorf("%w: peer %q", ErrNotFound, id)
	}
	return p, nil
}

// Peers returns all peers sorted by ID. Go map iteration order is random
// on purpose, so sorting is what makes the output deterministic.
func (r *Registry) Peers() []Peer {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.peersLocked()
}

func (r *Registry) peersLocked() []Peer {
	out := make([]Peer, 0, len(r.peers))
	for _, p := range r.peers {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// ---- files ----

// AddFile registers metadata for a new file owned by this node.
func (r *Registry) AddFile(f FileMeta) (FileMeta, error) {
	if err := f.Validate(); err != nil {
		return FileMeta{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.files[f.Name]; exists {
		return FileMeta{}, fmt.Errorf("%w: file %q already registered", ErrConflict, f.Name)
	}
	if len(r.files) >= MaxFiles {
		return FileMeta{}, fmt.Errorf("%w: file table full (%d)", ErrConflict, MaxFiles)
	}
	f.Owner = r.self.ID
	f.UpdatedAt = r.now().UTC()
	r.files[f.Name] = f
	return f, nil
}

// File returns metadata for one file.
func (r *Registry) File(name string) (FileMeta, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	f, ok := r.files[name]
	if !ok {
		return FileMeta{}, fmt.Errorf("%w: file %q", ErrNotFound, name)
	}
	return f, nil
}

// Files returns all file metadata sorted by name.
func (r *Registry) Files() []FileMeta {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.filesLocked()
}

func (r *Registry) filesLocked() []FileMeta {
	out := make([]FileMeta, 0, len(r.files))
	for _, f := range r.files {
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Snapshot returns a consistent copy of everything under one read lock,
// so peers and files are from the same instant.
func (r *Registry) Snapshot() Snapshot {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return Snapshot{Self: r.self, Peers: r.peersLocked(), Files: r.filesLocked()}
}
