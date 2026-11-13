# Week 3: modelling distributed state, and who is allowed to touch it

## What I built

`state.Peer` and `state.FileMeta` are the models; `state.Registry` is the only owner of peer and file data. REST gained `/v1/peers`, `/v1/peers/{id}`, `/v1/files`, `/v1/files/{name}` and `/v1/state` (full snapshot).

```
curl -s -X POST localhost:8081/v1/peers -H 'Content-Type: application/json' \
  -d '{"id":"node2","addrs":{"grpc":"127.0.0.1:9002","tcp":"127.0.0.1:7002"}}'
curl -s localhost:8081/v1/state
```

## The state boundary

```
 REST handler ─┐
 TCP handler  ─┼──► Registry methods ──► [mu] peers map, files map
 gRPC handler ─┘     (copy in, copy out)
```

Three rules:

1. **One owner.** The maps are unexported fields of `Registry`. Nothing outside can reach them.
2. **Copy in, copy out.** Methods take and return values (`Peer`, `[]Peer`), never pointers into the map. Mutate what you got back and you only change your own copy. `TestSnapshotIsACopy` proves it.
3. **Every method locks.** `sync.RWMutex`: many readers, one writer. Callers don't know the lock exists.

It's the Go version of Java's 'encapsulate the `ConcurrentHashMap`'. Go makes the copy explicit because structs are values: a `[]Peer` built from map values is a real copy. Returning a `map` would not be.

## Determinism

Go randomises map iteration order on purpose. Everything leaving the registry is sorted (`Peers()` by ID, `Files()` by name). Stable tests, readable diffs, and in Week 10 every node gives the same answer when comparing metadata.

`Snapshot()` takes self + peers + files under one read lock, so they describe one instant. `Peers()` then `Files()` separately could interleave with a write.

## Validation

- **File names:** allow-list `^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`, no `..`. No slashes means no path traversal by construction, which beats cleaning `../../etc/passwd` after the fact. It pays off in Week 7 when names become real paths.
- **Peer addresses:** host + port 1–65535. Port 0 is fine for our own listener (OS picks one), meaningless for a remote peer.
- **Duplicates get 409,** never a silent overwrite, because a typo could reroute traffic with nobody noticing.
- **Server-owned fields stay server-owned.** `PeerRequest` has no `source` or `addedAt`; `DisallowUnknownFields` makes trying a 400.
- **Bounded tables** (`MaxPeers`, `MaxFiles`). The concurrency test hit the limit on its first run, so the bound holds under contention.

## Peer expiry (for Week 10)

`TouchPeer(id, ttl)` extends `ExpiresAt`; `ExpirePeers()` removes anything past it. Static peers (zero `ExpiresAt`) never expire. Fake clock in the test, so no `time.Sleep` and no flakiness.

## What the concurrency test proves

`TestConcurrentAccessKeepsStateConsistent`: 15 goroutines adding peers, snapshotting, fighting over one duplicate ID. The final count proves the logic. Running under `go test -race` proves the memory access is safe; an unsynchronised map can pass by luck and then die in production with `fatal error: concurrent map writes`.

## Notes to self

1. Decide who owns shared state. Enforce it with package boundaries.
2. Copies across the boundary, sorted on the way out.
3. Allow-list beats sanitise.
4. Concurrent tests only count under `-race`.
5. Inject the clock and expiry tests run in microseconds.
