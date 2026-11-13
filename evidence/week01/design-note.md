# Week 1: a node that starts, reports health and stops cleanly

## What I built

`cmd/node` entry point. `internal/config` turns flags + env into a validated `Config`. `internal/node` binds the HTTP listener, serves `GET /healthz`, shuts down on SIGINT/SIGTERM.

```
cd source
make check            # go vet + go test
make run1             # node1 on 127.0.0.1:8081
curl -s localhost:8081/healthz
```

## Decisions

**Bind in `New`, serve in `Run`.** `net.Listen` happens before the node counts as started. An occupied port is a normal startup error with exit 1, never a half-started process that logs 'started' and then dies. Spring Boot does this in the container; in Go you pick where it happens. Binding early keeps failures predictable.

**Exit codes mean something.** 2 bad config, 1 runtime failure. Scripts and Docker restart policies can act on the difference.

**`signal.NotifyContext`.** OS signals become a context. Cancellation flows through contexts, not flags or interrupts like `Thread.interrupt()` in Java. Ctrl+C (SIGINT) and `docker stop` (SIGTERM) cancel the same context.

**Shutdown gets a fresh context.** The parent one is already cancelled by then, so `Shutdown` takes a `context.WithTimeout` to bound draining. Reuse the cancelled one and it aborts immediately.

**Server timeouts are not optional.** A bare `http.Server{}` has no read timeout, so one slow client can hold a connection open for ever (slowloris). `ReadHeaderTimeout` is the one I forgot.

**Go 1.22 method patterns.** `mux.Handle("GET /healthz", ...)` gets 405 with an `Allow` header for free. No third-party router needed.

**`errors.Join`.** All config problems reported at once, not one at a time.

## Java → Go

| Java habit | Go equivalent here |
|---|---|
| `@Value` / Spring config binding | `flag.FlagSet` + env fallback, explicit |
| Checked exceptions | `error` return values, wrapped with `%w` |
| `Logger` (SLF4J) | `log/slog`, structured JSON to stderr |
| JUnit + MockMvc | `testing` + `httptest.NewRecorder` |

## Notes to self

1. Fail at the boundary: validate config and bind ports before saying you're started.
2. Explicit timeouts and a bounded shutdown on every network server.
3. A context is the cancellation tree for the whole process. Pass it down, respect it.
4. Inject time (`now func() time.Time`) for deterministic tests.

## Try it

Start node1, start a second node on the same port, read the error. `curl -i -X POST localhost:8081/healthz` and check the `Allow` header.
