package rpc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"p2pfs/gen/peerv1"
	"p2pfs/internal/state"
)

const hash = "b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9"

func startServer(t *testing.T, files ...string) (string, *state.Registry) {
	t.Helper()
	reg := state.NewRegistry("srv", nil)
	for _, f := range files {
		if _, err := reg.AddFile(state.FileMeta{Name: f, Size: 1, SHA256: hash}); err != nil {
			t.Fatal(err)
		}
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := NewGRPCServer(slog.New(slog.NewTextHandler(io.Discard, nil)))
	peerv1.RegisterPeerServiceServer(s, &Server{Reg: reg, Started: time.Now()})
	go s.Serve(ln)
	t.Cleanup(s.Stop)
	return ln.Addr().String(), reg
}

func client(t *testing.T, addr string) peerv1.PeerServiceClient {
	t.Helper()
	p := NewPool(nil)
	t.Cleanup(func() { p.Close() })
	c, err := p.Client(addr)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func ctx(t *testing.T) context.Context {
	c, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)
	return c
}

func TestGetNodeInfo(t *testing.T) {
	addr, _ := startServer(t, "a.txt")
	info, err := client(t, addr).GetNodeInfo(ctx(t), &peerv1.GetNodeInfoRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if info.GetNodeId() != "srv" || info.GetFileCount() != 1 || info.GetProtocolVersion() != 1 {
		t.Fatalf("%v", info)
	}
}

func TestListFilesPagesDeterministically(t *testing.T) {
	addr, _ := startServer(t, "e.txt", "b.txt", "d.txt", "a.txt", "c.txt")
	c := client(t, addr)

	var got []string
	token := ""
	pages := 0
	for {
		resp, err := c.ListFiles(ctx(t), &peerv1.ListFilesRequest{PageSize: 2, PageToken: token})
		if err != nil {
			t.Fatal(err)
		}
		pages++
		for _, f := range resp.GetFiles() {
			got = append(got, f.GetName())
		}
		if token = resp.GetNextPageToken(); token == "" {
			break
		}
	}
	if fmt.Sprint(got) != "[a.txt b.txt c.txt d.txt e.txt]" || pages != 3 {
		t.Fatalf("got %v in %d pages", got, pages)
	}

	all, nodeID, err := ListAllFiles(ctx(t), c, "", 10)
	if err != nil || len(all) != 5 || nodeID != "srv" {
		t.Fatalf("ListAllFiles: %d %q %v", len(all), nodeID, err)
	}
}

func TestListFilesPrefix(t *testing.T) {
	addr, _ := startServer(t, "log-1", "log-2", "img-1")
	resp, err := client(t, addr).ListFiles(ctx(t), &peerv1.ListFilesRequest{NamePrefix: "log-"})
	if err != nil || len(resp.GetFiles()) != 2 {
		t.Fatalf("%v %v", resp, err)
	}
}

func TestListFilesInvalidArguments(t *testing.T) {
	addr, _ := startServer(t)
	c := client(t, addr)
	bad := []*peerv1.ListFilesRequest{
		{PageSize: 5000},
		{NamePrefix: "../"},
		{PageToken: "../etc"},
	}
	for _, req := range bad {
		_, err := c.ListFiles(ctx(t), req)
		if status.Code(err) != codes.InvalidArgument {
			t.Errorf("%v: want InvalidArgument, got %v", req, err)
		}
	}
}

func TestUnavailablePeerFailsFastWithUnavailable(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()

	c := client(t, addr)
	start := time.Now()
	_, err := c.GetNodeInfo(ctx(t), &peerv1.GetNodeInfoRequest{})
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("want Unavailable, got %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("unavailable call was not bounded")
	}
}

func TestCallerDeadlineIsPropagated(t *testing.T) {
	addr, _ := startServer(t)
	c := client(t, addr)
	expired, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	time.Sleep(time.Millisecond)
	if _, err := c.GetNodeInfo(expired, &peerv1.GetNodeInfoRequest{}); status.Code(err) != codes.DeadlineExceeded {
		t.Fatalf("want DeadlineExceeded, got %v", err)
	}
}

func TestDomainErrorMapping(t *testing.T) {
	cases := map[error]codes.Code{
		fmt.Errorf("x: %w", state.ErrInvalid):           codes.InvalidArgument,
		fmt.Errorf("x: %w", state.ErrNotFound):          codes.NotFound,
		fmt.Errorf("x: %w", state.ErrConflict):          codes.AlreadyExists,
		fmt.Errorf("x: %w", context.DeadlineExceeded):   codes.DeadlineExceeded,
		errors.New("database password is hunter2"):      codes.Internal,
		status.Error(codes.PermissionDenied, "already"): codes.PermissionDenied,
	}
	for in, want := range cases {
		got := toStatus(in)
		if status.Code(got) != want {
			t.Errorf("%v -> %v, want %v", in, status.Code(got), want)
		}
		if want == codes.Internal && status.Convert(got).Message() != "internal error" {
			t.Error("internal error details leaked to caller")
		}
	}
}

func TestPoolReusesConnectionsAndRefusesAfterClose(t *testing.T) {
	addr, _ := startServer(t)
	p := NewPool(nil)
	p.Client(addr)
	p.Client(addr)
	if len(p.conns) != 1 {
		t.Fatalf("want 1 cached conn, got %d", len(p.conns))
	}
	p.Close()
	if _, err := p.Client(addr); !errors.Is(err, ErrPoolClosed) {
		t.Fatalf("want ErrPoolClosed, got %v", err)
	}
}
