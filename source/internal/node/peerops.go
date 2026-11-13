package node

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"p2pfs/gen/peerv1"
	"p2pfs/internal/httpapi"
	"p2pfs/internal/ipc"
	"p2pfs/internal/rpc"
	"p2pfs/internal/state"
)

// peerCallTimeout bounds each remote operation started from the REST API.
const peerCallTimeout = 2 * time.Second

// ProbePeer contacts a known peer over every transport it advertises and
// reports per-transport results. It's the single REST call that proves REST,
// state, TCP, UDP and gRPC work together.
func (n *Node) ProbePeer(ctx context.Context, id string) (httpapi.ProbeResult, error) {
	p, err := n.reg.Peer(id)
	if err != nil {
		return httpapi.ProbeResult{}, err
	}
	res := httpapi.ProbeResult{PeerID: id}
	ping := func() ipc.Envelope {
		return ipc.Envelope{V: ipc.ProtocolVersion, Type: ipc.TypePing, ID: ipc.NewID(), From: n.cfg.NodeID, TS: time.Now().UnixMilli()}
	}

	if p.Addrs.TCP != "" {
		res.TCP = timed(ctx, func(c context.Context) error {
			_, err := ipc.TCPRequest(c, p.Addrs.TCP, ping())
			return err
		})
	}
	if p.Addrs.UDP != "" {
		res.UDP = timed(ctx, func(c context.Context) error {
			_, err := ipc.UDPRequest(c, p.Addrs.UDP, ping(), 3)
			return err
		})
	}
	if p.Addrs.GRPC != "" {
		res.GRPC = timed(ctx, func(c context.Context) error {
			cl, err := n.pool.Client(p.Addrs.GRPC)
			if err != nil {
				return err
			}
			info, err := cl.GetNodeInfo(c, &peerv1.GetNodeInfoRequest{})
			if err == nil && info.GetNodeId() != id {
				// The address answers, but it's a different node: config error.
				return fmt.Errorf("address %s answered as %q, expected %q", p.Addrs.GRPC, info.GetNodeId(), id)
			}
			return err
		})
	}
	if res.AnyOK() {
		_ = n.reg.TouchPeer(id, 0)
	}
	return res, nil
}

func timed(ctx context.Context, f func(context.Context) error) *httpapi.TransportResult {
	c, cancel := context.WithTimeout(ctx, peerCallTimeout)
	defer cancel()
	start := time.Now()
	err := f(c)
	r := &httpapi.TransportResult{OK: err == nil, RTTMillis: float64(time.Since(start).Microseconds()) / 1000}
	if err != nil {
		r.Error = err.Error()
	}
	return r
}

// RemoteFiles fetches a peer's file list over gRPC and converts it to the
// domain model. gRPC status codes become domain errors so the REST layer
// can map them without importing gRPC.
func (n *Node) RemoteFiles(ctx context.Context, id string) ([]state.FileMeta, error) {
	p, err := n.reg.Peer(id)
	if err != nil {
		return nil, err
	}
	if p.Addrs.GRPC == "" {
		return nil, fmt.Errorf("%w: peer %q has no gRPC address", state.ErrInvalid, id)
	}
	cl, err := n.pool.Client(p.Addrs.GRPC)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, peerCallTimeout)
	defer cancel()

	files, _, err := rpc.ListAllFiles(ctx, cl, "", 20)
	if err != nil {
		return nil, fromStatus(id, err)
	}
	_ = n.reg.TouchPeer(id, 0)
	out := make([]state.FileMeta, 0, len(files))
	for _, f := range files {
		out = append(out, state.FileMeta{
			Name: f.GetName(), Size: f.GetSizeBytes(), SHA256: f.GetSha256Hex(),
			Owner: f.GetOwnerNodeId(), UpdatedAt: time.UnixMilli(f.GetUpdatedUnixMs()).UTC(),
		})
	}
	return out, nil
}

func fromStatus(peer string, err error) error {
	switch status.Code(err) {
	case codes.Unavailable:
		return fmt.Errorf("%w: %s: %v", state.ErrUnavailable, peer, status.Convert(err).Message())
	case codes.DeadlineExceeded:
		return fmt.Errorf("peer %s: %w", peer, context.DeadlineExceeded)
	case codes.InvalidArgument:
		return fmt.Errorf("%w: %s", state.ErrInvalid, status.Convert(err).Message())
	}
	return err
}
