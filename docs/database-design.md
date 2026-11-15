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
