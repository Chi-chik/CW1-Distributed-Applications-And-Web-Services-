# CW1 implementation log (100872005)

## Timeline

| Date | What happened |
|---|---|
| 21 Oct 2026 | Started: Go module, CLI node, /healthz |
| 21 Oct 2026 | REST resource, strict decoding, error shape |
| 21 Oct 2026 | State model and registry |
| 21 Oct 2026 | TCP/UDP IPC |
| 21 Oct 2026 | gRPC contract, server, client, integrated demo |
| 21 Oct 2026 | Two-node demo run recorded (`evidence/final-integration/cw1-demo-transcript-2026-10-21.txt`) |
| 13 Nov 2026 | Work committed to GitHub in five commits, one per syllabus week, all dated today. |
| 13 Nov 2026 | `go.sum` had an incorrect checksum for genproto/googleapis/rpc, so a clean build failed. Fixed with `go get` (verified against the Go checksum database) and included in the final commit, so `final-cw1` builds cleanly. The bad line had been present from the start; only a clean build exposed it. |
| 13 Nov 2026 | Evidence produced from the final source: full test run under `-race` (`docs/test-results.txt`), per-week test runs (`evidence/weekNN/go-test.txt`), demo re-run (`evidence/final-integration/demo-transcript-2026-11-13.txt`), architecture diagram rendered from `docs/architecture.md`, commentary PDF written and packaged. |
| 13 Nov 2026 | Re-ran the integration test that failed in `make check`: all packages pass, and 950 repeat runs (incl. `-race`) passed. Cause found, left unfixed in `final-cw1`; recorded in `docs/test-results.txt`. |

## Decisions

One owner for shared state (`internal/state.Registry`): copy in, copy out, everything sorted on the way out. Bind in `New`, serve in `Run`, shutdown in reverse order. Strict JSON decoding: `DisallowUnknownFields`, one value per body, size cap. One versioned envelope over TCP and UDP, length-prefix framing on TCP, 1200-byte datagram cap on UDP. REST at the edge, gRPC between nodes. Reasoning per week in `learning/week01-05.md`.

## Problems and how I solved them

- **405 vs 404 (21 Oct):** a catch-all `"/"` route made `DELETE /v1/node` return 404 instead of 405. Wrapped the mux and rewrote its plain-text 404/405 replies as JSON.
- **Two clocks (21 Oct):** `TestTCPClientTimesOutAgainstSilentServer` failed with `i/o timeout` instead of `context.DeadlineExceeded`: the socket deadline fired before the context timer. Normalised in `ctxErr`.
- **go.sum checksum (13 Nov):** clean build failed on genproto/googleapis/rpc. Fixed with `go get`, verified against the checksum database.
- **Intermittent shutdown failure (13 Nov):** `TestIntegratedTwoNodeFlowOverREST` failed once with `stop http: context deadline exceeded` after 2.01 s, then passed under `-race` and in 950 repeat runs. Cause: `http.Server.Shutdown` waits for an accepted-but-unused keep-alive connection, and the test's 2 s shutdown window runs out first; reproduced deliberately with a raw idle connection. Not fixed in `final-cw1`. Fix for CW2: fall back to `httpSrv.Close()` when the shutdown context expires (as `stopGRPC` does) and close the test client's idle connections before stopping.

## Test changes

The 405-trap negative test was added when the catch-all route hid the wrong status code (21 Oct). The TCP timeout test was corrected after the two-clocks failure (21 Oct). `UDPServer.Drop` fault injection was added so the retry policy is proven by a test (21 Oct). No test changes were made for the 13 Nov intermittent failure; it is documented, not masked.

## Feedback acted upon

None received.

## AI-use statement

13 November 2026: I used Claude (Anthropic) to check the brief against my repository, prepare the five-commit history, diagnose and fix the go.sum checksum error, render `docs/architecture.md` to PNG and PDF, arrange the evidence folders, draft this log, and draft `cw1-commentary.pdf`, which I reviewed. The test results, per-week test runs and the 13 November demo transcript were produced by Claude running `make check`, `make race`, `go test -v` and `scripts/demo-cw1.sh` on my Mac at my direction; the outputs are those commands' real output, unedited. It did not write the Go code, the tests, or their results. I checked every file it produced. Later on 13 November I also used Claude to re-run the failing integration test, diagnose its cause and add the re-run results to `docs/test-results.txt` and this log; the source code was not changed. Claude also added the note in `repository.txt` listing which files in the zip differ from `final-cw1`, and rebuilt the submission zip with the correct `100872005_6CC545_CW1/` folder layout and without the `.git` directory; I checked the archive against the tag before submitting.
