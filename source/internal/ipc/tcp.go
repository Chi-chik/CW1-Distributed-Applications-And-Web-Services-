package ipc

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"
)

// Handler processes one validated request and returns the reply.
// Returning an error sends a TypeError reply.
type Handler func(ctx context.Context, req Envelope) (Envelope, error)

// ---- framing ----

// writeFrame sends [uint32 big-endian length][payload].
func writeFrame(w io.Writer, payload []byte) error {
	if len(payload) > MaxTCPFrame {
		return fmt.Errorf("%w: frame %d > %d", ErrTooLarge, len(payload), MaxTCPFrame)
	}
	buf := make([]byte, 4+len(payload)) // one Write, so header and body can't be split by another writer
	binary.BigEndian.PutUint32(buf, uint32(len(payload)))
	copy(buf[4:], payload)
	_, err := w.Write(buf)
	return err
}

// readFrame reads one frame. The length is checked *before* allocating,
// so a hostile 4 GiB header costs us 4 bytes, not 4 GiB.
func readFrame(r io.Reader, max int) ([]byte, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n == 0 {
		return nil, fmt.Errorf("%w: empty frame", ErrMalformed)
	}
	if n > uint32(max) {
		return nil, fmt.Errorf("%w: frame %d > %d", ErrTooLarge, n, max)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, fmt.Errorf("%w: truncated frame: %v", ErrMalformed, err)
	}
	return buf, nil
}

// ---- server ----

// TCPServer serves framed envelopes. Connections are persistent: a client
// may send many requests on one connection, each answered in order.
type TCPServer struct {
	NodeID      string
	Handler     Handler
	Log         *slog.Logger
	IdleTimeout time.Duration // max wait for the next request on a connection
	MaxConns    int

	ln     net.Listener
	mu     sync.Mutex
	conns  map[net.Conn]struct{}
	closed bool
	wg     sync.WaitGroup
	ctx    context.Context
	cancel context.CancelFunc
}

// NewTCPServer wraps an already-bound listener.
func NewTCPServer(ln net.Listener, nodeID string, h Handler, log *slog.Logger) *TCPServer {
	ctx, cancel := context.WithCancel(context.Background())
	return &TCPServer{
		NodeID: nodeID, Handler: h, Log: log, IdleTimeout: 30 * time.Second, MaxConns: 64,
		ln: ln, conns: make(map[net.Conn]struct{}), ctx: ctx, cancel: cancel,
	}
}

// Addr is the bound address.
func (s *TCPServer) Addr() string { return s.ln.Addr().String() }

// Serve accepts until Shutdown. It returns nil after a clean shutdown.
func (s *TCPServer) Serve() error {
	for {
		c, err := s.ln.Accept()
		if err != nil {
			if s.isClosed() {
				return nil
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			return err
		}
		if !s.track(c) {
			s.Log.Warn("tcp connection refused: limit reached", "remote", c.RemoteAddr().String())
			c.Close()
			continue
		}
		s.wg.Add(1)
		go s.handleConn(c)
	}
}

func (s *TCPServer) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func (s *TCPServer) track(c net.Conn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || len(s.conns) >= s.MaxConns {
		return false
	}
	s.conns[c] = struct{}{}
	return true
}

func (s *TCPServer) untrack(c net.Conn) {
	s.mu.Lock()
	delete(s.conns, c)
	s.mu.Unlock()
}

func (s *TCPServer) handleConn(c net.Conn) {
	defer s.wg.Done()
	defer s.untrack(c)
	defer c.Close()
	remote := c.RemoteAddr().String()

	for {
		_ = c.SetReadDeadline(time.Now().Add(s.IdleTimeout))
		payload, err := readFrame(c, MaxTCPFrame)
		if err != nil {
			if errors.Is(err, io.EOF) || s.isClosed() {
				return // client hung up, or we're shutting down
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				return // idle connection reclaimed
			}
			// Framing is broken: we can't find the next message boundary,
			// so reply once (best effort) and drop the connection.
			s.Log.Warn("tcp bad frame", "remote", remote, "err", err)
			s.send(c, errorReply("unknown", s.NodeID, codeFor(err), err.Error()))
			return
		}

		req, err := decode(payload)
		if err != nil {
			// The frame was well delimited, so the stream is still in sync:
			// reply with an error and keep the connection.
			s.Log.Warn("tcp bad message", "remote", remote, "err", err)
			s.send(c, errorReply(req.ID, s.NodeID, codeFor(err), err.Error()))
			continue
		}

		ctx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
		resp, err := s.Handler(ctx, req)
		cancel()
		if err != nil {
			resp = errorReply(req.ID, s.NodeID, "handler_error", err.Error())
		}
		if !s.send(c, resp) {
			return
		}
	}
}

func (s *TCPServer) send(c net.Conn, e Envelope) bool {
	b, err := encode(e)
	if err == nil {
		_ = c.SetWriteDeadline(time.Now().Add(5 * time.Second))
		err = writeFrame(c, b)
	}
	if err != nil {
		s.Log.Warn("tcp write failed", "err", err)
		return false
	}
	return true
}

// Shutdown stops accepting, closes every open connection and waits for the
// handlers to finish (or ctx to expire). No goroutines or sockets leak.
func (s *TCPServer) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	s.closed = true
	err := s.ln.Close()
	for c := range s.conns {
		c.Close() // unblocks any goroutine sitting in Read
	}
	s.mu.Unlock()
	s.cancel()

	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
		return err
	case <-ctx.Done():
		return fmt.Errorf("tcp shutdown: %w", ctx.Err())
	}
}

func codeFor(err error) string {
	switch {
	case errors.Is(err, ErrTooLarge):
		return "too_large"
	case errors.Is(err, ErrMalformed):
		return "malformed"
	}
	return "error"
}

// ---- client ----

// DefaultTimeout applies when the caller's context has no deadline.
const DefaultTimeout = 3 * time.Second

// TCPRequest dials addr, sends req, and waits for the matching reply.
// The context's deadline (or DefaultTimeout) bounds dial, write and read.
func TCPRequest(ctx context.Context, addr string, req Envelope) (Envelope, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, DefaultTimeout)
		defer cancel()
	}
	if err := req.Validate(); err != nil {
		return Envelope{}, err
	}
	payload, err := encode(req)
	if err != nil {
		return Envelope{}, err
	}

	var d net.Dialer
	c, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return Envelope{}, fmt.Errorf("tcp dial %s: %w", addr, err)
	}
	defer c.Close()

	deadline, _ := ctx.Deadline()
	_ = c.SetDeadline(deadline)
	// Cancellation (not just deadline): if ctx is cancelled mid-read,
	// force the blocked Read to return immediately.
	stop := context.AfterFunc(ctx, func() { _ = c.SetDeadline(time.Unix(1, 0)) })
	defer stop()

	if err := writeFrame(c, payload); err != nil {
		return Envelope{}, fmt.Errorf("tcp write: %w", ctxErr(ctx, err))
	}
	b, err := readFrame(c, MaxTCPFrame)
	if err != nil {
		return Envelope{}, fmt.Errorf("tcp read: %w", ctxErr(ctx, err))
	}
	resp, err := decode(b)
	if err != nil {
		return Envelope{}, err
	}
	if resp.Type == TypeError {
		return resp, remoteErr(resp)
	}
	if resp.ID != req.ID {
		return Envelope{}, fmt.Errorf("%w: reply id %q != request id %q", ErrMalformed, resp.ID, req.ID)
	}
	return resp, nil
}

// ctxErr reports context errors in preference to raw socket errors. The
// socket deadline and the context timer are two clocks: the socket can time
// out a few microseconds before ctx.Err() flips, so a timeout at or after
// the context deadline is reported as context.DeadlineExceeded.
func ctxErr(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		if d, ok := ctx.Deadline(); ok && !time.Now().Before(d.Add(-5*time.Millisecond)) {
			return context.DeadlineExceeded
		}
	}
	return err
}
