# p2pfs — 6CC545 CW1: a distributed file sharing node in Go

Week-by-week build of a peer-to-peer file sharing node, following the 6CC545 syllabus. One tag per week (`week01` … `week05`, `final-cw1`), one learning note per week in `learning/`.

## Prerequisites

- Go 1.23+ (built and tested with 1.24)
- Git, curl
- Only to regenerate gRPC code: `protoc` 27.x, `protoc-gen-go` v1.34.2, `protoc-gen-go-grpc` v1.5.1

## Build & test

```bash
cd source
go mod download        # first time only
make check             # go vet + go test ./...
make race              # go test -race ./...
```

Commands assume a bash shell (WSL, Git Bash, macOS or Linux).

## Run two nodes

Two terminals, from `source/`:

```bash
go run ./cmd/node -id node1 -http 127.0.0.1:8081 -tcp 127.0.0.1:7081 -udp 127.0.0.1:7081 -grpc 127.0.0.1:9081 \
  -peer node2,tcp=127.0.0.1:7082,udp=127.0.0.1:7082,grpc=127.0.0.1:9082

go run ./cmd/node -id node2 -http 127.0.0.1:8082 -tcp 127.0.0.1:7082 -udp 127.0.0.1:7082 -grpc 127.0.0.1:9082 \
  -peer node1,tcp=127.0.0.1:7081,udp=127.0.0.1:7081,grpc=127.0.0.1:9081
```

| Node | REST | TCP + UDP | gRPC |
|---|---|---|---|
| node1 | 8081 | 7081 | 9081 |
| node2 | 8082 | 7082 | 9082 |

Flags also have env vars (`NODE_ID`, `NODE_HTTP_ADDR`, `NODE_TCP_ADDR`, `NODE_UDP_ADDR`, `NODE_GRPC_ADDR`; `NODE_PEERS` split by `;`). `go run ./cmd/node -h` for the full set.

Exercise it:

```bash
curl -s localhost:8081/healthz
curl -s -X POST localhost:8082/v1/files -H 'Content-Type: application/json' \
  -d '{"name":"hello.txt","size":11,"sha256":"b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9"}'
curl -s -X POST localhost:8081/v1/peers/node2/probe     # TCP + UDP + gRPC to node2
curl -s localhost:8081/v1/peers/node2/files             # node2's files via gRPC
go run ./cmd/p2pctl tcp-ping -addr 127.0.0.1:7082
go run ./cmd/p2pctl info -addr 127.0.0.1:9082
```

Or run everything including the failure cases: `scripts/demo-cw1.sh`.

Stop with Ctrl+C (SIGINT) or `kill -TERM <pid>`. Shutdown drains within 5 s (`-shutdown-timeout`). Exit codes: 0 clean, 1 runtime/startup failure (occupied port etc.), 2 bad config.

CW1 writes nothing to disk. `make clean` removes `bin/`.

## Repository map

```
source/
  cmd/node        node entry point
  cmd/p2pctl      client for TCP/UDP/gRPC
  internal/config flags + env, validation
  internal/node   lifecycle, message dispatch, peer operations
  internal/state  the single owner of shared state
  internal/httpapi REST transport
  internal/ipc    TCP framing, UDP datagrams
  internal/rpc    gRPC server, client pool, error mapping
  proto/          .proto contracts
  gen/            generated gRPC code (committed)
docs/             API/protocol reference, architecture, evidence
learning/         one note per week
scripts/          demo and code generation
```

## Known limitations (CW1)

Plaintext transports, no auth. Peers configured by hand. File metadata declared, not derived from content. State in memory only. All CW2 work; `learning/week05.md` has the list.
