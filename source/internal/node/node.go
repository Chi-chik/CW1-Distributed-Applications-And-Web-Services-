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
	"time"

	"p2pfs/internal/config"
	"p2pfs/internal/httpapi"
)

// Node is one running peer.
type Node struct {
	cfg     config.Config
	log     *slog.Logger
	started time.Time

	httpLn  net.Listener
	httpSrv *http.Server
}

// New binds the listeners immediately. Binding here, rather than inside Run,
// means an occupied port is reported as a startup error before anything is
// "running", so main can exit cleanly with a clear message.
func New(cfg config.Config, log *slog.Logger) (*Node, error) {
	ln, err := net.Listen("tcp", cfg.HTTPAddr)
	if err != nil {
		return nil, fmt.Errorf("bind http %s: %w", cfg.HTTPAddr, err)
	}

	n := &Node{cfg: cfg, log: log, started: time.Now(), httpLn: ln}

	mux := http.NewServeMux()
	mux.Handle("GET /healthz", httpapi.HealthHandler(cfg.NodeID, n.started, time.Now))

	n.httpSrv = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second, // slowloris protection
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
	return n, nil
}

// HTTPAddr is the actual bound address (useful when the port was 0).
func (n *Node) HTTPAddr() string { return n.httpLn.Addr().String() }

// Run serves until ctx is cancelled or a server fails, then shuts down.
func (n *Node) Run(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() { errCh <- n.httpSrv.Serve(n.httpLn) }()
	n.log.Info("node started", "id", n.cfg.NodeID, "http", n.HTTPAddr())

	var runErr error
	select {
	case <-ctx.Done():
		n.log.Info("shutdown requested")
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			runErr = fmt.Errorf("http server: %w", err)
		}
	}

	// A fresh context: the parent is already cancelled, but draining
	// in-flight requests deserves its own bounded window.
	sctx, cancel := context.WithTimeout(context.Background(), n.cfg.ShutdownTimeout)
	defer cancel()
	if err := n.httpSrv.Shutdown(sctx); err != nil {
		runErr = errors.Join(runErr, fmt.Errorf("http shutdown: %w", err))
	}
	n.log.Info("node stopped", "id", n.cfg.NodeID)
	return runErr
}
