# Load tests

**Status: harness written, baseline NOT yet recorded.** Nothing here has been run. Every latency
target in the thresholds is a hypothetical goal from `docs/non-functional-requirements.md`, not a
measurement. Record real numbers in `docs/performance/baseline-phase3.md`.

## Prerequisites
- [k6](https://k6.io/docs/get-started/installation/) installed (not managed by this repo).
- Backend running locally against a migrated dev database: `make up && make migrate-up && make run`.
- Seed data: `make load-seed` (default 200 users in groups of 10). Variants:
  - `make load-seed LOAD_USERS=50 LOAD_GROUP_SIZE=50` creates one group of 50 (a "hot conversation": all writers contend on one counter row).
  - `go run ./cmd/loadseed -cleanup` (from `backend/`) removes everything it created.

The seeder writes straight to the database (registration and login are rate limited and expensive
by design). Its users cannot log in. It refuses to run when `APP_ENV=production`. The output file
contains access tokens; it is git-ignored.

## Run
```
make load-baseline                                   # 100 VUs for 2 minutes
k6 run -e VUS=300 -e DURATION=5m tests/load/k6/chat.js
k6 run -e SEND_INTERVAL_MS=500 -e VUS=100 tests/load/k6/chat.js   # higher message rate
```
Environment: `BASE_URL` (default `http://127.0.0.1:8080`), `VUS`, `DURATION`, `SEND_INTERVAL_MS`
(per VU, default 2000), `MSG_BYTES`, `SESSION_SECONDS` (connection lifetime, default 60).

Capacity assumption to compare against: about 60 msg/s at peak (docs/capacity-estimation.md). With
groups of 10 and one message per VU every 2 s, 100 VUs produce about 50 msg/s accepted and about
450 deliveries/s.

## What is measured
| Metric | Meaning | Hypothetical target |
|---|---|---|
| `accept_latency_ms` | `message.send` to `message.accepted` (includes the PostgreSQL commit) | p95 < 150 ms, p99 < 300 ms |
| `delivery_latency_ms` | send timestamp to `message.received` at another member | p95 < 300 ms, p99 < 800 ms |
| `ws_connect_failed` | failed WebSocket upgrades | < 1% |
| `server_error_events` | `error` events or socket errors | 0 |
| `abnormal_closes` | closes other than a normal client close (reported, no threshold) | n/a |

Delivery latency compares a client-side timestamp with the receiver's clock, so run k6 and the
backend on the same machine (or accept clock skew).

## Not covered yet (Phase 8)
Reconnect storms, slow consumers under load, large media uploads, thousands of connections,
multi-gateway delivery, and server-side metrics (Prometheus). This baseline exists to catch design
problems early, for example per-conversation counter contention, not to certify capacity.
