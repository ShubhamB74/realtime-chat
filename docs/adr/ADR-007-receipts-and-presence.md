# ADR-007: Receipts as cursors and scoped presence

Status: Proposed (new)
Date: 2026-11-13

## Decision
**Receipts:**
- No per-message, per-device receipt rows.
- `device_cursors.delivered_seq` per device and conversation, advanced by `message.ack(up_to_seq)`; gateways coalesce cursor flushes (assumed every few seconds, tune by measurement).
- `conversation_members.read_seq` per user and conversation, advanced by `read.update`.
- Delivered/read status for a message is derived: seq at or below a cursor. In groups, the UI shows counts or "seen by" on demand, not per-recipient push.
- Receipt broadcasts and `chat.receipt.updated.v1` events are coalesced per conversation per user per interval.

**Presence:**
- Redis keys with TTL refreshed by heartbeat, plus a grace period (assumed ~5-10 s) before offline to avoid flapping.
- Clients subscribe only to presence they can see (open conversation, visible list items, capped count).
- No presence broadcast in groups above a size threshold (assumed 50).
- Typing indicators are ephemeral pub/sub, rate-limited, not persisted.

## Consequences
+ Write volume scales with devices and active conversations, not with messages times recipients.
- Less granular per-recipient history of when a message was delivered.
- Cursor coalescing means a crash can lose a few seconds of cursor progress; effect is harmless re-delivery that clients dedupe.
