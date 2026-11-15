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
