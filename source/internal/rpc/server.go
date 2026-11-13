// Package rpc is the gRPC transport: the PeerService server, a client pool
// for calling other nodes, and the mapping between domain errors and gRPC
// status codes.
package rpc

import (
	"context"
	"errors"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"p2pfs/gen/peerv1"
	"p2pfs/internal/ipc"
	"p2pfs/internal/state"
)

const (
	defaultPageSize = 100
	maxPageSize     = 1000
	// MaxHandlerTime caps every RPC, even if the caller set no deadline or a
	// very long one. Callers' shorter deadlines are honoured as-is.
	MaxHandlerTime = 10 * time.Second
	// MaxMsgBytes bounds a single gRPC message in either direction.
	MaxMsgBytes = 1 << 20
)

var prefixPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{0,128}$`)

// Server implements peerv1.PeerServiceServer on top of the registry.
type Server struct {
	peerv1.UnimplementedPeerServiceServer // forward compatibility: new RPCs return Unimplemented
	Reg                                   *state.Registry
	Started                               time.Time
}

// GetNodeInfo returns this node's identity and a state summary.
func (s *Server) GetNodeInfo(ctx context.Context, _ *peerv1.GetNodeInfoRequest) (*peerv1.NodeInfo, error) {
	snap := s.Reg.Snapshot()
	return &peerv1.NodeInfo{
		NodeId:          snap.Self.ID,
		Status:          string(snap.Self.Status),
		StartedUnixMs:   s.Started.UnixMilli(),
		PeerCount:       uint32(len(snap.Peers)),
		FileCount:       uint32(len(snap.Files)),
		ProtocolVersion: ipc.ProtocolVersion,
	}, nil
}

// ListFiles pages through sorted file metadata. The page token is the last
// name returned, which works because the registry's order is deterministic.
func (s *Server) ListFiles(ctx context.Context, req *peerv1.ListFilesRequest) (*peerv1.ListFilesResponse, error) {
	if !prefixPattern.MatchString(req.GetNamePrefix()) {
		return nil, status.Error(codes.InvalidArgument, "name_prefix must be ≤128 chars of A-Z a-z 0-9 . _ -")
	}
	size := int(req.GetPageSize())
	switch {
	case size == 0:
		size = defaultPageSize
	case size > maxPageSize:
		return nil, status.Errorf(codes.InvalidArgument, "page_size %d exceeds %d", size, maxPageSize)
	}
	after := req.GetPageToken()
	if after != "" && state.ValidateName(after) != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid page_token")
	}

	resp := &peerv1.ListFilesResponse{NodeId: s.Reg.Self().ID}
	for _, f := range s.Reg.Files() {
		if after != "" && f.Name <= after {
			continue
		}
		if !strings.HasPrefix(f.Name, req.GetNamePrefix()) {
			continue
		}
		if len(resp.Files) == size {
			resp.NextPageToken = resp.Files[len(resp.Files)-1].Name
			break
		}
		resp.Files = append(resp.Files, toProto(f))
	}
	return resp, nil
}

func toProto(f state.FileMeta) *peerv1.FileMetadata {
	return &peerv1.FileMetadata{
		Name: f.Name, SizeBytes: f.Size, Sha256Hex: f.SHA256,
		OwnerNodeId: f.Owner, UpdatedUnixMs: f.UpdatedAt.UnixMilli(),
	}
}

// NewGRPCServer builds a server with bounded messages, a per-call time cap,
// panic recovery, logging and domain-error mapping.
func NewGRPCServer(log *slog.Logger, extra ...grpc.ServerOption) *grpc.Server {
	opts := []grpc.ServerOption{
		grpc.MaxRecvMsgSize(MaxMsgBytes),
		grpc.MaxSendMsgSize(MaxMsgBytes),
		grpc.ConnectionTimeout(5 * time.Second),
		grpc.ChainUnaryInterceptor(unaryInterceptor(log)),
	}
	return grpc.NewServer(append(opts, extra...)...)
}

func unaryInterceptor(log *slog.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp any, err error) {
		start := time.Now()
		ctx, cancel := context.WithTimeout(ctx, MaxHandlerTime) // the earlier of caller's deadline and our cap wins
		defer cancel()
		defer func() {
			if p := recover(); p != nil {
				log.Error("panic in rpc", "method", info.FullMethod, "panic", p)
				err = status.Error(codes.Internal, "internal error")
			}
			log.Info("grpc request", "method", info.FullMethod,
				"code", status.Code(err).String(), "durationMs", time.Since(start).Milliseconds())
		}()
		resp, err = handler(ctx, req)
		return resp, toStatus(err)
	}
}

// toStatus maps domain sentinels to gRPC codes, mirroring the HTTP mapping.
func toStatus(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := status.FromError(err); ok {
		return err
	}
	switch {
	case errors.Is(err, state.ErrInvalid):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, state.ErrNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, state.ErrConflict):
		return status.Error(codes.AlreadyExists, err.Error())
	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, err.Error())
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, err.Error())
	}
	return status.Error(codes.Internal, "internal error")
}
