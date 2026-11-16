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
