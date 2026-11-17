# Database Design

Status: Phase 1 tables implemented as migrations (not yet applied or tested against a live database in the authoring environment).

Planned core: users, user_sessions, devices (Phase 1); conversations (with `last_seq`), conversation_members (with `read_seq`), messages (`seq`, `mod_seq`), device_cursors, outbox_events (see LLD sections 2-3). The AI store is a separate database (ADR-004).

## Phase 1 schema (backend/migrations)
Migrations use goose (`-- +goose Up/Down`), run via `make migrate-up`. Requires **PostgreSQL 18** for the built-in `uuidv7()`.

**users**: `id uuid` (uuidv7), `email`, `display_name`, `password_hash`, `status` (`active|disabled`), timestamps. Unique index on `lower(email)` (case-insensitive without the citext extension). Check constraints on lengths and a basic `@` check; real email validation stays in application code.

**devices**: `id`, `user_id` -> users (cascade), `name`, `platform` (`web|ios|android|desktop|other`), `last_seen_at`, `revoked_at`, timestamps. `UNIQUE (id, user_id)` exists so sessions can reference both together.

**user_sessions**: one row per refresh token. `user_id`, `device_id`, `family_id` (the stable session identity across rotations), `refresh_token_hash bytea` (SHA-256 digest, 32 bytes, unique), `expires_at`, `rotated_at`, `revoked_at`. The composite FK `(device_id, user_id) -> devices(id, user_id)` guarantees a session cannot point at another user's device.

**Decisions to confirm in Phase 2**
- Hash length 32 assumes SHA-256 of a random opaque token. Changing it needs a new migration.
- No IP address or user agent is stored, to minimize personal data. Add if a security feature needs it.
- Indexes (hash lookup, family revoke, active sessions per user, expiry purge) are anticipated from the Phase 2 design, not measured. Revisit with real query plans.
- `set_updated_at()` is a shared trigger function; later tables reuse it.

## Phase 2 schema (migrations 00004 to 00006)
**conversations**: `id`, `type` (`direct|group`), `title` (groups only), `created_by` (nullable, set null if the user is deleted), `direct_key` (sorted `userA:userB`, unique, direct only), `last_seq bigint` (reserved for Phase 3), timestamps. A CHECK constraint enforces the shape: direct means key present and no title; group means title present and no key.

**conversation_members**: PK `(conversation_id, user_id)`, `role` (`admin|member`), `read_seq` (reserved for Phase 4A), `joined_at`, `removed_at`. Removal is soft; a removed member is treated as a non-member everywhere. Re-adding resets `joined_at`, role and `read_seq`. Partial index `(user_id, conversation_id) WHERE removed_at IS NULL` serves the hot path (listing and authorization).

**conversation_settings**: one row per conversation, `ai_enabled boolean default false` (ADR-004 conversation-level AI control).

**Rules enforced in code inside locked transactions** (not by constraints): direct conversations have exactly two members and fixed membership; a group always keeps at least one admin (leaving promotes the longest-standing member; demoting the last admin is refused); group size at most 256.

**Query patterns behind the indexes:** membership lookup by `(conversation_id, user_id)` (primary key); "my conversations" by `user_id` on active rows, ordered by `conversations.updated_at, id` with keyset pagination. The ordering index on `conversations` was deliberately **not** added; add it only if query plans from real data show a need.

**Known gaps:** empty groups (everyone left) remain as rows; `last_message` and unread are not stored yet; no purge of old sessions.

## Phase 3 schema (migrations 00007 and 00008)
**messages**: `id`, `conversation_id`, `sender_id` and `sender_device_id` (both nullable, set null on deletion), `idempotency_key`, `seq`, `mod_seq`, `body` (null for tombstones), `edited_at`, `deleted_at`, `created_at`. CHECK constraints: `seq > 0`, `mod_seq >= seq`, key length 8 to 64, and the body state rule (live: 1 to 4000 characters; deleted: null). Unique indexes: `(conversation_id, seq)` (the ordering authority), `(conversation_id, mod_seq)` (each counter value is held by one message; also serves sync) and `(sender_device_id, idempotency_key)` (idempotent retries).

**conversation_members.joined_seq** (new column): the conversation counter when the member joined; visibility is `seq > joined_seq`. Reset when a removed member is re-added.

**Counter discipline** (see ADR-003/010): every message write first locks the conversation row (`FOR UPDATE`) while verifying active membership in the same statement, then bumps `conversations.last_seq`. Rolled-back transactions undo the bump, so numbering has no gaps.

**Query patterns:** history `WHERE conversation_id AND seq > joined_seq AND seq < before ORDER BY seq DESC` (unique `(conversation_id, seq)` index); sync `WHERE conversation_id AND mod_seq > cursor AND seq > joined_seq ORDER BY mod_seq` (unique `(conversation_id, mod_seq)` index); last-message preview via a lateral subquery on the newest `seq`. Indexes are derived from these patterns, **not yet validated with `EXPLAIN` on realistic data**.

**Known gaps:** no outbox table yet (Phase 4B); edits overwrite the body with no history; no full-text index (Phase 6); no partitioning or archival (Phase 8); `sender_device_id` is set null when a device row is deleted, which weakens idempotency for that device only.
