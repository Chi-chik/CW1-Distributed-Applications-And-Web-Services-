package node

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"p2pfs/internal/config"
	"p2pfs/internal/httpapi"
	"p2pfs/internal/state"
)

const sampleHash = "b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9"

// The CW1 integration story in one test: node A is told about node B through
// REST, then a REST call on A drives TCP, UDP and gRPC traffic to B, and B's
// state (its file list) comes back through the gRPC contract.
func TestIntegratedTwoNodeFlowOverREST(t *testing.T) {
	a, stopA := startNode(t, "node-a")
	defer stopA()
	b, stopB := startNode(t, "node-b")

	if _, err := b.Registry().AddFile(state.FileMeta{Name: "hello.txt", Size: 11, SHA256: sampleHash}); err != nil {
		t.Fatal(err)
	}

	api := "http://" + a.HTTPAddr()
	body := fmt.Sprintf(`{"id":"node-b","addrs":{"tcp":%q,"udp":%q,"grpc":%q}}`, b.TCPAddr(), b.UDPAddr(), b.GRPCAddr())
	resp, err := http.Post(api+"/v1/peers", "application/json", strings.NewReader(body))
	if err != nil || resp.StatusCode != http.StatusCreated {
		t.Fatalf("register peer: %v %v", resp.Status, err)
	}
	resp.Body.Close()

	// Probe: one REST call, three transports.
	resp, err = http.Post(api+"/v1/peers/node-b/probe", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	var probe httpapi.ProbeResult
	json.NewDecoder(resp.Body).Decode(&probe)
	resp.Body.Close()
	if !probe.TCP.OK || !probe.UDP.OK || !probe.GRPC.OK {
		t.Fatalf("probe: tcp=%+v udp=%+v grpc=%+v", probe.TCP, probe.UDP, probe.GRPC)
	}

	// Remote file listing via gRPC.
	resp, err = http.Get(api + "/v1/peers/node-b/files")
	if err != nil {
		t.Fatal(err)
	}
	var files struct {
		Count int              `json:"count"`
		Files []state.FileMeta `json:"files"`
	}
	json.NewDecoder(resp.Body).Decode(&files)
	resp.Body.Close()
	if files.Count != 1 || files.Files[0].Name != "hello.txt" || files.Files[0].Owner != "node-b" {
		t.Fatalf("remote files: %+v", files)
	}

	// Failure path: B goes away; A must answer quickly with 502, not hang.
	stopB()
	start := time.Now()
	resp, err = http.Get(api + "/v1/peers/node-b/files")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("want 502 after peer stopped, got %d", resp.StatusCode)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("unavailable peer not handled within bound")
	}

	// Unknown peer is a client error, not a gateway error.
	resp, _ = http.Get(api + "/v1/peers/nobody/files")
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown peer: %d", resp.StatusCode)
	}
}

func TestStaticPeersFromConfig(t *testing.T) {
	cfg := testConfig("node-a")
	cfg.Peers = append(cfg.Peers, config.PeerSpec{ID: "node-z", GRPC: "127.0.0.1:1"})
	n, err := New(cfg, quietLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx := context.Background()
		n.httpLn.Close()
		n.grpcLn.Close()
		n.tcp.Shutdown(ctx)
		n.udp.Shutdown(ctx)
	}()
	p, err := n.Registry().Peer("node-z")
	if err != nil || p.Source != "static" || !p.ExpiresAt.IsZero() {
		t.Fatalf("%+v %v", p, err)
	}
}
