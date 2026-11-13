// Package state holds the node's shared mutable state. It is the single owner:
// transports (REST, TCP, UDP, gRPC) call its methods and never touch the data
// directly. Every method takes the lock, so callers need no synchronisation.
package state

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

// Sentinel errors let transports map failures to protocol-specific codes
// (HTTP 422/404/409, gRPC InvalidArgument/NotFound/AlreadyExists).
var (
	ErrInvalid  = errors.New("invalid")
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("already exists")
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

// SelfInfo describes this node. It is returned by value, so callers get a
// copy they can't use to mutate shared state behind the lock's back.
type SelfInfo struct {
	ID        string     `json:"id"`
	Status    NodeStatus `json:"status"`
	Reason    string     `json:"reason,omitempty"`
	UpdatedAt time.Time  `json:"updatedAt"`
}

// Registry is the node's state owner.
type Registry struct {
	mu   sync.RWMutex
	now  func() time.Time
	self SelfInfo
}

// NewRegistry creates state for the node with the given ID.
func NewRegistry(nodeID string, now func() time.Time) *Registry {
	if now == nil {
		now = time.Now
	}
	return &Registry{now: now, self: SelfInfo{ID: nodeID, Status: StatusActive, UpdatedAt: now().UTC()}}
}

// Self returns a snapshot of this node's info.
func (r *Registry) Self() SelfInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.self
}

// SetStatus validates and applies a status change. Validation happens before
// the write, so an invalid request can never leave state half-changed.
func (r *Registry) SetStatus(status NodeStatus, reason string) (SelfInfo, error) {
	if !status.valid() {
		return SelfInfo{}, fmt.Errorf("%w: status %q must be active, draining or maintenance", ErrInvalid, status)
	}
	if len(reason) > 200 {
		return SelfInfo{}, fmt.Errorf("%w: reason longer than 200 characters", ErrInvalid)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.self.Status = status
	r.self.Reason = reason
	r.self.UpdatedAt = r.now().UTC()
	return r.self, nil
}
