# API and protocol reference

Every limit here is enforced in code. Constants are named so they're easy to grep.

## REST (JSON over HTTP)

Every response carries `X-Request-ID`, `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`, `Content-Security-Policy: default-src 'none'` and `Cache-Control: no-store`. One error shape everywhere:

```json
{ "error": { "code": "invalid_value", "message": "…", "requestId": "b25f1ec3e339092e" } }
```

| Method & path | Purpose | Success | Notable errors |
|---|---|---|---|
| `GET /healthz` | Liveness and identity | 200 | |
| `GET /v1/node` | This node's state | 200 | |
| `POST /v1/node/status` | Change status `{"status":"active\|draining\|maintenance","reason":"…"}` | 200 + new state | 400, 413, 415, 422 |
| `GET /v1/state` | Consistent snapshot: self, peers, files | 200 | |
| `GET /v1/peers` | List peers (sorted by ID) | 200 | |
| `POST /v1/peers` | Register `{"id":"node2","addrs":{"http","tcp","udp","grpc"}}` | 201 + `Location` | 409 duplicate, 422 invalid |
| `GET /v1/peers/{id}` | One peer | 200 | 404 |
| `DELETE /v1/peers/{id}` | Remove peer | 204 | 404 |
| `POST /v1/peers/{id}/probe` | Contact the peer over TCP, UDP and gRPC, report RTTs | 200 | 404 |
| `GET /v1/peers/{id}/files` | The peer's file list, over gRPC | 200 | 404, 422 no gRPC addr, **502** unavailable, **504** timeout |
| `GET /v1/files` | Local file metadata (sorted by name) | 200 | |
| `POST /v1/files` | Register metadata `{"name","size","sha256"}` (CW1; real uploads replace it in CW2) | 201 | 409, 422 |
| `GET /v1/files/{name}` | One file's metadata | 200 | 404 |

Request bodies: `Content-Type: application/json` (else 415), max 64 KiB (`httpapi.DefaultMaxBodyBytes`, 413), unknown fields rejected (400 `unknown_field`), exactly one JSON value.

Validation: node and peer IDs `^[a-z0-9][a-z0-9-]{0,31}$`; file names `^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`, no `..`; `sha256` is 64 lowercase hex; size 0–8 MiB; at most 256 peers and 10,000 files.

## TCP and UDP message protocol (`internal/ipc`)

Envelope (protocol version 1):

```json
{ "v": 1, "type": "ping", "id": "85019f1573e60e57", "from": "node1", "ts": 1761048858000, "body": { } }
```

| Field | Rule |
|---|---|
| `v` | Must be 1. Other versions rejected, never guessed at |
| `type` | `ping`→`pong`, `echo`→`echo_reply`, `heartbeat`→`ack`; `error` for failures |
| `id` | Correlation ID `^[A-Za-z0-9-]{1,64}$`. Reply carries the same ID |
| `from` | Sender node ID |
| `body` | Optional JSON, max 32 KiB (`MaxBodyBytes`) |

**TCP framing:** `[uint32 big-endian length][JSON]`. Max frame 64 KiB (`MaxTCPFrame`). Persistent connections, requests answered in order. Idle 30 s, then close. Bad message in a good frame gets an `error` reply and the connection stays; a bad *frame* (oversize/truncated) gets one `error`, then close. Max 64 concurrent connections.

**UDP:** one envelope per datagram, max 1200 bytes (`MaxUDPPayload`). Oversize gets dropped with no reply (anti-amplification); malformed gets a small `error` reply. The client retries up to 3 within the overall deadline, so request types must be idempotent.

**Timeouts:** client default 3 s (`DefaultTimeout`); server handler 5 s (TCP), 1 s (UDP).

## gRPC (`proto/peer/v1/peer.proto`)

Service `p2pfs.peer.v1.PeerService`:

| RPC | Request | Response | Errors |
|---|---|---|---|
| `GetNodeInfo` | `{}` | `node_id, status, started_unix_ms, peer_count, file_count, protocol_version` | |
| `ListFiles` | `name_prefix, page_size (0→100, max 1000), page_token` | `node_id, files[], next_page_token` | `INVALID_ARGUMENT` for bad prefix, size or token |

Contract notes: sizes are `int64 size_bytes`, times are `int64 *_unix_ms`, so the units live in the field names. The page token is the last name returned, which works because the order is deterministic. `protocol_version` lets callers detect an incompatible peer.

Server limits: 1 MiB per message, 10 s per call (`MaxHandlerTime`; a shorter caller deadline wins), panic recovery. Domain errors map to `INVALID_ARGUMENT`, `NOT_FOUND`, `ALREADY_EXISTS`, `DEADLINE_EXCEEDED`. Anything else becomes `INTERNAL` with a generic message.

Client: one cached `ClientConn` per peer address (`rpc.Pool`), 3 s default deadline, paging capped at 20 pages. `UNAVAILABLE` maps to REST 502, `DEADLINE_EXCEEDED` to 504.

Compatibility: never renumber or reuse a field number, reserve removed numbers, only ever add fields. Old readers ignore them.
