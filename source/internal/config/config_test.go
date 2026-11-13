package config

import (
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func noEnv(string) string { return "" }

func TestParseValid(t *testing.T) {
	c, err := Parse([]string{"-id", "node1", "-http", "127.0.0.1:9000"}, noEnv, io.Discard)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.NodeID != "node1" || c.HTTPAddr != "127.0.0.1:9000" || c.ShutdownTimeout != 5*time.Second {
		t.Fatalf("unexpected config: %+v", c)
	}
}

func TestEnvFallbackAndFlagPrecedence(t *testing.T) {
	env := map[string]string{"NODE_ID": "envnode", "NODE_HTTP_ADDR": ":7000"}
	get := func(k string) string { return env[k] }

	c, err := Parse(nil, get, io.Discard)
	if err != nil || c.NodeID != "envnode" || c.HTTPAddr != ":7000" {
		t.Fatalf("env fallback failed: %+v %v", c, err)
	}
	c, err = Parse([]string{"-id", "flagnode"}, get, io.Discard)
	if err != nil || c.NodeID != "flagnode" {
		t.Fatalf("flag should win over env: %+v %v", c, err)
	}
}

func TestParseRejectsBadInput(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"missing id", []string{}, "invalid node id"},
		{"uppercase id", []string{"-id", "Node1"}, "invalid node id"},
		{"no port", []string{"-id", "n1", "-http", "localhost"}, "not host:port"},
		{"port too big", []string{"-id", "n1", "-http", ":70000"}, "invalid port"},
		{"unknown flag", []string{"-id", "n1", "-bogus"}, "flag provided but not defined"},
		{"stray arg", []string{"-id", "n1", "extra"}, "unexpected arguments"},
		{"zero timeout", []string{"-id", "n1", "-shutdown-timeout", "0s"}, "shutdown timeout"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse(tc.args, noEnv, io.Discard)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestHelpIsDistinguishable(t *testing.T) {
	_, err := Parse([]string{"-h"}, noEnv, io.Discard)
	if !errors.Is(err, ErrHelp) {
		t.Fatalf("want ErrHelp, got %v", err)
	}
}

func TestPeerSpecs(t *testing.T) {
	c, err := Parse([]string{"-id", "node1",
		"-peer", "node2,grpc=127.0.0.1:9082,tcp=127.0.0.1:7082",
		"-peer", "node3,udp=127.0.0.1:7083"}, noEnv, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Peers) != 2 || c.Peers[0].GRPC != "127.0.0.1:9082" || c.Peers[1].UDP != "127.0.0.1:7083" {
		t.Fatalf("%+v", c.Peers)
	}
	env := map[string]string{"NODE_ID": "node1", "NODE_PEERS": "node2,grpc=node2:9090; node3,grpc=node3:9090"}
	c, err = Parse(nil, func(k string) string { return env[k] }, io.Discard)
	if err != nil || len(c.Peers) != 2 || c.Peers[1].GRPC != "node3:9090" {
		t.Fatalf("env peers: %+v %v", c.Peers, err)
	}
	for _, bad := range []string{"Node2,grpc=h:1", "node2", "node2,grpc", "node2,ftp=h:1", "node2,grpc=nohost"} {
		if _, err := Parse([]string{"-id", "node1", "-peer", bad}, noEnv, io.Discard); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	if _, err := Parse([]string{"-id", "node1", "-peer", "node1,grpc=h:1"}, noEnv, io.Discard); err == nil {
		t.Error("accepted self as peer")
	}
}
