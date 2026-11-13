// Package config parses and validates node configuration.
//
// Every setting can come from a command-line flag or an environment
// variable. Flags win over environment variables, which win over defaults.
// Environment support matters later (Week 8) when nodes run in containers.
package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Config is the complete, validated configuration for one node.
type Config struct {
	NodeID          string
	HTTPAddr        string
	TCPAddr         string
	UDPAddr         string
	GRPCAddr        string
	Peers           []PeerSpec
	ShutdownTimeout time.Duration
}

// PeerSpec is a statically configured peer (manual bootstrap).
// Flag syntax: -peer node2,grpc=127.0.0.1:9082,tcp=127.0.0.1:7082,udp=127.0.0.1:7082
type PeerSpec struct {
	ID                   string
	HTTP, TCP, UDP, GRPC string
}

// peerFlags implements flag.Value so -peer can be repeated.
type peerFlags []PeerSpec

func (p *peerFlags) String() string { return fmt.Sprint(len(*p), " peers") }

func (p *peerFlags) Set(v string) error {
	spec, err := ParsePeerSpec(v)
	if err != nil {
		return err
	}
	*p = append(*p, spec)
	return nil
}

// ParsePeerSpec parses "id,key=addr,..." where key is http, tcp, udp or grpc.
func ParsePeerSpec(v string) (PeerSpec, error) {
	parts := strings.Split(strings.TrimSpace(v), ",")
	spec := PeerSpec{ID: parts[0]}
	if !nodeIDPattern.MatchString(spec.ID) {
		return PeerSpec{}, fmt.Errorf("peer %q: invalid id", v)
	}
	for _, kv := range parts[1:] {
		k, addr, ok := strings.Cut(kv, "=")
		if !ok {
			return PeerSpec{}, fmt.Errorf("peer %q: %q is not key=addr", v, kv)
		}
		if err := ValidateHostPort(addr); err != nil {
			return PeerSpec{}, fmt.Errorf("peer %q: %w", v, err)
		}
		switch k {
		case "http":
			spec.HTTP = addr
		case "tcp":
			spec.TCP = addr
		case "udp":
			spec.UDP = addr
		case "grpc":
			spec.GRPC = addr
		default:
			return PeerSpec{}, fmt.Errorf("peer %q: unknown key %q", v, k)
		}
	}
	if spec.GRPC == "" && spec.TCP == "" && spec.UDP == "" && spec.HTTP == "" {
		return PeerSpec{}, fmt.Errorf("peer %q: no addresses", v)
	}
	return spec, nil
}

// nodeIDPattern keeps IDs short, lowercase and DNS-safe, so the same ID
// can later be used as a container hostname and a TLS certificate name.
var nodeIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// ErrHelp is returned when the user asked for -h/-help.
var ErrHelp = flag.ErrHelp

// Parse reads flags from args, falling back to getenv for defaults.
// Usage and flag errors are written to out.
func Parse(args []string, getenv func(string) string, out io.Writer) (Config, error) {
	fs := flag.NewFlagSet("node", flag.ContinueOnError)
	fs.SetOutput(out)

	env := func(key, def string) string {
		if v := getenv(key); v != "" {
			return v
		}
		return def
	}
	envDur := func(key string, def time.Duration) time.Duration {
		if v := getenv(key); v != "" {
			if d, err := time.ParseDuration(v); err == nil {
				return d
			}
		}
		return def
	}

	var c Config
	fs.StringVar(&c.NodeID, "id", env("NODE_ID", ""), "unique node ID, e.g. node1 (env NODE_ID)")
	fs.StringVar(&c.HTTPAddr, "http", env("NODE_HTTP_ADDR", "127.0.0.1:8081"), "REST listen address host:port (env NODE_HTTP_ADDR)")
	fs.StringVar(&c.TCPAddr, "tcp", env("NODE_TCP_ADDR", "127.0.0.1:7081"), "TCP message listen address (env NODE_TCP_ADDR)")
	fs.StringVar(&c.UDPAddr, "udp", env("NODE_UDP_ADDR", "127.0.0.1:7081"), "UDP message listen address (env NODE_UDP_ADDR)")
	fs.StringVar(&c.GRPCAddr, "grpc", env("NODE_GRPC_ADDR", "127.0.0.1:9081"), "gRPC listen address (env NODE_GRPC_ADDR)")
	var peers peerFlags
	fs.Var(&peers, "peer", "static peer, repeatable: id,grpc=h:p,tcp=h:p,udp=h:p (env NODE_PEERS, ';'-separated)")
	fs.DurationVar(&c.ShutdownTimeout, "shutdown-timeout", envDur("NODE_SHUTDOWN_TIMEOUT", 5*time.Second), "max time to drain on shutdown")

	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	if fs.NArg() > 0 {
		return Config{}, fmt.Errorf("unexpected arguments: %v", fs.Args())
	}
	c.Peers = peers
	if len(c.Peers) == 0 && getenv("NODE_PEERS") != "" {
		for _, v := range strings.Split(getenv("NODE_PEERS"), ";") {
			if strings.TrimSpace(v) == "" {
				continue
			}
			spec, err := ParsePeerSpec(v)
			if err != nil {
				return Config{}, fmt.Errorf("NODE_PEERS: %w", err)
			}
			c.Peers = append(c.Peers, spec)
		}
	}
	return c, c.Validate()
}

// Validate reports every problem at once rather than stopping at the first,
// which is friendlier when someone is fixing a broken config.
func (c Config) Validate() error {
	var errs []error
	if !nodeIDPattern.MatchString(c.NodeID) {
		errs = append(errs, fmt.Errorf("invalid node id %q: want 1-32 chars of a-z, 0-9 or '-'", c.NodeID))
	}
	// A slice, not a map: map order is random and error output should be stable.
	for _, a := range [][2]string{{"http", c.HTTPAddr}, {"tcp", c.TCPAddr}, {"udp", c.UDPAddr}, {"grpc", c.GRPCAddr}} {
		if err := ValidateHostPort(a[1]); err != nil {
			errs = append(errs, fmt.Errorf("%s address: %w", a[0], err))
		}
	}
	seen := map[string]bool{c.NodeID: true}
	for _, p := range c.Peers {
		if seen[p.ID] {
			errs = append(errs, fmt.Errorf("peer %q is duplicated or is this node", p.ID))
		}
		seen[p.ID] = true
	}
	if c.ShutdownTimeout <= 0 || c.ShutdownTimeout > time.Minute {
		errs = append(errs, fmt.Errorf("shutdown timeout %s out of range (0, 1m]", c.ShutdownTimeout))
	}
	return errors.Join(errs...)
}

// ValidateHostPort checks a listen or peer address of the form host:port.
// An empty host means "all interfaces". Port 0 asks the OS for a free port,
// which tests rely on.
func ValidateHostPort(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("%q is not host:port: %w", addr, err)
	}
	p, err := strconv.Atoi(port)
	if err != nil || p < 0 || p > 65535 {
		return fmt.Errorf("%q has invalid port", addr)
	}
	if len(host) > 253 {
		return fmt.Errorf("%q host too long", addr)
	}
	return nil
}
