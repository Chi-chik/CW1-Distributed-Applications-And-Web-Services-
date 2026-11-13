#!/usr/bin/env bash
# Regenerates Go bindings from source/proto. Generated code is committed, so
# you only need this after editing a .proto file.
#
# Pinned tool versions (must match what produced the committed files):
#   protoc               27.x   (or: pip install grpcio-tools==1.67.1, then PROTOC="python3 -m grpc_tools.protoc")
#   protoc-gen-go        v1.34.2   go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.34.2
#   protoc-gen-go-grpc   v1.5.1    go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.5.1
set -euo pipefail
cd "$(dirname "$0")/../source"
PROTOC=${PROTOC:-protoc}
export PATH="$(go env GOPATH)/bin:$PATH"
mkdir -p gen/peerv1
$PROTOC -I proto \
  --go_out=gen/peerv1 --go_opt=paths=source_relative --go_opt=Mpeer/v1/peer.proto=p2pfs/gen/peerv1 \
  --go-grpc_out=gen/peerv1 --go-grpc_opt=paths=source_relative --go-grpc_opt=Mpeer/v1/peer.proto=p2pfs/gen/peerv1 \
  peer/v1/peer.proto
# paths=source_relative mirrors proto/peer/v1 -> gen/peerv1/peer/v1; flatten it.
mv gen/peerv1/peer/v1/*.go gen/peerv1/ && rm -rf gen/peerv1/peer
echo "generated: $(ls gen/peerv1)"
