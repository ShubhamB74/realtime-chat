# Phase 3 Baseline (template)

**Status: NOT RUN.** No numbers exist. This file is a template to fill in after running
`tests/load/k6/chat.js`. Do not quote any figure from the NFR document as a result.

## Environment (fill in)
| Item | Value |
|---|---|
| Date | |
| Machine (CPU model, cores, RAM) | |
| OS / kernel | |
| k6 version | |
| Go version (backend build) | |
| PostgreSQL version and settings changed from defaults | |
| Redis version | |
| Backend config differences from defaults (`WS_*`, `ARGON2_*`, `DB_MAX_CONNS`) | |
| k6 and backend on the same host? | |

## Runs (fill in)
| Run | Command | VUs | Msg/s accepted | accept p50 / p95 / p99 (ms) | delivery p50 / p95 / p99 (ms) | connect failures | server errors | notes |
|---|---|---|---|---|---|---|---|---|
| 1. Baseline | `make load-baseline` | 100 | | | | | | |
| 2. Higher rate | `-e SEND_INTERVAL_MS=500` | 100 | | | | | | |
| 3. Hot conversation | seed with one group of 50 | 50 | | | | | | |

## Also record
- Backend `gateway stats` log lines (connections, slow consumers, rejected).
- PostgreSQL: CPU, lock waits on `conversations` rows during run 3, connection count vs `DB_MAX_CONNS`.
- Whether any threshold failed and the first suspected cause.

## Conclusions (fill in after the runs)
- Is per-conversation counter contention visible in run 3? At what message rate per conversation?
- Which NFR targets are met, missed or still unmeasured?
- What changes are needed before Phase 4A?
