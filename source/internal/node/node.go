// Package node owns the lifecycle of one peer: bind every listener up front,
// serve until the context is cancelled, then shut down within a deadline.
package node

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"google.golang.org/grpc"

	"p2pfs/gen/peerv1"
	"p2pfs/internal/config"
	"p2pfs/internal/httpapi"
	"p2pfs/internal/ipc"
	"p2pfs/internal/rpc"
	"p2pfs/internal/state"
)

// component is anything the node starts and stops.
type component struct {
	name  string
	serve func() error // blocks; returns nil after a clean stop
	stop  func(context.Context) error
}

// Node is one running peer.
type Node struct {
	cfg     config.Config
	log     *slog.Logger
	started time.Time
	reg     *state.Registry

	httpLn  net.Listener
	httpSrv *http.Server
	tcp     *ipc.TCPServer
	udp     *ipc.UDPServer
	grpcLn  net.Listener
	grpcSrv *grpc.Server
	pool    *rpc.Pool

	components []component
}

// New binds every listener immediately. If any bind fails, the ones already
// bound are closed before returning, so a failed start leaks nothing.
func New(cfg config.Config, log *slog.Logger) (n *Node, err error) {
	var closers []func() error
	defer func() {
		if err != nil {
			for i := len(closers) - 1; i >= 0; i-- {
				_ = closers[i]()
			}
		}
	}()

	httpLn, err := net.Listen("tcp", cfg.HTTPAddr)
	if err != nil {
		return nil, fmt.Errorf("bind http %s: %w", cfg.HTTPAddr, err)
	}
	closers = append(closers, httpLn.Close)

	tcpLn, err := net.Listen("tcp", cfg.TCPAddr)
	if err != nil {
		return nil, fmt.Errorf("bind tcp %s: %w", cfg.TCPAddr, err)
	}
	closers = append(closers, tcpLn.Close)

	udpPC, err := net.ListenPacket("udp", cfg.UDPAddr)
	if err != nil {
		return nil, fmt.Errorf("bind udp %s: %w", cfg.UDPAddr, err)
	}
	closers = append(closers, udpPC.Close)

	grpcLn, err := net.Listen("tcp", cfg.GRPCAddr)
	if err != nil {
		return nil, fmt.Errorf("bind grpc %s: %w", cfg.GRPCAddr, err)
	}
	closers = append(closers, grpcLn.Close)

	n = &Node{cfg: cfg, log: log, started: time.Now(), httpLn: httpLn, grpcLn: grpcLn}
	n.reg = state.NewRegistry(cfg.NodeID, time.Now)
	n.pool = rpc.NewPool(nil) // plaintext in CW1; mutual TLS arrives in Week 9

	// Manual bootstrap: statically configured peers never expire.
	for _, ps := range cfg.Peers {
		if _, err := n.reg.AddPeer(state.Peer{ID: ps.ID, Source: "static",
			Addrs: state.Addresses{HTTP: ps.HTTP, TCP: ps.TCP, UDP: ps.UDP, GRPC: ps.GRPC}}); err != nil {
			return nil, fmt.Errorf("static peer %s: %w", ps.ID, err)
		}
	}

	n.httpSrv = &http.Server{
		Handler: httpapi.NewRouter(httpapi.Deps{
			NodeID: cfg.NodeID, Started: n.started, Now: time.Now, Registry: n.reg, Peers: n, Log: log,
		}),
		ReadHeaderTimeout: 5 * time.Second, // slowloris protection
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
	n.tcp = ipc.NewTCPServer(tcpLn, cfg.NodeID, n.handleMessage, log.With("transport", "tcp"))
	n.udp = ipc.NewUDPServer(udpPC, cfg.NodeID, n.handleMessage, log.With("transport", "udp"))
	n.grpcSrv = rpc.NewGRPCServer(log.With("transport", "grpc"))
	peerv1.RegisterPeerServiceServer(n.grpcSrv, &rpc.Server{Reg: n.reg, Started: n.started})

	n.components = []component{
		{"http", func() error {
			if err := n.httpSrv.Serve(httpLn); !errors.Is(err, http.ErrServerClosed) {
				return err
			}
			return nil
		}, n.httpSrv.Shutdown},
		{"tcp", n.tcp.Serve, n.tcp.Shutdown},
		{"udp", n.udp.Serve, n.udp.Shutdown},
		{"grpc", func() error { return n.grpcSrv.Serve(grpcLn) }, n.stopGRPC},
	}
	return n, nil
}

// Registry exposes the state owner (used by tests and later components).
func (n *Node) Registry() *state.Registry { return n.reg }

// HTTPAddr, TCPAddr and UDPAddr are the actual bound addresses.
func (n *Node) HTTPAddr() string { return n.httpLn.Addr().String() }
func (n *Node) TCPAddr() string  { return n.tcp.Addr() }
func (n *Node) UDPAddr() string  { return n.udp.Addr() }
func (n *Node) GRPCAddr() string { return n.grpcLn.Addr().String() }

// stopGRPC drains in-flight RPCs, but if that takes longer than the
// shutdown window it forces connections closed rather than hanging.
func (n *Node) stopGRPC(ctx context.Context) error {
	done := make(chan struct{})
	go func() { n.grpcSrv.GracefulStop(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		n.grpcSrv.Stop()
		<-done
		return fmt.Errorf("grpc forced stop: %w", ctx.Err())
	}
}

// Run serves every component until ctx is cancelled or one fails. Either way,
// all components are stopped in reverse start order within ShutdownTimeout.
func (n *Node) Run(ctx context.Context) error {
	errCh := make(chan error, len(n.components))
	var wg sync.WaitGroup
	for _, c := range n.components {
		wg.Add(1)
		go func(c component) {
			defer wg.Done()
			if err := c.serve(); err != nil {
				errCh <- fmt.Errorf("%s: %w", c.name, err)
			}
		}(c)
	}
	n.log.Info("node started", "id", n.cfg.NodeID,
		"http", n.HTTPAddr(), "tcp", n.TCPAddr(), "udp", n.UDPAddr(), "grpc", n.GRPCAddr(),
		"staticPeers", len(n.cfg.Peers))

	var runErr error
	select {
	case <-ctx.Done():
		n.log.Info("shutdown requested")
	case runErr = <-errCh:
		n.log.Error("component failed; shutting down", "err", runErr)
	}

	// Fresh context: the parent is already cancelled, but draining
	// in-flight work deserves its own bounded window.
	sctx, cancel := context.WithTimeout(context.Background(), n.cfg.ShutdownTimeout)
	defer cancel()
	for i := len(n.components) - 1; i >= 0; i-- {
		c := n.components[i]
		if err := c.stop(sctx); err != nil {
			runErr = errors.Join(runErr, fmt.Errorf("stop %s: %w", c.name, err))
		}
	}
	wg.Wait() // every serve goroutine has returned: nothing left running
	if err := n.pool.Close(); err != nil {
		runErr = errors.Join(runErr, fmt.Errorf("close rpc pool: %w", err))
	}
	n.log.Info("node stopped", "id", n.cfg.NodeID)
	return runErr
}
