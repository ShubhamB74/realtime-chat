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

## Phase 4A expectations (several gateways)
| Scenario | Expected behavior | Evidence |
|---|---|---|
| Redis down (registry and bus) | Sending, local delivery and sync are unaffected. Live pushes to other gateways pause and are counted as `remote_failed`; those clients catch up with `sync.request` after reconnecting or on their periodic reconcile. Presence and rate limiting degrade (limiter fails open). No message is lost | unit and end-to-end tests with an in-memory bus |
| Redis returns | The bus subscription re-establishes itself (backoff and jitter); new connections register and receive pushes again. Connections made *during* the outage are registered at their next heartbeat (up to 25 s) | unit test; heartbeat re-registration not tested |
| A gateway crashes | Its clients reconnect (any gateway), then sync. Its registry entries linger up to 75 s: other gateways may publish pushes to it (lost, repaired by sync) and its users appear online for up to that long | by design; not tested |
| Cross-gateway queue full | The remote send is dropped (`remote_dropped`); local delivery unaffected; sync repairs | unit-level by construction; not load-tested |
| Two pushes arrive out of order from another gateway | Client sorts by `seq`, detects a gap and syncs | protocol rule; client not built |
| Gateway restarts with unflushed cursors | At most ~2 s of acknowledgements/read marks are lost (graceful shutdown flushes); clients re-acknowledge, some messages are re-delivered and deduplicated | shutdown flush tested; crash not tested |
| Database down during a cursor flush | The batch is dropped and logged (throttled); clients acknowledge again after their next batch | by construction |
| Duplicate `GATEWAY_ID` on two replicas | Each treats the other's connections as local and skips them: **cross-replica live delivery silently stops** (sync still works). Operators must keep ids unique | not detected by the code |
