# Phase 3 — Load Benchmark Report

**Date:** 2026-11-17  
**Status:** Executed locally; all configured k6 thresholds passed  
**Scope:** Phase 3 WebSocket messaging, persistence, and hot-conversation baseline

---

## 1. Environment

| Item | Recorded value |
|---|---|
| Machine | AMD Ryzen 7 5800H with Radeon Graphics |
| CPU | 8 cores / 16 logical processors |
| RAM | 15.34 GB |
| Operating system | Windows 11 Home Single Language, version 10.0.26300, build 26300 |
| k6 | v2.4.0 |
| Go | go1.27.1 windows/amd64 |
| PostgreSQL | 18.7 (Docker image tag `postgres:18`) |
| PostgreSQL `max_connections` | 100 |
| Redis | 8.6.8 (Docker image tag `redis:8.6`) |
| Backend and load generator | Same host; k6 and backend ran locally |
| Infrastructure | Docker Compose: PostgreSQL, Redis, SeaweedFS; backend started separately with `make run` |
| Compose file | `infrastructure/docker/docker-compose.yml` |

### Backend configuration

The configuration source defines these defaults. The `.env` inspection found no matching `WS_*`, `ARGON2_*`, or `DB_MAX_CONNS` entries; this does not independently prove that the process environment had no overrides.

| Setting | Default recorded in config |
|---|---:|
| `WS_MAX_CONNS` | 5,000 |
| `WS_MAX_CONNS_PER_USER` | 5 |
| `WS_SEND_QUEUE` | 256 |
| `WS_MAX_QUEUE_BYTES` | 4 MiB |
| `WS_PING_INTERVAL` | 25s |
| `WS_PONG_TIMEOUT` | 10s |
| `WS_WRITE_TIMEOUT` | 10s |
| `WS_REVALIDATE_INTERVAL` | 60s |
| `WS_READ_LIMIT_BYTES` | 32,768 |
| `WS_EVENT_RATE` | 20 |
| `WS_EVENT_BURST` | 40 |
| `ARGON2_MEMORY_KIB` | 65,536 |
| `ARGON2_TIME` | 3 |
| `ARGON2_THREADS` | 2 |
| `ARGON2_MAX_CONCURRENT` | 0 (automatic; one slot per CPU) |
| `DB_MAX_CONNS` | 0 (use driver default) |

No custom PostgreSQL server tuning beyond the observed `max_connections=100` is recorded here. `DB_MAX_CONNS=0` means the application uses the driver's default pool setting; it must not be interpreted as a pool size of zero or as PostgreSQL's `max_connections`.

---

## 2. Test methodology

The k6 script was `tests/load/k6/chat.js`.

Default baseline command:

```powershell
make load-baseline
```

Higher-rate command:

```powershell
k6 run -e VUS=100 -e DURATION=2m -e SEND_INTERVAL_MS=500 tests/load/k6/chat.js
```

Hot-conversation seed command:

```powershell
make load-seed LOAD_USERS=50 LOAD_GROUP_SIZE=50
```

Hot-conversation command:

```powershell
k6 run -e VUS=50 -e DURATION=2m tests/load/k6/chat.js
```

The hot-conversation seed was verified to contain 50 users and one group/conversation. The hot-conversation run was executed twice against that same seeded conversation.

The k6 script's configured thresholds were:

| Metric | Threshold |
|---|---:|
| Acceptance latency p95 | < 150 ms |
| Acceptance latency p99 | < 300 ms |
| Delivery latency p95 | < 300 ms |
| Delivery latency p99 | < 800 ms |
| WebSocket connection failure rate | < 1% |
| Server error events | 0 |

These are local single-host measurements. They are not a multi-machine or production capacity test.

---

## 3. Benchmark results

### Run 1 — Baseline

**Command:** `make load-baseline`  
**Load:** 100 VUs, 2 minutes, default 2,000 ms send interval.

| Metric | Result |
|---|---:|
| Messages sent | 5,800 |
| Messages accepted | 5,799 |
| Accepted throughput | 47.48 messages/s |
| Acceptance latency p50 | 9 ms |
| Acceptance latency p95 | 15 ms |
| Acceptance latency p99 | 27 ms |
| Delivery latency p50 | 9 ms |
| Delivery latency p95 | 15 ms |
| Delivery latency p99 | 27 ms |
| WebSocket connection failures | 0 / 200 (0%) |
| HTTP 101 upgrade checks | 200 / 200 |
| Server error events | 0 |
| Thresholds | All passed |

**Note:** One fewer message was accepted than was sent. The run output alone does not establish the reason.

### Run 2 — Higher message rate

**Command:**

```powershell
k6 run -e VUS=100 -e DURATION=2m -e SEND_INTERVAL_MS=500 tests/load/k6/chat.js
```

| Metric | Result |
|---|---:|
| Messages sent | 23,800 |
| Messages accepted | 23,797 |
| Accepted throughput | 194.95 messages/s |
| Acceptance latency p50 | 8 ms |
| Acceptance latency p95 | 14 ms |
| Acceptance latency p99 | 20 ms |
| Delivery latency p50 | 8 ms |
| Delivery latency p95 | 14 ms |
| Delivery latency p99 | 20 ms |
| WebSocket connection failures | 0 / 200 (0%) |
| HTTP 101 upgrade checks | 200 / 200 |
| Server error events | 0 |
| Thresholds | All passed |

**Note:** Three fewer messages were accepted than were sent. The run output alone does not establish the reason.

### Run 3 — Hot conversation

**Seed:** 50 users in one conversation.  
**Load:** 50 VUs, 2 minutes.  
**Command:**

```powershell
k6 run -e VUS=50 -e DURATION=2m tests/load/k6/chat.js
```

The run was repeated twice using the same seeded conversation.

| Metric | First run | Rerun |
|---|---:|---:|
| Messages sent | 2,900 | 2,900 |
| Messages accepted | 2,900 | 2,900 |
| Accepted throughput | 23.76 messages/s | 23.75 messages/s |
| Acceptance latency p50 | 11 ms | 11 ms |
| Acceptance latency p95 | 19 ms | 19 ms |
| Acceptance latency p99 | 26 ms | 25 ms |
| Delivery latency p50 | 11 ms | 11 ms |
| Delivery latency p95 | 19 ms | 19 ms |
| Delivery latency p99 | 27 ms | 25 ms |
| WebSocket connection failures | 0 / 100 (0%) | 0 / 100 (0%) |
| HTTP 101 upgrade checks | 100 / 100 | 100 / 100 |
| Server error events | 0 | 0 |
| Thresholds | All passed | All passed |

**Persistence verification:** After the first run, PostgreSQL reported 2,900 persisted messages, with `min(seq)=1` and `max(seq)=2900`. After the rerun, PostgreSQL reported 5,800 persisted messages, with `min(seq)=1` and `max(seq)=5800`. This verifies that the 5,800 messages from both runs were persisted and that the observed sequence range was continuous from 1 through 5,800.

---

## 4. Gateway observations

During/after the hot-conversation run, gateway logs included these cumulative counters:

| Observation | Recorded value |
|---|---:|
| Connections during run | 50 |
| `rejected_limit_total` | 0 |
| `slow_consumers_total` | 0 |
| Final active connections after sessions ended | 0 |

The gateway counters (`accepted_total`, `published_total`, and `delivered_total`) were cumulative since backend startup, not isolated to Run 3. They should not be presented as per-run totals.

A session revalidation warning was also observed during client shutdown:

```text
session revalidation failed error="session active: context canceled"
```

Its cause was not established. It is recorded as an observation rather than classified as a server failure.

---

## 5. PostgreSQL and resource observations

### PostgreSQL connection and lock sampling

A PowerShell loop sampled `pg_stat_activity` approximately every two seconds during the hot-conversation rerun. The query excluded its own sampling connection.

| Metric | Observation |
|---|---:|
| Maximum other client connections in sampled snapshots | 16 |
| Samples with at least one active query | 5 |
| Samples with a lock wait | 1 |
| Samples without a lock wait | 64 |
| PostgreSQL `max_connections` | 100 |

One sample at `15:29:31` returned:

```text
14|0|1
```

This represents 14 other client connections, zero active queries, and one client backend waiting on a lock.

**Interpretation:** A transient PostgreSQL lock wait was observed in one sample. The sampler did not capture the locked relation, SQL statement, or blocking transaction. Therefore, the observation does not establish that the conversation sequence counter caused the wait. It is an unresolved finding, not a demonstrated bottleneck.

The sampled maximum of 16 client connections does not establish the application's effective pool maximum or prove that the pool was close to exhaustion.

### Docker resource snapshot

A `docker stats --no-stream` snapshot taken after the run showed:

| Container | CPU snapshot | Memory snapshot |
|---|---:|---:|
| PostgreSQL | 10.63% | 90.22 MiB |
| Redis | 4.18% | 17.38 MiB |
| SeaweedFS | 0.56% | 111.1 MiB |

These are post-run snapshots only. They are **not** peak usage, average usage, or measurements captured during the load test.

---

## 6. Conclusions

### NFR / threshold assessment

All three benchmark runs passed the thresholds configured in the k6 script:

- Acceptance latency p95 and p99 stayed below their configured limits.
- Delivery latency p95 and p99 stayed below their configured limits.
- WebSocket connection failure rate was 0%.
- Server error events were 0.

### Per-conversation contention

The hot-conversation test sustained approximately 23.75 accepted messages/second for one conversation, with 19 ms acceptance and delivery p95 latency. The two runs persisted 5,800 messages with a continuous observed sequence range.

One PostgreSQL lock wait appeared in one of 65 sampled snapshots. Because the sampler did not identify the lock target or blocker, the available evidence is insufficient to attribute it to the per-conversation sequence counter. No clear latency degradation was observed in the hot-conversation k6 results, but this test does not prove that contention is absent at higher per-conversation rates.

### What these tests establish

- The local Phase 3 WebSocket messaging path handled the three tested workloads without observed WebSocket connection failures or server error events.
- The measured latency percentiles were within the script's configured thresholds.
- The hot-conversation test persisted all 5,800 accepted messages across two runs and maintained the observed sequence range from 1 to 5,800.
- The PostgreSQL sampler observed one lock wait; its cause remains unknown.

### What these tests do not establish

- Capacity for 10k DAU or any production traffic profile.
- Maximum sustainable throughput or breaking point.
- Multi-host, multi-region, or multi-backend-replica behavior.
- Peak or average CPU and memory usage during load.
- Absence of database lock contention.
- A measured application connection-pool maximum.

### Changes required before Phase 4A

No benchmark result currently demonstrates a need to redesign the Phase 3 message path. Preserve the single-backend-replica constraint documented in ADR-010: the current push path uses in-process behavior and has not been validated across multiple backend replicas.

The single sampled PostgreSQL lock wait should remain an open observation. If future profiling shows material contention, capture the lock target, SQL, and blocking transaction before changing the sequence-allocation design. Multi-replica delivery should be addressed as part of the planned Phase 4A work and validated with dedicated tests.

**Overall assessment:** Phase 3's local benchmark is complete against the configured k6 thresholds. This is a successful local baseline, not a production-readiness or 10k-user capacity certification.
