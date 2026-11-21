# API Specification

Status: REST API through Phase 4A specified and implemented (not yet executed against live services; see the acceptance reports). The WebSocket protocol is in [websocket-protocol.md](websocket-protocol.md).

**Source of truth:** [`packages/openapi/openapi.yaml`](../packages/openapi/openapi.yaml) (OpenAPI 3.0.3, hand-maintained). A unit test (`TestEveryRouteIsInOpenAPI`) fails if a registered route is missing from it; request and response *shapes* are not machine-checked yet. View it with any OpenAPI viewer (for example Swagger Editor).

## Endpoints (31, plus the WebSocket upgrade `GET /v1/ws`)
| Area | Endpoints |
|---|---|
| Auth | `POST /v1/auth/register`, `/login`, `/refresh`, `/logout` |
| Profile | `GET`/`PATCH /v1/me`, `GET /v1/users/lookup?email=` |
| Devices and sessions | `GET /v1/devices`, `DELETE /v1/devices/{id}`, `GET /v1/sessions`, `DELETE /v1/sessions/{id}` |
| Conversations | `POST /v1/conversations/direct`, `POST /v1/conversations/groups`, `GET /v1/conversations`, `GET`/`PATCH /v1/conversations/{id}` |
| Membership | `GET`/`POST /v1/conversations/{id}/members`, `DELETE /v1/conversations/{id}/members/{userId}`, `PUT .../members/{userId}/role` |
| Settings | `GET`/`PATCH /v1/conversations/{id}/settings` |
| Messages | `POST`/`GET /v1/conversations/{id}/messages`, `PATCH`/`DELETE /v1/conversations/{id}/messages/{messageId}` |
| Sync and presence | `GET /v1/conversations/{id}/changes?after_mod_seq=`, `GET /v1/conversations/{id}/presence` |
| Receipts | `POST /v1/conversations/{id}/read`, `GET /v1/conversations/{id}/receipts` |
| Real time | `GET /v1/ws` (WebSocket upgrade) |

## Conventions
- Wire format is JSON with snake_case fields. Unknown request fields are rejected (400). Bodies are limited to 64 KiB (413).
- Authentication: `Authorization: Bearer <access_token>`. Access tokens last 15 minutes; renew with `/auth/refresh`.
- Errors use the shared contract `{code, message, request_id, retryable}`; see docs/LLD.md section 6. Status mapping: 400 `invalid_request`, 401 `unauthenticated`, 403 `forbidden`, 404 `not_found`, 409 `conflict`, 413 `payload_too_large`, 429 `rate_limited` (with `Retry-After`), 503 `unavailable`, 500 `internal`.
- **Existence is not leaked:** a conversation, session or device the caller may not access returns the same 404 as one that does not exist. 403 is used only when the caller is a member but lacks the admin role.
- Identifiers are UUIDs (v7). Malformed ids return 404.
- Conversation list pagination is keyset-based (`limit` 1 to 100, default 30; opaque `cursor` from `next_cursor`). `last_message` (latest visible message, truncated to 140 characters) appears in list responses; `last_seq` is the conversation counter; `unread_count` (list responses) counts messages from others after your read cursor, capped at 100.

## Token lifecycle for clients
1. `POST /auth/login` with a device description. Store `refresh_token` and `device_id` securely; reuse `device.id` on later logins from the same install so devices are not duplicated (limit: 20 per user).
2. Send `access_token` on every call. On 401, call `/auth/refresh` **once** (serialize concurrent refreshes), then retry.
3. Always replace the stored refresh token with the one just returned. Replaying an older token revokes the whole session (ADR-009).

## Messaging notes (Phase 3)
- **Send** over the WebSocket (`message.send`) or `POST .../messages`; both go through the same service and both require an `idempotency_key`. The message is committed before either responds.
- **History** (`GET .../messages`) is newest first, keyset-paginated by `before_seq`. **Sync** (`GET .../changes`) is ordered by `mod_seq` and includes edits and deletions; use it after a reconnect (it is the REST twin of the WebSocket `sync.request`).
- Members only see messages from after they joined (ADR-010). Deleted messages appear as tombstones with `body: null`.
- Bodies: 1 to 4000 characters, not blank, no control characters except newline, carriage return and tab.

## Receipts notes (Phase 4A)
- `POST .../read` moves your read cursor forward immediately (never backwards, capped at the conversation counter) and returns 204; a non-member gets 404. The WebSocket `read.update` does the same but is coalesced.
- `GET .../receipts` returns every member's `delivered_seq` and `read_seq` (members only). Delivery acknowledgements are sent over the WebSocket (`message.ack`); REST-only clients cannot acknowledge delivery yet.
- `GET .../changes`: `last_seq` is read before the changes, so when `has_more` is false it is a safe cursor to continue from.
