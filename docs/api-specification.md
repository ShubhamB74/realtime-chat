# API Specification

Status: REST API for Phase 2 specified and implemented (not yet executed against a live database, see docs/phase-2-acceptance-report.md). The WebSocket protocol is drafted in LLD.md and arrives in Phase 3.

**Source of truth:** [`packages/openapi/openapi.yaml`](../packages/openapi/openapi.yaml) (OpenAPI 3.0.3, hand-maintained). A unit test (`TestEveryRouteIsInOpenAPI`) fails if a registered route is missing from it; request and response *shapes* are not machine-checked yet. View it with any OpenAPI viewer (for example Swagger Editor).

## Endpoints (22)
| Area | Endpoints |
|---|---|
| Auth | `POST /v1/auth/register`, `/login`, `/refresh`, `/logout` |
| Profile | `GET`/`PATCH /v1/me`, `GET /v1/users/lookup?email=` |
| Devices and sessions | `GET /v1/devices`, `DELETE /v1/devices/{id}`, `GET /v1/sessions`, `DELETE /v1/sessions/{id}` |
| Conversations | `POST /v1/conversations/direct`, `POST /v1/conversations/groups`, `GET /v1/conversations`, `GET`/`PATCH /v1/conversations/{id}` |
| Membership | `GET`/`POST /v1/conversations/{id}/members`, `DELETE /v1/conversations/{id}/members/{userId}`, `PUT .../members/{userId}/role` |
| Settings | `GET`/`PATCH /v1/conversations/{id}/settings` |

## Conventions
- Wire format is JSON with snake_case fields. Unknown request fields are rejected (400). Bodies are limited to 64 KiB (413).
- Authentication: `Authorization: Bearer <access_token>`. Access tokens last 15 minutes; renew with `/auth/refresh`.
- Errors use the shared contract `{code, message, request_id, retryable}`; see docs/LLD.md section 6. Status mapping: 400 `invalid_request`, 401 `unauthenticated`, 403 `forbidden`, 404 `not_found`, 409 `conflict`, 413 `payload_too_large`, 429 `rate_limited` (with `Retry-After`), 503 `unavailable`, 500 `internal`.
- **Existence is not leaked:** a conversation, session or device the caller may not access returns the same 404 as one that does not exist. 403 is used only when the caller is a member but lacks the admin role.
- Identifiers are UUIDs (v7). Malformed ids return 404.
- Conversation list pagination is keyset-based (`limit` 1 to 100, default 30; opaque `cursor` from `next_cursor`). `last_message` and `unread_count` are reserved fields (null/0 until Phases 3 and 4A).

## Token lifecycle for clients
1. `POST /auth/login` with a device description. Store `refresh_token` and `device_id` securely; reuse `device.id` on later logins from the same install so devices are not duplicated (limit: 20 per user).
2. Send `access_token` on every call. On 401, call `/auth/refresh` **once** (serialize concurrent refreshes), then retry.
3. Always replace the stored refresh token with the one just returned. Replaying an older token revokes the whole session (ADR-009).
