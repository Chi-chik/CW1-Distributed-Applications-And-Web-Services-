package rpc

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"

	"p2pfs/gen/peerv1"
)

// DefaultCallTimeout bounds a remote call when the caller set no deadline.
const DefaultCallTimeout = 3 * time.Second

// Pool keeps one *grpc.ClientConn per peer address. A ClientConn is
// expensive to create (HTTP/2 connection, possibly a TLS handshake) and safe
// for concurrent use, so it should be shared, not created per call.
type Pool struct {
	creds credentials.TransportCredentials

	mu     sync.Mutex
	conns  map[string]*grpc.ClientConn
	closed bool
}

// NewPool creates a pool. nil creds means plaintext (CW1 only: Week 9
// replaces this with mutual TLS).
func NewPool(creds credentials.TransportCredentials) *Pool {
	if creds == nil {
		creds = insecure.NewCredentials()
	}
	return &Pool{creds: creds, conns: make(map[string]*grpc.ClientConn)}
}

// ErrPoolClosed is returned after Close.
var ErrPoolClosed = errors.New("rpc pool closed")

// Client returns a PeerService client for addr, creating the connection
// lazily. grpc.NewClient doesn't dial; the first RPC does.
func (p *Pool) Client(addr string) (peerv1.PeerServiceClient, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil, ErrPoolClosed
	}
	if cc, ok := p.conns[addr]; ok {
		return peerv1.NewPeerServiceClient(cc), nil
	}
	cc, err := grpc.NewClient(addr,
		grpc.WithTransportCredentials(p.creds),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(MaxMsgBytes), grpc.MaxCallSendMsgSize(MaxMsgBytes)),
	)
	if err != nil {
		return nil, fmt.Errorf("grpc client %s: %w", addr, err)
	}
	p.conns[addr] = cc
	return peerv1.NewPeerServiceClient(cc), nil
}

// Forget drops (and closes) the cached connection for addr, e.g. after a
// peer has been removed.
func (p *Pool) Forget(addr string) {
	p.mu.Lock()
	cc := p.conns[addr]
	delete(p.conns, addr)
	p.mu.Unlock()
	if cc != nil {
		_ = cc.Close()
	}
}

// Close closes every connection. Safe to call more than once.
func (p *Pool) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	var errs []error
	for addr, cc := range p.conns {
		errs = append(errs, cc.Close())
		delete(p.conns, addr)
	}
	return errors.Join(errs...)
}

// ListAllFiles follows page tokens to collect a peer's whole file list.
// maxPages bounds the loop, so a misbehaving peer that always returns a
// next token can't keep us here for ever.
func ListAllFiles(ctx context.Context, c peerv1.PeerServiceClient, prefix string, maxPages int) ([]*peerv1.FileMetadata, string, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, DefaultCallTimeout)
		defer cancel()
	}
	var all []*peerv1.FileMetadata
	var nodeID, token string
	for page := 0; page < maxPages; page++ {
		resp, err := c.ListFiles(ctx, &peerv1.ListFilesRequest{NamePrefix: prefix, PageSize: 500, PageToken: token})
		if err != nil {
			return nil, "", err
		}
		nodeID = resp.GetNodeId()
		all = append(all, resp.GetFiles()...)
		if token = resp.GetNextPageToken(); token == "" {
			return all, nodeID, nil
		}
	}
	return nil, "", fmt.Errorf("peer returned more than %d pages", maxPages)
}
