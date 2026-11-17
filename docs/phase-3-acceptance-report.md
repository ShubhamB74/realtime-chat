# Phase 3 Acceptance Report

Date: 2026-11-15. Status: **code written, NOT yet accepted. The load baseline has NOT been run.** Phase 3 builds on Phases 1 and 2, neither of which has been verified on a real machine (no results were reported back).

## Authoring environment limits
Python and Node only. **No Go toolchain, PostgreSQL, Redis, Docker, k6 or network access.** No Go code has been compiled, formatted, linted or run; no WebSocket has ever been opened against this server.

## What was actually executed
| Check | Result |
|---|---|
| OpenAPI parses; all 29 Go routes documented with the right methods; no extra operations; all `$ref`s resolve; layout matches what the Go contract test expects | Passed |
| Static scan of 55 Go files: brackets balanced, imports used, no unimported stdlib package referenced (heuristic, **not a compiler**) | Passed |
| `node --check` on the k6 script (as an ES module) and the browser debug page script | Passed |
| New migrations (00007, 00008) have one Up/Down each and balanced markers | Passed |
| `make -n load-seed` renders correctly; `make help` lists the new targets | Passed |

## Written but NOT executed
Everything else: the Go build and `gofmt`; all unit tests (messages, gateway, token bucket, middleware, config, contract); all integration tests (message store, Redis presence, REST and WebSocket end to end); migrations 7 and 8 and every SQL statement in `messages.go` and the changed `conversations.go`; the load seeder; the k6 script; the debug page in a browser.

**The WebSocket library API was written from memory of `coder/websocket`** (v1.8.x): `Accept`/`AcceptOptions`, `Read`/`Write` with contexts, `Ping`, `Close` and `CloseNow`, `CloseStatus`, `SetReadLimit`, `Dial`/`DialOptions`. A mismatch would appear as a compile error. Behaviors I relied on and have not seen run: `Close` from a goroutine while a `Read` is in progress delivers our close code to the client; a context timeout on `Ping` closes the connection; `Accept` works through the middleware recorder (which now implements `Hijack`).

Most likely first-run problems: compile errors in the `ws` package, SQL typing details (for example the `$3::bigint = 0 OR seq < $3::bigint` pattern and `deleted_at::text`), timing-sensitive gateway tests (slow consumer, revocation, shutdown), and `gofmt` whitespace (`make fmt`).

## Verification procedure
```
extract the zip over the repo, then:
make deps               # adds github.com/coder/websocket; commit go.mod and go.sum
make fmt
make up && make migrate-up          # expect 8 migrations in total
make migrate-down && make migrate-down && make migrate-up    # 8 and 7 roll back and re-apply
make test                           # includes the gateway tests (real in-process WebSockets)
make test-integration               # includes the end-to-end REST + WebSocket tests
make lint
make run
```
Manual check: with `make run` and `cd frontend && npm run dev`, open `http://localhost:5173/ws-debug.html` in two browser windows, register and log in as two users, create a group (REST, for example with curl), connect both, send messages, disconnect one, send more, reconnect and watch the resync.

**The most valuable tests:** `TestWebSocketEndToEnd`, `TestSendAssignsGaplessSequenceUnderConcurrency`, `TestSendIsIdempotent`, `TestHistoryStartsAtTheMembersJoinPoint`, `TestSlowConsumerIsDisconnectedNotBuffered`. Send me the output of any failure.

## Load baseline (the Phase 3 exit criterion, still open)
```
make load-seed                     # 200 users in groups of 10 (dev database only)
make load-baseline                 # needs k6 installed and the backend running
make load-seed LOAD_USERS=50 LOAD_GROUP_SIZE=50 && k6 run -e VUS=50 tests/load/k6/chat.js   # hot conversation
```
Fill in `docs/performance/baseline-phase3.md` with real numbers and the machine description. **Until then there are no performance results and none of the NFR latency targets is validated.**

## Requirements status
| Phase 3 requirement (revised roadmap) | Status |
|---|---|
| WebSocket gateway: handshake auth, lifecycle, ping/pong, read and write pumps, connection limits, graceful disconnect, reconnection support, write deadlines, backpressure with bounded queues | Written |
| Protocol with versioned events, event ids, timestamps, request ids | Written for `message.send`, `sync.request`, `typing.*` and the server events; `message.ack`, `read.update`, `presence.update`, `conversation.join/leave` deliberately answered with `unsupported_event` |
| Message persistence before acknowledgement; retrieval; history; cursor pagination; edit; delete; client idempotency keys | Written |
| Cursor sync | Written (client-supplied cursors; server-side device cursors are Phase 4A) |
| Minimal presence in Redis (multi-device, expiry) | Written; `presence.changed` push events are Phase 4A |
| Last-message preview (deferred from Phase 2) | Written |
| Tests: connect, auth failures, reconnect, concurrent messages, slow consumers, heartbeat timeouts, persistence, duplicates | Written, **except heartbeat timeout** |
| k6 harness and baseline | Harness written, **baseline not run** |
| Acceptance: messages survive restarts; users reconnect and retrieve missed history | **Unverified until you run the tests** (restart survival is by construction: PostgreSQL commit before ack) |

## Decisions and deviations to confirm (ADR-010)
1. **New members see only messages from after they joined** (resolves the Phase 2 open question). Say so if you prefer full history.
2. **Edits overwrite the body; no edit history is kept.** Deletes are tombstones. No edit time limit.
3. **One backend replica only until Phase 4A.** Push is in-process.
4. **Sockets outlive access-token expiry**; the session is re-checked every 60 s (revocation latency up to about a minute).
5. Added beyond the brief: REST send, history, changes and presence endpoints; `connection.ready`, `message.updated` and `sync.batch` events; byte-bounded send queue; 1/s sync limit; browser debug page; `cmd/loadseed`.
6. **Hardening found while writing:** the first design bounded the outbound queue by frame count only; a client repeatedly requesting sync without reading could have queued hundreds of MB. It is now also bounded by bytes (4 MiB).

## Known limitations
- No frontend product UI for chat (only the debug page); no client reducer implementation beyond that page.
- No heartbeat-timeout test, no multi-gateway behavior, no outbox or Kafka yet, no read receipts, unread counts or delivery state.
- Every message costs about six SQL statements in one transaction; per-conversation write throughput is unmeasured.
- Typing events cost a database query each (throttled).
- No message search, attachments, retention or moderation yet.
- Defaults (queue sizes, rates, limits) are guesses until the baseline is run.
