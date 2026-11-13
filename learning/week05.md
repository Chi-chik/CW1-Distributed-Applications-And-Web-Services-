# Week 5: remote invocation with gRPC, and pulling it all together

## What I built

- `proto/peer/v1/peer.proto`: the contract, `GetNodeInfo` + paged `ListFiles`
- `gen/peerv1`: generated Go code, committed. `scripts/gen-proto.sh` regenerates with pinned versions
- `internal/rpc`: server on top of the registry, interceptor (time cap, panic recovery, logging, error mapping), `Pool` with one connection per peer
- `-peer` flags: manual bootstrap, static peers at startup
- `POST /v1/peers/{id}/probe` and `GET /v1/peers/{id}/files`: one REST call on node1 drives TCP, UDP and gRPC to node2. Where all of CW1 meets.
- `scripts/demo-cw1.sh`: full two-node flow including failures, transcript in `docs/evidence/cw1-demo-transcript.txt`

```
scripts/demo-cw1.sh
```

## REST vs gRPC

| | REST/JSON (`/v1/...`) | gRPC/Protobuf (`PeerService`) |
|---|---|---|
| Contract | Implicit, documented by hand | `.proto` *is* the contract; code generated from it |
| Encoding | Text JSON, self-describing, curl-readable | Binary, compact, needs the schema |
| Transport | HTTP/1.1, one request per connection at a time | HTTP/2, many calls multiplexed on one connection |
| Errors | HTTP status + JSON body | Status code (`UNAVAILABLE`, `INVALID_ARGUMENT`, …) + message |
| Deadlines | Each hop invents its own | Travels with the call (`grpc-timeout` header) |
| Evolution | Add fields, hope clients ignore unknowns | Field numbers give defined forward/backward compatibility |
| Best for | Public APIs, browsers, humans debugging | Internal service-to-service calls |

REST at the edge, gRPC between nodes. Kubernetes and most microservice estates make the same split.

## Decisions

**Units in field names.** `size_bytes`, `updated_unix_ms`, so nobody guesses KiB vs bytes or seconds vs milliseconds. Proto3 zero means unset, so zero defaults are chosen to be safe: `page_size: 0` means 'server default', never 'zero results'. `protocol_version` catches an incompatible peer early.

**Pagination by last-seen key.** Token is the last file name returned. Works because Week 3 sorted everything. Offset paging would skip or repeat when files change between calls.

**Server-side deadline cap.** The interceptor wraps every call in `context.WithTimeout(ctx, 10s)`. Caller set 2 s? Theirs wins. Set none? Still bounded. Deadlines propagate: a call made inside a handler inherits the remaining budget.

**Errors mapped at both edges.** Same sentinels (`state.ErrInvalid`, `ErrNotFound`) map to HTTP codes in `httpapi` and gRPC codes in `rpc`. Unknown becomes `INTERNAL: internal error`, and the test proves a message containing 'hunter2' never escapes. In the other direction `UNAVAILABLE` becomes 502 (we're fine, upstream isn't) and `DEADLINE_EXCEEDED` becomes 504.

**`UnimplementedPeerServiceServer` on purpose.** New RPC in the `.proto`, old servers still compile and answer `UNIMPLEMENTED`.

**Connection reuse.** A `ClientConn` is concurrency-safe and expensive to set up (HTTP/2 now, TLS handshake from Week 9), so the pool keeps one per address. Demo: first gRPC call 2.4 ms vs 0.3 ms for UDP. That's connection setup.

**Fail fast.** No `WaitForReady`, so gRPC answers `UNAVAILABLE` immediately when a peer is down instead of waiting out the deadline. The demo stops node2 and node1 answers 502 in 7 ms.

**Probe checks identity, not just reachability.** node2's gRPC address answering as node3 is a configuration error and gets reported. A bare 'connected OK' would hide it.

**Generated code is committed.** Builds without `protoc`. The script pins plugin versions because different versions generate different code.

## Notes to self

1. Contract first: field names, units, defaults, treated as carefully as a database schema.
2. Deadlines flow through the chain; every server caps them too.
3. Map errors at the boundary, both directions, leak nothing.
4. Reuse expensive connections, fail fast on dead peers.
5. One scripted demo with negative cases beats twenty screenshots.

## CW1 retrospective: carried into CW2

- **Plaintext everywhere:** readable and forgeable on the network. Week 9 adds TLS, mutual TLS, JWT.
- **Static peers:** adding a node means reconfiguring the rest. Week 10 adds gossip.
- **Declared metadata:** no content, so nothing proves the hash. Week 7 adds real storage with SHA-256 from the bytes.
- **In-memory state:** a restart forgets dynamic peers. Week 7 makes files durable and peers are rediscovered through gossip.
- **Sequential UDP handling:** acceptable for heartbeats, documented trade-off.
