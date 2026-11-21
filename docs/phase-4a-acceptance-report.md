# Phase 4A Acceptance Report

Date: 2026-11-16. Status: **code written, NOT yet accepted.** Phase 4A builds on Phases 1 to 3, none of which has been verified on a real machine (no results were reported back). **No load figures exist.**

## Authoring environment limits
Python and Node only. **No Go toolchain, PostgreSQL, Redis, Docker, k6 or network access.** No Go code has been compiled, formatted, linted or run. No two gateways have ever run side by side.

## What was actually executed
| Check | Result |
|---|---|
| OpenAPI parses; all 31 Go routes documented with the right methods; no extra operations; all `$ref`s resolve; layout matches what the Go contract test expects | Passed |
| Static scan of 68 Go files: brackets balanced, imports used, no unimported stdlib package referenced (heuristic, **not a compiler**) | Passed |
| `node --check` on the updated debug page script and the k6 script | Passed |
| Migration 00009 (and 1 to 8) have one Up/Down each and balanced markers | Passed |
| Compose file (with the new `multi` profile) and CI workflow parse as YAML; `make -n up-multi` renders | Passed |

## Written but NOT executed
Everything else: the Go build and `gofmt`; all unit tests (gateway cluster, gwbus, receipts, messages, config, contract); all integration tests (Redis registry and bus, receipts/unread/pending/visibility in PostgreSQL, two-gateway end-to-end); migration 9 and every SQL statement in `receipts.go` and the changed `messages.go`/`conversations.go`; the debug page in a browser.

Behaviors I relied on and have not seen run: go-redis `Subscribe`/`Receive`/`Channel` semantics (confirmation wait, automatic reconnect), `ZRangeBy` argument types, a broadcast reaching the publishing process too, `time.AfterFunc` timing in the presence tests, and several timing-sensitive tests (grace periods, flush intervals, one-second sync gaps).

Most likely first-run problems: compile errors in `internal/transport/ws` (the largest change), SQL parameter typing in the new queries (`ANY($2::text[]::uuid[])`, `GREATEST/LEAST` with bigint casts, the unread subquery), timing flakiness in the cluster tests, and `gofmt` whitespace (`make fmt`).

## Verification procedure
```
extract the zip over the repo, then:
make deps            # no new modules this phase; run it anyway after pulling
make fmt
make up && make migrate-up                 # expect 9 migrations
make migrate-down && make migrate-up       # migration 9 rolls back and re-applies
make test                                  # gateway cluster tests are the most important new ones
make test-integration                      # needs Postgres AND Redis (make up starts both)
make lint
make up-multi                              # two gateways for the manual check below
```
Manual check of the exit criterion (two gateways deliver across each other): open `http://localhost:5173/ws-debug.html` in two browser windows. Register and log in as two users, create a direct conversation (`POST /v1/conversations/direct`), then connect window 1 with the default WebSocket base and window 2 with `127.0.0.1:8082`. Messages should flow both ways; `docker compose logs backend backend2` should show `remote_published_total` and `remote_received_total` rising. Then `docker compose stop redis`: messages sent from one window stop appearing live in the other (`remote_failed_total` rises), nothing breaks, and after `docker compose start redis` plus a reconnect the missed messages arrive through sync.

**The most valuable tests:** `TestMessageCrossesGateways`, `TestRegistryOrBusOutageFallsBackToSyncWithoutLoss`, `TestTwoGatewaysDeliverToEachOther`, `TestBusOutageDoesNotLoseMessages`, `TestLastSeqIsASafeCursorUnderConcurrentWrites`, `TestCursorsAreMonotonicCappedAndValidated`. Send me the output of any failure.

## Requirements status (revised roadmap, Phase 4A)
| Requirement | Status |
|---|---|
| Fast path over Redis pub/sub; connection registry; cross-gateway delivery | Written (Redis registry and bus; in-memory twins for tests) |
| Device cursors; receipts as cursors; unread counts | Written |
| Presence subscriptions (`presence.subscribe`, snapshot, changed events, cross-gateway) | Written |
| Server-driven sync (one indexed query per device) | Written |
| Exit: "two gateways deliver across each other" | **Unverified until you run the tests and the manual check** |
| Exit: "Redis outage degrades to sync without loss" | **Unverified** (tested with an injected in-memory outage, not with real Redis down) |

## Changes to earlier phases (please review)
1. **`GET .../changes` semantics (Phase 3 fix):** `ListChanges` read the counter *after* the messages, so a client that acknowledged `last_seq` could skip a concurrent change. It now reads the counter first and bounds the query by it; `last_seq` is a safe cursor when `has_more` is false (test included).
2. **`message.ack` payload** uses `up_to_mod_seq` (the LLD said `up_to_seq`); the sync cursor is a `mod_seq`.
3. **`sync.request`:** an explicit empty list is still invalid, but an absent or null list now means server-driven sync. The `ws` test for the old "planned" `message.ack` error was replaced.
4. **`ws.Hub` constructor/config:** new `GatewayID`, `FlushInterval`, `PresenceGrace`, `MaxPresenceSubs`; new `Start`, `BindReceipts`; `Presence` is now the `gwbus.Registry` interface.
5. Connection registration in Redis now has a 500 ms timeout so a slow Redis cannot delay new connections.

## Decisions to confirm (ADR-011)
1. Receipts are cursors; direct conversations push them to both sides; groups only expose "seen by" on demand.
2. Read receipts are always on (no opt-out). Presence is visible to anyone you share a small (at most 50 members) conversation with.
3. Cross-gateway live delivery is best effort with no ordering guarantee; sync is the correctness mechanism.
4. The registry entry lifetime is 75 s, so a crashed gateway looks alive that long.
5. Redis becomes a trust boundary for live frames: it must be private and authenticated.
6. Instant session kick over the bus was **not** built; revocation still closes sockets within about 60 s.

## Known limitations
- Out-of-order delivery of pushes from another gateway is possible (clients must sort by `seq`).
- A duplicate `GATEWAY_ID` silently stops cross-replica live delivery; nothing detects it.
- Clients connecting during a Redis outage are not registered until their next heartbeat (up to 25 s).
- Cursor buffer is in memory: a crash loses up to about 2 s of acknowledgements.
- REST-only clients cannot acknowledge delivery (only the WebSocket `message.ack` exists).
- The unread count and the server-driven sync query are unmeasured and partly unindexed (`updated_at` ordering).
- No heartbeat-timeout test; no test with two real processes or real Redis failure; the end-to-end tests use the in-memory bus.
- No outbox or Kafka yet (Phase 4B): derived pipelines (notifications, media, AI) do not exist.
- No frontend product UI; the debug page is a manual aid.
