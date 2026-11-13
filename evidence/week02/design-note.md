# Week 2: a REST resource that refuses bad input without breaking

## What I built

`GET /v1/node` (state) and `POST /v1/node/status` (`active|draining|maintenance`). The endpoints are the small part; the plumbing is what everything after reuses:

- `decodeJSON`: media type, size cap, no unknown fields, exactly one JSON value
- `writeError`: one error shape everywhere, `{"error":{"code","message","requestId"}}`
- `withCommon`: request ID, security headers, panic recovery, one access-log line
- `state.Registry` owns the data; handlers call methods, never touch fields

```
curl -s -X POST localhost:8081/v1/node/status \
  -H 'Content-Type: application/json' -d '{"status":"draining","reason":"upgrade"}'
```

## Status codes

| Situation | Code | Why |
|---|---|---|
| Success, representation returned | 200 | Status change returns the new state, no need to GET again |
| Not JSON / no Content-Type | 415 | Body may be fine; the declared format is wrong |
| Malformed JSON, unknown field, wrong type | 400 | Can't parse into the contract |
| Parses but value is wrong (`"sleepy"`) | 422 | Syntax OK, semantics not. 400 vs 422 tells the client what to fix |
| Body over 64 KiB | 413 | `http.MaxBytesReader` stops at the cap, so a 1 GB body never reaches memory |
| Unknown path / wrong method | 404 / 405 | 405 includes an `Allow` header |
| Anything unexpected | 500 | Generic message only. Details go to the log, never the client |

## Decisions

**`DisallowUnknownFields`.** Without it `{"status":"draining","admin":true}` passes silently and a `"staus"` typo gets swallowed. Strict decoding keeps the contract explicit.

**Second `Decode` call.** `json.Decoder` stops after the first value, so `{...}{...}` would pass. Decode again and expect `io.EOF`.

**Validate, then lock, then write.** `SetStatus` checks everything before the mutex, so a rejected request can't half-change state. The table test compares state before and after each rejection.

**The 405 trap.** I first used a catch-all `"/"` route for JSON 404s, but with the Go 1.22 mux `"/"` matches everything, so `DELETE /v1/node` came back 404 instead of 405. Fix: wrap the mux and rewrite its plain-text 404/405 into JSON. The negative test caught it.

**Never echo raw request IDs.** An `X-Request-ID` containing a newline forges log lines. It's only reused if it matches `^[A-Za-z0-9-]{1,64}$`.

## Java → Go

| Spring | Here |
|---|---|
| `@RestControllerAdvice` + `@ExceptionHandler` | `writeError` with `errors.Is`/`errors.As` |
| `FAIL_ON_UNKNOWN_PROPERTIES` (Jackson) | `dec.DisallowUnknownFields()` |
| `spring.servlet.multipart.max-request-size` | `http.MaxBytesReader` |
| Servlet `Filter` chain | handler-wrapping middleware (`withCommon`) |

## Notes to self

1. Error contract before the happy path.
2. Sentinels in the domain layer (`state.ErrInvalid`), mapping at the edge. The same sentinels become gRPC codes in Week 5.
3. Table-driven tests for the negative cases.
4. Bound every input: body size, field length, request ID format.
