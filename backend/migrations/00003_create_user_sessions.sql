-- +goose Up
-- One row per refresh token. Rotation inserts a new row in the same family and marks the old
-- one rotated; reuse of a rotated token revokes the whole family (see docs/LLD.md section 7).
CREATE TABLE user_sessions (
    id                 uuid        PRIMARY KEY DEFAULT uuidv7(),
    user_id            uuid        NOT NULL,
    device_id          uuid        NOT NULL,
    family_id          uuid        NOT NULL,
    refresh_token_hash bytea       NOT NULL,
    expires_at         timestamptz NOT NULL,
    rotated_at         timestamptz,
    revoked_at         timestamptz,
    created_at         timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT user_sessions_user_fk   FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE CASCADE,
    CONSTRAINT user_sessions_device_fk FOREIGN KEY (device_id, user_id) REFERENCES devices (id, user_id) ON DELETE CASCADE,
    -- Only a SHA-256 digest of the opaque token is stored, never the token itself.
    CONSTRAINT user_sessions_hash_len  CHECK (octet_length(refresh_token_hash) = 32),
    CONSTRAINT user_sessions_expiry    CHECK (expires_at > created_at)
);

-- Anticipated Phase 2 access patterns: look up by token hash, revoke a family,
-- list a user's active sessions, purge expired rows.
CREATE UNIQUE INDEX user_sessions_token_hash_key ON user_sessions (refresh_token_hash);
CREATE INDEX user_sessions_family_idx            ON user_sessions (family_id);
CREATE INDEX user_sessions_user_active_idx       ON user_sessions (user_id) WHERE revoked_at IS NULL;
CREATE INDEX user_sessions_expires_at_idx        ON user_sessions (expires_at);

-- +goose Down
DROP TABLE user_sessions;
