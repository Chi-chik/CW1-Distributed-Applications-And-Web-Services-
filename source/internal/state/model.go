package state

import (
	"fmt"
	"net"
	"regexp"
	"strconv"
	"time"
)

// Limits are explicit so tests, docs and validation agree on one number.
const (
	MaxPeers     = 256
	MaxFiles     = 10_000
	MaxFileSize  = 8 << 20 // 8 MiB: small synthetic files only
	MaxNameLen   = 128
	MaxReasonLen = 200
)

var (
	// IDPattern is shared with config: DNS-safe, so IDs double as hostnames.
	IDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
	// namePattern forbids '/', '\', leading dots and '..', which rules out path
	// traversal by construction rather than by trying to clean bad input.
	namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	sha256Hex   = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// Addresses are the service endpoints a peer advertises. Any may be empty,
// but a peer must advertise at least one.
type Addresses struct {
	HTTP string `json:"http,omitempty"`
	TCP  string `json:"tcp,omitempty"`
	UDP  string `json:"udp,omitempty"`
	GRPC string `json:"grpc,omitempty"`
}

// Peer is another node this node knows about.
type Peer struct {
	ID        string    `json:"id"`
	Addrs     Addresses `json:"addrs"`
	Source    string    `json:"source"` // "static", "api" or "gossip"
	AddedAt   time.Time `json:"addedAt"`
	LastSeen  time.Time `json:"lastSeen"`
	ExpiresAt time.Time `json:"expiresAt,omitempty"` // zero for static peers
}

// FileMeta describes one file held by a node.
type FileMeta struct {
	Name      string    `json:"name"`
	Size      int64     `json:"size"`
	SHA256    string    `json:"sha256"`
	Owner     string    `json:"owner"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// ValidateName reports whether name is a safe flat file name.
func ValidateName(name string) error {
	if !namePattern.MatchString(name) || containsDotDot(name) {
		return fmt.Errorf("%w: file name %q must match %s and not contain '..'", ErrInvalid, name, namePattern)
	}
	return nil
}

func containsDotDot(s string) bool {
	for i := 0; i+1 < len(s); i++ {
		if s[i] == '.' && s[i+1] == '.' {
			return true
		}
	}
	return false
}

// ValidatePeerAddr checks a remote address: host required, port 1-65535.
func ValidatePeerAddr(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("%w: address %q is not host:port", ErrInvalid, addr)
	}
	p, err := strconv.Atoi(port)
	if host == "" || err != nil || p < 1 || p > 65535 {
		return fmt.Errorf("%w: address %q needs a host and a port 1-65535", ErrInvalid, addr)
	}
	return nil
}

// Validate checks a peer record supplied from outside.
func (p Peer) Validate() error {
	if !IDPattern.MatchString(p.ID) {
		return fmt.Errorf("%w: peer id %q", ErrInvalid, p.ID)
	}
	addrs := []string{p.Addrs.HTTP, p.Addrs.TCP, p.Addrs.UDP, p.Addrs.GRPC}
	n := 0
	for _, a := range addrs {
		if a == "" {
			continue
		}
		n++
		if err := ValidatePeerAddr(a); err != nil {
			return err
		}
	}
	if n == 0 {
		return fmt.Errorf("%w: peer %q advertises no addresses", ErrInvalid, p.ID)
	}
	return nil
}

// Validate checks file metadata supplied from outside.
func (f FileMeta) Validate() error {
	if err := ValidateName(f.Name); err != nil {
		return err
	}
	if f.Size < 0 || f.Size > MaxFileSize {
		return fmt.Errorf("%w: size %d outside 0..%d", ErrInvalid, f.Size, MaxFileSize)
	}
	if !sha256Hex.MatchString(f.SHA256) {
		return fmt.Errorf("%w: sha256 must be 64 lowercase hex characters", ErrInvalid)
	}
	return nil
}
