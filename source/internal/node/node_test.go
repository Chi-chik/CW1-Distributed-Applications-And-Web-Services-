package node

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"

	"p2pfs/internal/config"
)

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func testConfig() config.Config {
	return config.Config{NodeID: "test1", HTTPAddr: "127.0.0.1:0", ShutdownTimeout: 2 * time.Second}
}

func TestOccupiedPortFailsAtStartup(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()

	cfg := testConfig()
	cfg.HTTPAddr = busy.Addr().String()
	if _, err := New(cfg, quietLogger()); err == nil {
		t.Fatal("expected bind error for occupied port")
	}
}

func TestRunServesAndStopsOnCancel(t *testing.T) {
	n, err := New(testConfig(), quietLogger())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- n.Run(ctx) }()

	resp, err := http.Get("http://" + n.HTTPAddr() + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("node did not stop within 3s")
	}

	// The listener must be released after shutdown.
	if _, err := http.Get("http://" + n.HTTPAddr() + "/healthz"); err == nil {
		t.Fatal("server still answering after shutdown")
	}
}
