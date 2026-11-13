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
	"time"
)

// Config is the complete, validated configuration for one node.
type Config struct {
	NodeID          string
	HTTPAddr        string
	ShutdownTimeout time.Duration
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
	fs.DurationVar(&c.ShutdownTimeout, "shutdown-timeout", envDur("NODE_SHUTDOWN_TIMEOUT", 5*time.Second), "max time to drain on shutdown")

	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	if fs.NArg() > 0 {
		return Config{}, fmt.Errorf("unexpected arguments: %v", fs.Args())
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
	if err := ValidateHostPort(c.HTTPAddr); err != nil {
		errs = append(errs, fmt.Errorf("http address: %w", err))
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
