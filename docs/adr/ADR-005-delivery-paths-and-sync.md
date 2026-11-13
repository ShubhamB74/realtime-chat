# ADR-005: Delivery paths and sync model

Status: Proposed (new)
Date: 2026-11-13

## Context
Revision 1 routed every delivery through outbox, Kafka, worker, and Redis lookup. That adds hops and poll latency to the hot path, and Kafka is not needed for correctness once clients can sync.

## Decision
Two paths:
1. **Fast path (best effort):** after commit, the receiving gateway publishes the message envelope to the Redis pub/sub channel of each gateway holding a recipient connection (ADR-006). Latency target: p95 < 300 ms (unvalidated).
2. **Reliable path:** outbox then Kafka, feeding derived pipelines only: notifications, media, AI, analytics.

**Sync is the correctness mechanism.** Messages are stored once per conversation. Each device has a server-side cursor per conversation. On reconnect, `sync.request` returns messages whose `mod_seq` exceeds the device cursor, paged. Clients:
- dedupe by `(conversation_id, seq, mod_seq)` through one idempotent reducer used for both fast-path and sync data,
- detect gaps (received `seq` jumps) and request gap-fill,
- run a periodic reconcile sync (assumed 60 s while connected) to catch trailing lost pushes.

## Alternatives
- **Per-user update log** (Telegram `pts`, Matrix sync tokens): single-stream reconnect, but write amplification in large groups. Rejected for group sizes up to 256 (assumed); revisit if measurements show reconnect cost is a problem.
- **Kafka in the delivery path:** rejected (latency, complexity, no correctness benefit).

## Consequences
+ Redis or gateway failure degrades latency, not correctness.
+ Reconnect storms are one indexed query per device, not per conversation round trip.
- Two delivery mechanisms to test; the idempotent reducer and gap/reconcile logic are mandatory.
- Worst-case staleness after fast-path loss is bounded by the reconcile interval.
