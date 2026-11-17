# Testing Strategy

Status: Phase 1 in progress. Test code written; see docs/phase-1-acceptance-report.md for what has actually been executed.

Unit tests beside code; integration tests against real dependencies; contract tests for events and APIs; **k6 harness from Phase 3 with a recorded baseline**; chaos tests. Specific tests: client reducer dedupe/gap/reconcile, concurrent sends to one conversation (gapless seq), idempotent retries, sync after Redis outage. Nothing is reported as passing unless executed.

## Phase 1 test inventory
| Area | Type | Location | Run with |
|---|---|---|---|
| Config loading, validation, secret redaction | unit | `backend/internal/platform/config` | `make test` |
| Logger construction | unit | `backend/internal/platform/logging` | `make test` |
| Retry with exponential backoff and jitter | unit | `backend/internal/platform/retry` | `make test` |
| Request ID, access log (no query strings), panic recovery, error contract, graceful shutdown of in-flight requests | unit | `backend/internal/platform/httpx` | `make test` |
| Readiness semantics (required vs optional, timeout, panic, draining), TCP checks | unit | `backend/internal/platform/health` | `make test` |
| Schema constraints: case-insensitive email uniqueness, status check, FKs, cross-user device/session guard, token-hash uniqueness and length, cascade delete, `updated_at` trigger | integration (real PostgreSQL 18) | `backend/internal/store/postgres` | `make test-integration` |
| Migration up, down to zero, up again | CI step | `.github/workflows/ci.yml` | CI |
| AI service health endpoints | unit | `ai-service/tests` | `make ai-test` |

Not covered in Phase 1: Redis integration test, Kafka and object-store connectivity tests (checks exist but only as TCP readiness probes), frontend tests, compose smoke test in CI.

## Phase 2 test inventory
| Area | Type | Location |
|---|---|---|
| Argon2id: round trip, salting, rehash on parameter change, malformed hashes, concurrency bound | unit | `internal/auth/password_test.go` |
| JWT: round trip, expiry, wrong secret/issuer, tampering, `alg=none`, secret length; refresh-token generation | unit | `internal/auth/tokens_test.go` |
| Fixed-window limiter | unit | `internal/platform/ratelimit` |
| Accounts: validation, normalization, login failure indistinguishability and timing path, disabled users, rehash, refresh outcomes, logout | unit (fake repo) | `internal/app/accounts` |
| Chats authorization: every operation rejects non-members with 404, member-only vs admin-only (403), leave vs remove, validation, caps | unit (fake repo) | `internal/app/chats` |
| HTTP: bearer parsing, 401s, 429 with Retry-After, limiter fail-open, client IP, strict JSON decoding and size limit, error mapping, cursor, OpenAPI route coverage | unit | `internal/transport/httpapi` |
| Rotation, reuse detection, concurrent refresh with one token, expiry, absolute lifetime, device limit and reuse, device and session revocation | integration (PostgreSQL) | `internal/store/postgres/store_integration_test.go` |
| Direct conversation uniqueness under concurrency, membership as authorization primitive, last-admin rules, member limit rollback, re-add, keyset pagination | integration (PostgreSQL) | same file |
| Redis limiter window behavior | integration (Redis) | `internal/store/redis` |
| **Acceptance test:** strangers, members, admins and removed members across every conversation endpoint; auth lifecycle; sessions and devices; login rate limit; pagination | end-to-end (real services, real PostgreSQL, in-memory limiter) | `internal/transport/httpapi/api_integration_test.go` |

Not covered: frontend (no Phase 2 UI), load and concurrency beyond the targeted races above, fuzzing, Redis outage behavior end to end.

## Phase 3 test inventory
| Area | Type | Location |
|---|---|---|
| Message validation, normalization, publish-after-persist, no publish on duplicates or failures, limit clamping | unit (fake repo and publisher) | `internal/app/messages` |
| Gateway over real in-process WebSocket connections: handshake auth (header, subprotocol, query rejected, revoked session), ready event, send/ack/deliver to other members only, idempotent duplicates, non-member send, bad frames and versions, planned events, binary and oversized frames, sync, typing, revocation close (4001), slow consumer (frame and byte bounds), connection limits, graceful shutdown (1001), presence, inbound and sync rate limits, panic containment | unit (fakes) | `internal/transport/ws/hub_test.go` |
| Token bucket | unit | `internal/transport/ws/bucket_test.go` |
| Middleware recorder supports hijacking for upgrades | unit | `internal/platform/httpx` |
| Gapless sequence under 40 concurrent senders; idempotency incl. 8 racing retries; member-only writes (removed members); edit/delete rules and counter accounting; change and history ordering; join-point visibility incl. re-add; last-message preview; session liveness; table constraints | integration (PostgreSQL) | `internal/store/postgres/messages_integration_test.go` |
| Presence set semantics and expiry | integration (Redis) | `internal/store/redis` |
| REST lifecycle, join-point rule, conversation summary, **WebSocket end to end** (persisted send, non-member rejected, offline then sync, live edits, typing, presence, oversize) and revoked-session handshake plus live disconnect | end-to-end (real services and PostgreSQL) | `internal/transport/httpapi/messages_integration_test.go` |
| OpenAPI covers all 29 routes plus the WebSocket route | unit | `internal/transport/httpapi` |
| **Load baseline (k6): harness only, NOT yet run** | load | `tests/load` |

Not covered: heartbeat/pong timeout; multi-gateway delivery (Phase 4A); behavior with Redis down end to end; browser WebSocket behavior (the dev page `frontend/public/ws-debug.html` is a manual aid, not a test); unit tests of `cmd/loadseed`; fuzzing of the frame parser.
