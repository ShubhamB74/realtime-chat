# Failure Scenarios

Status: Phase 3 scenarios written as expectations and partly covered by tests; none measured under load. Chaos testing is Phase 8.

Expected behaviors to verify: Redis down (cross-gateway fast path lost, sync repairs, bounded by reconcile interval); gateway crash (clients reconnect with jittered backoff, sync); Kafka down (derived features delayed, delivery unaffected); PostgreSQL down (acceptance fails, no false acks); AI down (messaging unaffected). Results will be recorded from actual tests only.

## Phase 3 expectations (single gateway)
| Scenario | Expected behavior | Evidence |
|---|---|---|
| Client sends, connection drops before the ack | Client retries with the same idempotency key after reconnecting; gets the original message (`duplicate: true`); no duplicate stored or broadcast | store and e2e tests |
| Gateway restarts | Clients get 1001 (or an abrupt close), reconnect with backoff, sync from their cursors; nothing committed is lost | graceful close tested; abrupt crash not tested |
| Client is slow or stalled | Outbound queue overflows (256 frames or 4 MiB), connection closed with 4002; client reconnects and syncs. Memory per connection stays bounded | unit tests |
| Redis down | Presence and rate limiting degrade (limiter fails open, presence unavailable); sending, receiving and sync are unaffected | presence errors are logged throttled; not tested end to end |
| PostgreSQL down | Sends fail with 503/`unavailable` (retryable) and are never acknowledged; no false acceptance | by construction; not tested |
| Session revoked while connected | Socket closed with 4001 within about 60 s (default re-check interval) | e2e test (100 ms interval) |
| Duplicate or reordered pushes (push and sync overlap) | Client reducer dedupes by id and highest `mod_seq` | protocol rule; client not built yet |
| Handler panic | That connection is closed (1011); others unaffected | unit test |
