// Command node starts one peer of the distributed file sharing service.
//
//	go run ./cmd/node -id node1 -http 127.0.0.1:8081
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"p2pfs/internal/config"
	"p2pfs/internal/node"
)

// Exit codes: 0 clean stop, 1 runtime/startup failure, 2 bad configuration.
func main() { os.Exit(run()) }

func run() int {
	cfg, err := config.Parse(os.Args[1:], os.Getenv, os.Stderr)
	if errors.Is(err, config.ErrHelp) {
		return 0
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "configuration error:\n%v\n", err)
		return 2
	}

	log := slog.New(slog.NewJSONHandler(os.Stderr, nil)).With("node", cfg.NodeID)

	n, err := node.New(cfg, log)
	if err != nil {
		log.Error("startup failed", "err", err)
		return 1
	}

	// SIGINT (Ctrl+C) and SIGTERM (docker stop) both trigger graceful shutdown.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := n.Run(ctx); err != nil {
		log.Error("node failed", "err", err)
		return 1
	}
	return 0
}
