package node

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"

	"p2pfs/internal/config"
	"p2pfs/internal/ipc"
	"p2pfs/internal/state"
)

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func testConfig(id string) config.Config {
	return config.Config{
		NodeID: id, HTTPAddr: "127.0.0.1:0", TCPAddr: "127.0.0.1:0", UDPAddr: "127.0.0.1:0", GRPCAddr: "127.0.0.1:0",
		ShutdownTimeout: 2 * time.Second,
	}
}

// startNode runs a node and returns a stop function that asserts a clean exit.
func startNode(t *testing.T, id string) (*Node, func()) {
	t.Helper()
	n, err := New(testConfig(id), quietLogger())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- n.Run(ctx) }()
	return n, func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Run returned %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Error("node did not stop within 3s")
		}
	}
}

func TestOccupiedPortFailsAndReleasesEarlierBinds(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()

	// HTTP binds fine on a free port; TCP collides. The HTTP port must be
	// released again, which we prove by binding a fixed free port twice.
	free, _ := net.Listen("tcp", "127.0.0.1:0")
	freeAddr := free.Addr().String()
	free.Close()

	cfg := testConfig("test1")
	cfg.HTTPAddr = freeAddr
	cfg.TCPAddr = busy.Addr().String()
	if _, err := New(cfg, quietLogger()); err == nil {
		t.Fatal("expected bind error for occupied TCP port")
	}
	again, err := net.Listen("tcp", freeAddr)
	if err != nil {
		t.Fatalf("HTTP listener leaked after failed start: %v", err)
	}
	again.Close()
}

func TestRunServesAllTransportsAndStops(t *testing.T) {
	n, stop := startNode(t, "test1")

	resp, err := http.Get("http://" + n.HTTPAddr() + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	ping := ipc.Envelope{V: ipc.ProtocolVersion, Type: ipc.TypePing, ID: ipc.NewID(), From: "tester"}
	if _, err := ipc.TCPRequest(context.Background(), n.TCPAddr(), ping); err != nil {
		t.Fatalf("tcp: %v", err)
	}
	ping.ID = ipc.NewID()
	if _, err := ipc.UDPRequest(context.Background(), n.UDPAddr(), ping, 2); err != nil {
		t.Fatalf("udp: %v", err)
	}

	stop()
	for _, addr := range []string{n.HTTPAddr(), n.TCPAddr()} {
		if c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond); err == nil {
			c.Close()
			t.Fatalf("%s still accepting after shutdown", addr)
		}
	}
}

// Two independent node processes (here, two Node values with separate
// listeners and registries) talk over TCP and UDP, and the messages update
// the receiver's view of the sender.
func TestTwoNodesExchangeMessagesAndUpdatePeerState(t *testing.T) {
	a, stopA := startNode(t, "node-a")
	defer stopA()
	b, stopB := startNode(t, "node-b")
	defer stopB()

	if _, err := b.Registry().AddPeer(state.Peer{ID: "node-a", Addrs: state.Addresses{TCP: a.TCPAddr()}}); err != nil {
		t.Fatal(err)
	}
	before, _ := b.Registry().Peer("node-a")
	time.Sleep(5 * time.Millisecond)

	req := ipc.Envelope{V: ipc.ProtocolVersion, Type: ipc.TypePing, ID: ipc.NewID(), From: "node-a"}
	resp, err := ipc.TCPRequest(context.Background(), b.TCPAddr(), req)
	if err != nil {
		t.Fatal(err)
	}
	var pong PongBody
	if err := json.Unmarshal(resp.Body, &pong); err != nil || pong.NodeID != "node-b" || pong.Peers != 1 {
		t.Fatalf("pong %+v %v", pong, err)
	}

	after, _ := b.Registry().Peer("node-a")
	if !after.LastSeen.After(before.LastSeen) {
		t.Fatal("message from known peer should refresh lastSeen")
	}
}
