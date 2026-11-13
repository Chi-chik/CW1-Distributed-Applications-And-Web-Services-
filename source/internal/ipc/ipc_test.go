package ipc

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// echoHandler answers ping with pong and echo with echo_reply.
func echoHandler(ctx context.Context, req Envelope) (Envelope, error) {
	switch req.Type {
	case TypePing, TypeHeartbeat:
		return Reply(req, "server", TypePong, map[string]string{"ok": "yes"})
	case TypeEcho:
		return Envelope{V: ProtocolVersion, Type: TypeEchoReply, ID: req.ID, From: "server", Body: req.Body}, nil
	}
	return Envelope{}, errors.New("unsupported type")
}

func ping() Envelope {
	return Envelope{V: ProtocolVersion, Type: TypePing, ID: NewID(), From: "client"}
}

func startTCP(t *testing.T) *TCPServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := NewTCPServer(ln, "server", echoHandler, quiet)
	go s.Serve()
	t.Cleanup(func() { s.Shutdown(context.Background()) })
	return s
}

func startUDP(t *testing.T) *UDPServer {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := NewUDPServer(pc, "server", echoHandler, quiet)
	return s
}

// ---- framing ----

func TestFrameRoundTripAndLimits(t *testing.T) {
	var buf bytes.Buffer
	if err := writeFrame(&buf, []byte(`{"a":1}`)); err != nil {
		t.Fatal(err)
	}
	got, err := readFrame(&buf, MaxTCPFrame)
	if err != nil || string(got) != `{"a":1}` {
		t.Fatalf("round trip: %q %v", got, err)
	}

	// A header claiming 1 GiB must be rejected without allocating it.
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], 1<<30)
	if _, err := readFrame(bytes.NewReader(hdr[:]), MaxTCPFrame); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("oversize header: %v", err)
	}
	// Header promises 10 bytes, stream delivers 3.
	binary.BigEndian.PutUint32(hdr[:], 10)
	if _, err := readFrame(io.MultiReader(bytes.NewReader(hdr[:]), strings.NewReader("abc")), MaxTCPFrame); !errors.Is(err, ErrMalformed) {
		t.Fatalf("truncated: %v", err)
	}
	if err := writeFrame(io.Discard, make([]byte, MaxTCPFrame+1)); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("writer limit: %v", err)
	}
}

func TestEnvelopeValidation(t *testing.T) {
	bad := []string{
		`{"v":2,"type":"ping","id":"a","from":"c"}`,          // future version
		`{"v":1,"type":"launch","id":"a","from":"c"}`,        // unknown type
		`{"v":1,"type":"ping","id":"a b","from":"c"}`,        // bad id
		`{"v":1,"type":"ping","id":"a","from":"C!"}`,         // bad sender
		`{"v":1,"type":"ping","id":"a","from":"c","x":1}`,    // unknown field
		`{"v":1,"type":"ping","id":"a","from":"c"} trailing`, // trailing data
		`not json`,
	}
	for _, b := range bad {
		if _, err := decode([]byte(b)); err == nil {
			t.Errorf("accepted %s", b)
		}
	}
}

// ---- TCP ----

func TestTCPRoundTrip(t *testing.T) {
	s := startTCP(t)
	req := ping()
	resp, err := TCPRequest(context.Background(), s.Addr(), req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Type != TypePong || resp.ID != req.ID {
		t.Fatalf("unexpected %+v", resp)
	}
}

func TestTCPMultipleMessagesOnOneConnection(t *testing.T) {
	s := startTCP(t)
	c, err := net.Dial("tcp", s.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for i := 0; i < 3; i++ {
		b, _ := encode(ping())
		writeFrame(c, b)
		if _, err := readFrame(c, MaxTCPFrame); err != nil {
			t.Fatalf("message %d: %v", i, err)
		}
	}
}

func TestTCPMalformedMessageGetsErrorAndConnectionSurvives(t *testing.T) {
	s := startTCP(t)
	c, _ := net.Dial("tcp", s.Addr())
	defer c.Close()

	writeFrame(c, []byte(`{"v":1,"type":"nope","id":"x1","from":"client"}`))
	b, err := readFrame(c, MaxTCPFrame)
	if err != nil {
		t.Fatal(err)
	}
	e, _ := decode(b)
	if e.Type != TypeError {
		t.Fatalf("want error reply, got %+v", e)
	}
	// Framing was intact, so the connection is still usable.
	p, _ := encode(ping())
	writeFrame(c, p)
	if _, err := readFrame(c, MaxTCPFrame); err != nil {
		t.Fatalf("connection should survive a bad message: %v", err)
	}
}

func TestTCPOversizeFrameClosesConnection(t *testing.T) {
	s := startTCP(t)
	c, _ := net.Dial("tcp", s.Addr())
	defer c.Close()
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], MaxTCPFrame+1)
	c.Write(hdr[:])

	b, err := readFrame(c, MaxTCPFrame)
	if err != nil {
		t.Fatal(err)
	}
	if e, _ := decode(b); e.Type != TypeError {
		t.Fatal("want error reply")
	}
	c.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := c.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("server should close after framing error, got %v", err)
	}
}

func TestTCPClientTimesOutAgainstSilentServer(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0") // accepts (via backlog) but never replies
	defer ln.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := TCPRequest(ctx, ln.Addr().String(), ping())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want deadline exceeded, got %v", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("timeout not honoured")
	}
}

func TestTCPClientCancellation(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	time.AfterFunc(100*time.Millisecond, cancel)
	start := time.Now()
	if _, err := TCPRequest(ctx, ln.Addr().String(), ping()); !errors.Is(err, context.Canceled) {
		t.Fatalf("want canceled, got %v", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("cancellation not honoured")
	}
}

func TestTCPUnavailablePeer(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close() // nothing listening now
	if _, err := TCPRequest(context.Background(), addr, ping()); err == nil {
		t.Fatal("expected dial error")
	}
}

func TestTCPShutdownClosesIdleConnections(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	s := NewTCPServer(ln, "server", echoHandler, quiet)
	served := make(chan error, 1)
	go func() { served <- s.Serve() }()

	c, _ := net.Dial("tcp", s.Addr()) // idle client holding a connection open
	defer c.Close()
	time.Sleep(50 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := s.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if err := <-served; err != nil {
		t.Fatalf("Serve returned %v", err)
	}
	if _, err := net.Dial("tcp", s.Addr()); err == nil {
		t.Fatal("listener leaked")
	}
}

// ---- UDP ----

func TestUDPRoundTrip(t *testing.T) {
	s := startUDP(t)
	go s.Serve()
	defer s.Shutdown(context.Background())

	req := ping()
	resp, err := UDPRequest(context.Background(), s.Addr(), req, 1)
	if err != nil || resp.ID != req.ID || resp.Type != TypePong {
		t.Fatalf("%+v %v", resp, err)
	}
}

func TestUDPRetrySurvivesDroppedDatagram(t *testing.T) {
	s := startUDP(t)
	var n atomic.Int32
	s.Drop = func() bool { return n.Add(1) == 1 } // fault injection: lose the first datagram
	go s.Serve()
	defer s.Shutdown(context.Background())

	ctx, cancel := context.WithTimeout(context.Background(), 900*time.Millisecond)
	defer cancel()
	if _, err := UDPRequest(ctx, s.Addr(), ping(), 3); err != nil {
		t.Fatalf("retry should recover from one loss: %v", err)
	}
	if n.Load() < 2 {
		t.Fatal("expected at least two datagrams")
	}
}

func TestUDPNoRetryFailsOnLoss(t *testing.T) {
	s := startUDP(t)
	s.Drop = func() bool { return true }
	go s.Serve()
	defer s.Shutdown(context.Background())

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := UDPRequest(ctx, s.Addr(), ping(), 1); err == nil {
		t.Fatal("expected failure when every datagram is lost")
	}
}

func TestUDPOversizeIsDroppedAndMalformedGetsError(t *testing.T) {
	s := startUDP(t)
	go s.Serve()
	defer s.Shutdown(context.Background())

	c, _ := net.Dial("udp", s.Addr())
	defer c.Close()
	c.SetDeadline(time.Now().Add(300 * time.Millisecond))

	c.Write(bytes.Repeat([]byte("x"), MaxUDPPayload+50))
	buf := make([]byte, 2048)
	if _, err := c.Read(buf); err == nil {
		t.Fatal("oversize datagram should get no reply")
	}

	c.SetDeadline(time.Now().Add(300 * time.Millisecond))
	c.Write([]byte(`{"v":1,"type":"ping"}`))
	n, err := c.Read(buf)
	if err != nil {
		t.Fatalf("malformed datagram should get an error reply: %v", err)
	}
	var e Envelope
	json.Unmarshal(buf[:n], &e)
	if e.Type != TypeError {
		t.Fatalf("got %+v", e)
	}
}

func TestUDPClientRejectsOversizeRequest(t *testing.T) {
	req := ping()
	req.Body = json.RawMessage(`"` + strings.Repeat("x", MaxUDPPayload) + `"`)
	if _, err := UDPRequest(context.Background(), "127.0.0.1:9", req, 1); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("want ErrTooLarge, got %v", err)
	}
}

func TestUDPShutdownUnblocksServe(t *testing.T) {
	s := startUDP(t)
	done := make(chan error, 1)
	go func() { done <- s.Serve() }()
	time.Sleep(20 * time.Millisecond)
	s.Shutdown(context.Background())
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Serve did not return after Shutdown")
	}
}
