# ADR-003: Message persistence and ordering

Status: Proposed (revision 2)
Date: 2026-11-13

## Decision
1. One transaction: bump the conversation counter, insert the message with `seq` and `mod_seq`, insert the outbox row.
2. Acknowledge only after commit.
3. The outbox publisher publishes to Kafka and marks rows published after broker ack. Duplicates are possible; consumers are idempotent.
4. Clients send an idempotency key; unique `(sender_device_id, idempotency_key)` makes retries safe.
5. **Ordering authority is `(conversation_id, seq)`**, never Kafka offsets. Kafka partitioning by `conversation_id` gives consumers locality and per-conversation publish order only.
6. Edits and deletes bump the counter and set a new `mod_seq`, so sync can find every change.

## Why seq is safe for cursors
The counter row lock is held until commit, so sequence values become visible in order and without gaps. A reader holding cursor N can never later see a committed seq below N.

## Durability
Depends on actual PostgreSQL commit and replication settings. RPO = 0 holds only for the configuration tested in Phases 3 and 8.

## Consequences
+ No DB/Kafka dual-write problem; recoverable after worker or broker failure.
+ Sync by `mod_seq` handles edits and deletes uniformly.
- Writers to one conversation are serialized; throughput per conversation is bounded by commit latency (to be measured).
- Outbox needs pruning and lag monitoring.
