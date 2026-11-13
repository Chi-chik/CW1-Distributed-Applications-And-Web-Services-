// Command p2pctl is a small client for exercising a node's interfaces.
//
//	p2pctl tcp-ping  -addr 127.0.0.1:7081
//	p2pctl udp-ping  -addr 127.0.0.1:7081 -attempts 3
//	p2pctl tcp-echo  -addr 127.0.0.1:7081 -msg hello
//	p2pctl info      -addr 127.0.0.1:9081            (gRPC GetNodeInfo)
//	p2pctl files     -addr 127.0.0.1:9081 -prefix "" (gRPC ListFiles)
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"google.golang.org/grpc/status"

	"p2pfs/gen/peerv1"
	"p2pfs/internal/ipc"
	"p2pfs/internal/rpc"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]

	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:7081", "target host:port")
	from := fs.String("from", "p2pctl", "sender ID placed in the envelope")
	timeout := fs.Duration("timeout", 2*time.Second, "overall deadline")
	attempts := fs.Int("attempts", 3, "UDP attempts")
	msg := fs.String("msg", "hello", "echo payload")
	prefix := fs.String("prefix", "", "file name prefix (files)")
	fs.Parse(args)

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	req := ipc.Envelope{V: ipc.ProtocolVersion, ID: ipc.NewID(), From: *from, TS: time.Now().UnixMilli()}
	var resp ipc.Envelope
	var err error
	start := time.Now()

	switch cmd {
	case "tcp-ping":
		req.Type = ipc.TypePing
		resp, err = ipc.TCPRequest(ctx, *addr, req)
	case "udp-ping":
		req.Type = ipc.TypePing
		resp, err = ipc.UDPRequest(ctx, *addr, req, *attempts)
	case "info", "files":
		os.Exit(grpcCmd(ctx, cmd, *addr, *prefix))
	case "tcp-echo":
		req.Type = ipc.TypeEcho
		req.Body, _ = json.Marshal(map[string]string{"msg": *msg})
		resp, err = ipc.TCPRequest(ctx, *addr, req)
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s failed after %s: %v\n", cmd, time.Since(start).Round(time.Millisecond), err)
		os.Exit(1)
	}
	out, _ := json.MarshalIndent(resp, "", "  ")
	fmt.Printf("%s\n(round trip %s)\n", out, time.Since(start).Round(time.Microsecond))
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: p2pctl <tcp-ping|udp-ping|tcp-echo|info|files> [-addr host:port] [flags]")
}

func grpcCmd(ctx context.Context, cmd, addr, prefix string) int {
	pool := rpc.NewPool(nil)
	defer pool.Close()
	c, err := pool.Client(addr)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	var out any
	if cmd == "info" {
		out, err = c.GetNodeInfo(ctx, &peerv1.GetNodeInfoRequest{})
	} else {
		var files []*peerv1.FileMetadata
		var node string
		files, node, err = rpc.ListAllFiles(ctx, c, prefix, 20)
		out = map[string]any{"nodeId": node, "count": len(files), "files": files}
	}
	if err != nil {
		st := status.Convert(err)
		fmt.Fprintf(os.Stderr, "%s failed: code=%s msg=%q\n", cmd, st.Code(), st.Message())
		return 1
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	fmt.Println(string(b))
	return 0
}
