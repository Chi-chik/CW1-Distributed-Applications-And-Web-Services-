# Week 4: talking over raw sockets (TCP and UDP)

## What I built

`internal/ipc` carries one versioned JSON envelope over two transports:

```
TCP:  [ uint32 big-endian length ][ {"v":1,"type":"ping","id":"…","from":"node1",…} ]
UDP:  one envelope per datagram, max 1200 bytes
```

Both transports feed one dispatcher (`node.handleMessage`): `ping`→`pong`, `heartbeat`→`ack`, `echo`→`echo_reply`. A message from a registered peer refreshes its `lastSeen`, the first time two parts of the system feed each other. `cmd/p2pctl` pokes at it.

```
make run1   # terminal 1
make run2   # terminal 2
go run ./cmd/p2pctl tcp-ping -addr 127.0.0.1:7082 -from node1
go run ./cmd/p2pctl udp-ping -addr 127.0.0.1:7081 -attempts 3
```

TCP and UDP share port 7081, which works because they're separate protocol namespaces in the kernel. Surprised me at first.

## TCP vs UDP

| | TCP | UDP |
|---|---|---|
| **Reliability** | Kernel retransmits; you get the bytes or an error | Datagrams vanish silently. Client retries (3 attempts, split deadline) |
| **Ordering** | Byte stream arrives in order | Independent datagrams, out of order or duplicated |
| **Message boundaries** | None. `Write("ab"); Write("cd")` can arrive as `"abcd"` or `"a"`,`"bcd"`, so you must frame | Built in. One `WriteTo` = one datagram = one `ReadFrom` |
| **Overhead** | Handshake before the first byte, connection state both ends | No setup; one packet out, one back |
| **Size** | Unbounded stream (frames capped at 64 KiB) | Must fit a packet, capped at 1200 bytes to dodge IP fragmentation |
| **Failure signal** | Dial error or read error | Usually silence; timeouts are the signal |
| **Good for** | Requests that must arrive, file transfer, RPC (gRPC runs on it) | Heartbeats, discovery, anywhere a late answer is worthless |

## Decisions

**Length check before allocate.** `readFrame` reads the 4-byte header, checks the 64 KiB limit, then allocates. A hostile 4 GiB claim costs 4 bytes, not 4 GiB of memory. The test sends a 1 GiB header.

**Two kinds of bad input.** Bad JSON in a good frame gets an `error` reply and the connection stays, because the stream is still in sync. A bad *frame* (oversize/truncated) gets one `error` and the connection closes, because the server can't find the next boundary.

**Deadlines on everything.** Server: idle read deadline per message, so silent clients get reclaimed. Client: deadline comes from the `context`; `context.AfterFunc` sets it in the past to unblock a stuck `Read`. Java equivalent: `Socket.setSoTimeout` plus closing the socket from another thread.

**Two clocks bit me.** First run of `TestTCPClientTimesOutAgainstSilentServer` failed with `i/o timeout`, not `context.DeadlineExceeded`: the socket deadline fired microseconds before the context timer. `ctxErr` normalises it. Flaky timing test? Find the second clock, don't add a sleep.

**UDP retries require idempotency.** Lost reply means the server already processed the request, and the retry processes it again. Fine for `ping` and `heartbeat`, not for 'transfer £10'. Non-idempotent over UDP needs request IDs and server-side dedup.

**Connected UDP reports refused ports.** `udp-ping` against a closed port fails instantly with `connection refused`. The kernel delivers ICMP 'port unreachable' to a `Dial`ed socket; `ListenPacket` + `WriteTo` would just time out.

**Never reply to oversize UDP garbage.** Small spoofed request triggering a big reply is a UDP amplification attack. Oversize drops silently; error replies are always smaller than the request.

**Failed starts leak nothing.** Third socket fails, deferred cleanup closes the first two. The test rebinds the HTTP port after a failed start. Shutdown: reverse order, `WaitGroup` on every serve goroutine, so `Run` returning means nothing is still running.

**Fault injection.** `UDPServer.Drop` discards the first datagram. It turns 'retries work' from a claim into evidence.

## Notes to self

1. TCP is a byte stream, not a message stream. Framing is your job.
2. Check sizes before allocating; every network length field is an attack surface.
3. Deadline on every call, and a way to interrupt blocked I/O for cancellation.
4. UDP silence is the normal failure mode, so timeouts and idempotent retries.
5. Flaky timing test means two clocks disagreeing.
