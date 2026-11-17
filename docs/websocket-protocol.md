# WebSocket Protocol v1

Status: implemented in Phase 3, **not yet executed against a live server** (see docs/phase-3-acceptance-report.md). Extends the draft in docs/LLD.md section 4. Endpoint: `GET /v1/ws` (through the dev proxy: `/api/v1/ws`).

## Connecting
- **Non-browser clients:** `Authorization: Bearer <access_token>`.
- **Browsers** (cannot set headers): offer two subprotocols, `rtc.v1` and `bearer.<access_token>`. The server selects `rtc.v1`.
- **Query-string tokens are rejected** (URLs leak into logs and history).
- The handshake fails with HTTP 401 for a missing/invalid/expired token or a **revoked session**, 429 when the user has too many connections (default 5), 503 when the gateway is full or shutting down.
- Browser origins must match `WS_ALLOWED_ORIGINS`; clients that send no `Origin` header are unaffected.
- After the handshake the server sends `connection.ready`. The access token is **not** re-checked for expiry on an open connection; the **session** is (every 60 s by default). Revoking the session closes the socket with code 4001.

## Frames
Text frames only, JSON, at most 32 KiB inbound (larger frames close the connection with 1009). Binary frames close it with 1003.

```json
{ "v": 1, "type": "message.send", "event_id": "...", "request_id": "client-chosen", "ts": "RFC3339", "payload": { } }
```
`v` must be 1. `request_id` is echoed on every direct response to a client event. Server-pushed events have no `request_id`. Unknown payload fields are rejected.

## Client events
| Type | Payload | Responses |
|---|---|---|
| `message.send` | `{conversation_id, idempotency_key, body}` | `message.accepted` or `error` |
| `sync.request` | `{conversations: [{conversation_id, after_mod_seq}]}` (1 to 20 items; one request per second) | one `sync.batch` per conversation, or `error` |
| `typing.start`, `typing.stop` | `{conversation_id}` | none (best effort); `error` on failure |

Specified but **not implemented yet**, answered with `error` code `unsupported_event` (so clients can feature-detect): `message.ack`, `read.update`, `presence.update`, `presence.subscribe` (Phase 4A), `conversation.join`, `conversation.leave` (not needed: members receive their conversations' events automatically).

## Server events
| Type | Payload |
|---|---|
| `connection.ready` | `{connection_id, user_id, device_id, heartbeat_interval_ms, max_frame_bytes, server_time}` |
| `message.accepted` | `{message, duplicate}`. Sent to the sending connection **only after the message is committed to PostgreSQL** |
| `message.received` | a `Message` (below), pushed to every other connection of every member, including the sender's other devices |
| `message.updated` | a `Message` after an edit or a delete (a deleted message has `body: null`), pushed to all member connections |
| `typing.update` | `{conversation_id, user_id, typing}` to the other members |
| `sync.batch` | `{conversation_id, status: "ok" or "not_found", changes: [Message], has_more, last_seq, done}` |
| `error` | `{code, message, retryable}` with codes `invalid_request`, `unsupported_version`, `unsupported_event`, `forbidden`, `not_found`, `conflict`, `rate_limited`, `unavailable`, `internal` |

`Message`: `{id, conversation_id, sender_id, seq, mod_seq, body, edited_at, deleted_at, created_at, idempotency_key?}`.

## Ordering and delivery semantics (what is and is not guaranteed)
- `seq` is the per-conversation display order: gapless, assigned in commit order, immutable. Always sort by `seq`.
- `mod_seq` changes on every edit or delete and is the sync cursor. Your cursor for a conversation is the highest `mod_seq` you have applied.
- Push delivery is **best effort and at-least-once**: a message can arrive twice (push and sync) and a push can be lost (slow consumer, restart). **Clients must apply events with an idempotent reducer** keyed by message `id`, keeping the highest `mod_seq` seen, and must repair gaps by syncing.
- Not guaranteed: ordering across conversations; delivery of typing events; arrival order of `message.accepted` relative to `message.received` for other devices.
- Edits and deletes of messages from before you joined a conversation are never sent to you (join-point rule, ADR-010).

## Client responsibilities
1. **Idempotency keys:** generate one per logical send (8 to 64 chars of letters, digits, `.`, `_`, `-`) and reuse it when retrying after a timeout or reconnect. A retry returns the original message with `duplicate: true`.
2. **Reconnect with exponential backoff and jitter** (for example 0.5 s doubling to 30 s, randomized). After every (re)connect send `sync.request` with your cursors, and also for conversations you have never synced (cursor 0), found via `GET /v1/conversations` (compare `last_seq` with what you hold).
3. **Gap detection:** if a pushed `seq` is more than one above what you hold for that conversation, sync it.
4. **Periodic reconcile:** while connected, sync active conversations about once a minute to catch trailing lost pushes.
5. **Serialize token refresh** (ADR-009). The socket stays open across access-token expiry, but a reconnect needs a fresh token.

## Close codes
| Code | Meaning | Client action |
|---|---|---|
| 1000 | normal | none |
| 1001 | server restarting (graceful shutdown) | reconnect with backoff, then sync |
| 1003 / 1009 | binary frame / frame too large | fix the client |
| 1008 | persistent rate-limit abuse | slow down |
| 1011 | internal error | reconnect with backoff, then sync |
| 4001 | session revoked | log in again |
| 4002 | slow consumer: the outbound queue (256 frames or 4 MiB) overflowed | reconnect, then sync |

## Server limits (defaults, unmeasured)
5 connections per user, 5000 per process, 256 queued frames or 4 MiB per connection, 20 events/s with burst 40, 25 s ping with 10 s pong timeout, 10 s write timeout, 4000-character message bodies.
