# ADR-010: Messaging, history visibility and the real-time gateway (Phase 3)

Status: Proposed (Phase 3)
Date: 2026-11-15

## Decisions
1. **Persist first, then acknowledge, then push.** `SendMessage` runs in one PostgreSQL transaction: lock the conversation row and check membership in a single statement, check the idempotency key, bump the counter, insert, list recipients, commit. Only then does the caller acknowledge, and only then is the best-effort push published (ADR-003, ADR-005). The transactional outbox row joins this transaction in Phase 4B.
2. **Ordering.** `seq` and `mod_seq` come from the per-conversation counter inside the message transaction. The row lock is held to commit, so values are gapless and visible in commit order. Edit and delete take a new `mod_seq` and keep `seq`.
3. **Idempotency** is evaluated *inside* the conversation lock, so concurrent retries serialize and exactly one creates the message. A key reused for a different conversation is a 409. The comparison deliberately ignores the body, because a message may legitimately have been edited between retries.
4. **History visibility (resolves the open question from Phase 2): members see only messages after they joined.** `conversation_members.joined_seq` stores the counter at join time; history, sync, edit and delete all filter on `seq > joined_seq`. A removed member has no access; a re-added member starts fresh. Edits of older messages never reach later joiners. *Trade-off:* new members cannot read the backstory; the alternative (full history) is simpler but exposes content created before the person was invited.
5. **Edit and delete.** Only the sender edits. The sender or a group admin deletes; deletion is a tombstone (null body, `deleted_at`). There is no edit time limit and **the previous body is overwritten, not versioned**: a moderation or audit trail would need a separate decision.
6. **Sync in Phase 3 is client-cursor based.** The client sends `after_mod_seq` per conversation; the server stores nothing per device. Server-side device cursors, unread counts and receipts arrive in Phase 4A (ADR-007).
7. **Single gateway process.** The hub keeps connections in memory and fans out in-process. Cross-gateway routing over Redis (ADR-006) is Phase 4A. Running two replicas now would silently drop cross-replica pushes (sync still repairs them).
8. **WebSocket library: `github.com/coder/websocket`** (pkg.go.dev shows v1.8.15 published June 2026; maintained by Coder; supports concurrent writes, context-based deadlines and a ping API). Compression is disabled for predictable CPU and memory.
9. **Authentication.** Header, or `bearer.<token>` subprotocol for browsers; query tokens rejected. The session is verified at the handshake and re-verified every 60 s, closing with 4001 when revoked. Open sockets outlive access-token expiry by design; the session is the authority (resolves the REST revocation gap for sockets, ADR-009).
10. **Backpressure.** One writer goroutine per connection drains a queue bounded by **both** frame count (256) and bytes (4 MiB). Overflow closes the connection with 4002 instead of buffering; the client reconnects and syncs. Inbound events use a token bucket (20/s, burst 40) with a stricter 1/s limit on `sync.request`; persistent flooding closes the connection. Per-user (5) and global (5000) connection caps apply at the handshake.
11. **Failure containment.** Panics in a connection goroutine close that connection only. Presence (Redis) is best effort: failures are logged (throttled) and never affect messaging.
12. **Presence** is a Redis sorted set per user (connection ids scored by expiry), refreshed by heartbeats. Exposed read-only via `GET /v1/conversations/{id}/presence` for groups of up to 50; live presence events arrive in Phase 4A.

## Consequences and limitations
- Writers to one conversation serialize on the row lock. The cost is unmeasured; the Phase 3 k6 "hot conversation" run exists to measure it.
- Every message costs about six statements in one transaction (lock+authz, idempotency check, counter, insert, recipients, commit). This is the first thing to optimize if the baseline misses its targets.
- Typing events cost one membership query each, throttled to one per 2 s per conversation per connection.
- A conversation's `updated_at` (list ordering) changes on edits and deletes too, so an edit can reorder the list.
- Very large inbound bursts are bounded, but bytes in flight to a *healthy* client are not otherwise limited.
- Heartbeat timeout behavior is implemented but not covered by an automated test.

## Alternatives rejected
Full-history visibility for new members; server-side per-device cursors in Phase 3 (needs the receipts design first); gorilla/websocket (reported archived in earlier years; not re-verified); tokens in the query string; unbounded or count-only queues.
