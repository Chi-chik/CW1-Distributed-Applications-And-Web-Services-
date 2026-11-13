package ipc

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"
)

// UDPServer answers one datagram with one datagram. There is no connection,
// no ordering and no delivery guarantee: the client owns retries.
type UDPServer struct {
	NodeID  string
	Handler Handler
	Log     *slog.Logger

	// Drop, if set, is called per received datagram; returning true discards
	// it silently. Used for fault-injection tests of the client retry policy.
	Drop func() bool

	pc     net.PacketConn
	once   sync.Once
	closed chan struct{}
	ctx    context.Context
	cancel context.CancelFunc
}

// NewUDPServer wraps an already-bound packet connection.
func NewUDPServer(pc net.PacketConn, nodeID string, h Handler, log *slog.Logger) *UDPServer {
	ctx, cancel := context.WithCancel(context.Background())
	return &UDPServer{NodeID: nodeID, Handler: h, Log: log, pc: pc, closed: make(chan struct{}), ctx: ctx, cancel: cancel}
}

// Addr is the bound address.
func (s *UDPServer) Addr() string { return s.pc.LocalAddr().String() }

// Serve handles datagrams sequentially until Shutdown. Sequential handling
// is a deliberate bound: a flood can't create unbounded goroutines.
func (s *UDPServer) Serve() error {
	// One byte larger than the limit: if a read fills it, the datagram was
	// oversize (the kernel truncates silently, so this is how we detect it).
	buf := make([]byte, MaxUDPPayload+1)
	for {
		n, from, err := s.pc.ReadFrom(buf)
		if err != nil {
			select {
			case <-s.closed:
				return nil
			default:
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			return err
		}
		if s.Drop != nil && s.Drop() {
			continue
		}
		if n > MaxUDPPayload {
			s.Log.Warn("udp oversize datagram dropped", "from", from.String())
			// No reply: answering garbage from unauthenticated sources is how
			// UDP amplification attacks work.
			continue
		}
		req, err := decode(buf[:n])
		if err != nil {
			s.Log.Warn("udp bad message", "from", from.String(), "err", err)
			// Error replies are always smaller than the request, so they
			// can't be used for amplification.
			s.reply(from, errorReply(req.ID, s.NodeID, codeFor(err), "malformed"))
			continue
		}
		ctx, cancel := context.WithTimeout(s.ctx, time.Second)
		resp, err := s.Handler(ctx, req)
		cancel()
		if err != nil {
			resp = errorReply(req.ID, s.NodeID, "handler_error", err.Error())
		}
		s.reply(from, resp)
	}
}

func (s *UDPServer) reply(to net.Addr, e Envelope) {
	b, err := encode(e)
	if err != nil || len(b) > MaxUDPPayload {
		s.Log.Warn("udp reply not sent", "err", err, "size", len(b))
		return
	}
	_ = s.pc.SetWriteDeadline(time.Now().Add(time.Second))
	if _, err := s.pc.WriteTo(b, to); err != nil {
		s.Log.Warn("udp write failed", "err", err)
	}
}

// Shutdown closes the socket, which unblocks ReadFrom.
func (s *UDPServer) Shutdown(context.Context) error {
	var err error
	s.once.Do(func() {
		close(s.closed)
		s.cancel()
		err = s.pc.Close()
	})
	return err
}

// UDPRequest sends req and waits for a reply with the same ID, retrying up to
// attempts times. Each attempt gets an equal share of the remaining time.
// Replies with a different ID (late answers to an earlier attempt from a
// previous call) are ignored. A retried request may be processed twice, so
// UDP request types must be idempotent (ping and heartbeat are).
func UDPRequest(ctx context.Context, addr string, req Envelope, attempts int) (Envelope, error) {
	if attempts < 1 {
		attempts = 1
	}
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
	if len(payload) > MaxUDPPayload {
		return Envelope{}, fmt.Errorf("%w: datagram %d > %d", ErrTooLarge, len(payload), MaxUDPPayload)
	}

	var d net.Dialer
	c, err := d.DialContext(ctx, "udp", addr) // "connected" UDP: kernel filters other senders
	if err != nil {
		return Envelope{}, fmt.Errorf("udp dial %s: %w", addr, err)
	}
	defer c.Close()
	stop := context.AfterFunc(ctx, func() { _ = c.SetDeadline(time.Unix(1, 0)) })
	defer stop()

	buf := make([]byte, MaxUDPPayload+1)
	var lastErr error
	for i := 0; i < attempts; i++ {
		if ctx.Err() != nil {
			break // don't let a fresh per-attempt deadline undo a cancellation
		}
		deadline, _ := ctx.Deadline()
		per := time.Until(deadline) / time.Duration(attempts-i)
		_ = c.SetDeadline(time.Now().Add(per))

		if _, err := c.Write(payload); err != nil {
			lastErr = err
			continue
		}
		for {
			n, err := c.Read(buf)
			if err != nil {
				lastErr = err
				break // timeout for this attempt: retry
			}
			if n > MaxUDPPayload {
				continue
			}
			resp, err := decode(buf[:n])
			if err != nil || resp.ID != req.ID {
				continue // not ours; keep waiting within this attempt
			}
			if resp.Type == TypeError {
				return resp, remoteErr(resp)
			}
			return resp, nil
		}
		if ctx.Err() != nil {
			break
		}
	}
	return Envelope{}, fmt.Errorf("udp request to %s failed after %d attempts: %w", addr, attempts, ctxErr(ctx, lastErr))
}
