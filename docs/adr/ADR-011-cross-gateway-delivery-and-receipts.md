# ADR-011: Cross-gateway delivery, receipts, unread counts and presence subscriptions (Phase 4A)

Status: Proposed (Phase 4A). Implements ADR-005, ADR-006 and ADR-007; refines them where noted.
Date: 2026-11-16

## Decisions
**Routing (ADR-006, as built)**
1. **The registry reuses the presence structure.** Redis sorted set per user, members `"<gatewayID>/<connID>"` scored by expiry time. One structure answers "is the user online?" and "which gateways hold their connections?". Entries expire unless refreshed by the heartbeat (3 x the ping interval, 75 s by default).
2. **Per-gateway pub/sub channels plus one broadcast channel** (`gwbus.Bus`, Redis implementation). Frames are serialized once and carried as bytes; the receiving gateway writes them to its sockets without re-encoding.
3. **Local first, remote best effort.** `Hub.Publish` delivers to this gateway's connections immediately, then queues a remote task (bounded queue of 1024, 4 workers). A worker does one pipelined registry lookup and publishes one bus message per *other* gateway. If the registry or bus fails, or the queue is full, live delivery to other gateways is skipped and counted (`remote_failed`, `remote_dropped`); those clients converge through sync. Local delivery never waits for Redis.
4. The same path carries typing indicators, message updates and receipt events. Presence changes use the broadcast channel (every gateway, including the sender).
5. The bus subscription is kept alive by a retry loop with exponential backoff and jitter.

**Receipts and unread (ADR-007, as built)**
6. **`message.ack {conversation_id, up_to_mod_seq}`** (refines the LLD's `up_to_seq`, because the sync cursor is a `mod_seq`): "this device has applied every change up to N". Stored in `device_cursors` (migration 00009).
7. **`read.update {conversation_id, up_to_seq}`** moves `conversation_members.read_seq` (a message `seq`).
8. **All cursors are validated in SQL**: caller must be an active member and (for acks) own a non-revoked device; values only move forward (`GREATEST`) and are capped at the conversation counter, so a client cannot invent future positions.
9. **Coalescing:** acks and read marks are buffered per gateway and written every 2 s (one transaction per flush, keys sorted to avoid lock-order deadlocks). A crash loses at most that window, which only causes harmless re-delivery that clients deduplicate. Shutdown flushes.
10. **Derived status:** a message is delivered to a user when `seq <=` the highest cursor across their live devices, and read when `seq <= read_seq`.
11. **Who hears about receipts:** in direct conversations both participants get `receipt.updated`; in groups only the member's own devices hear about their read progress, and "seen by" is available on demand (`GET .../receipts`). Unchanged states are not re-pushed.
12. **Unread count** (conversation list): messages from other people, not deleted, with `seq` above `max(read_seq, joined_seq)`, counted up to a cap of 100.

**Sync (ADR-005, as built)**
13. **Server-driven sync:** `sync.request` without a conversation list lets the server find conversations with changes beyond the device's cursors (`c.last_seq > cursor`, one indexed query, up to 20 per request). An explicit list still works.
14. **`last_seq` is a safe cursor.** `ListChanges` now reads the counter first and bounds the changes query by it. Because counter values become visible in commit order, every change up to `last_seq` is already returned (or reachable through `has_more`), so a client may acknowledge `last_seq` once `has_more` is false. (The first version of `ListChanges` read the counter after the messages and could skip a concurrent change.) This also clears conversations whose only new activity is invisible to the member (edits of messages from before they joined).

**Presence subscriptions**
15. `presence.subscribe {user_ids}` is accepted only for users the caller shares an active conversation with that has at most 50 members; others are silently omitted. At most 50 subscriptions per connection. The reply is `presence.snapshot`; changes arrive as `presence.changed`.
16. Online transitions are detected through the registry (first connection anywhere / last connection anywhere), and "offline" is announced after a grace period (8 s) unless the user came back, which absorbs reconnects and page reloads.

## Consequences and limitations
- **Remote delivery order is not guaranteed.** Several workers send concurrently, so two messages to the same remote gateway can arrive out of order. Clients must sort by `seq` and treat gaps as a reason to sync (already a protocol rule).
- **Crashed gateways stay routable for up to 75 s** (entry lifetime), so pushes to them are lost and their users look online for that long. Sync repairs delivery; presence is briefly wrong.
- **A client that connects while Redis is down is not registered** until its next heartbeat (up to 25 s), so it misses cross-gateway pushes in that window (sync repairs).
- **Redis is a trust boundary.** Anything that can publish to the bus channels can inject frames into users' sockets. Redis must require authentication and be reachable only from the gateways (docs/security.md).
- Presence broadcasts reach every gateway: cost grows with the number of gateways times the transition rate. Fine for a few gateways; revisit beyond that.
- Read receipts are always on; there is no per-user opt-out yet.
- A device that acknowledges a cursor while an older, since-edited message was still pending can briefly over-report delivery. Clients should acknowledge only the contiguous cursor they have actually applied.
- The unread count scans from the read cursor and filters out the member's own messages; a member who sent thousands of messages since last reading pays for that scan. Unmeasured.
- **Not implemented here:** instant disconnect on session revocation through the bus (the 60 s re-validation remains), per-conversation channels, NATS, receipts opt-out, the transactional outbox and Kafka (Phase 4B).

## Alternatives rejected
Per-message delivery rows (write amplification, ADR-007); Kafka for live fan-out (ADR-005); one bus channel per conversation (subscription management and authorization on subscribe; revisit if group fan-out dominates); pushing group receipts to every member (fan-out explosion); a second Redis structure just for routing (duplicates presence for no gain).
