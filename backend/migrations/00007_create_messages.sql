-- +goose Up
-- Messages are stored once per conversation (ADR-005). `seq` is the immutable display order;
-- `mod_seq` is bumped from the same per-conversation counter on every edit or delete, so a sync
-- by "mod_seq greater than my cursor" sees every change (docs/LLD.md section 3).
CREATE TABLE messages (
    id               uuid        PRIMARY KEY DEFAULT uuidv7(),
    conversation_id  uuid        NOT NULL REFERENCES conversations (id) ON DELETE CASCADE,
    -- Nullable so a deleted account does not delete other people's conversations.
    sender_id        uuid        REFERENCES users (id) ON DELETE SET NULL,
    sender_device_id uuid        REFERENCES devices (id) ON DELETE SET NULL,
    idempotency_key  text        NOT NULL,
    seq              bigint      NOT NULL,
    mod_seq          bigint      NOT NULL,
    body             text,
    edited_at        timestamptz,
    deleted_at       timestamptz,
    created_at       timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT messages_seq_positive   CHECK (seq > 0),
    CONSTRAINT messages_mod_seq_order  CHECK (mod_seq >= seq),
    CONSTRAINT messages_idempotency_key_shape CHECK (char_length(idempotency_key) BETWEEN 8 AND 64),
    -- A deleted message is a tombstone: no body. A live message has a non-empty body.
    CONSTRAINT messages_body_state CHECK (
        (deleted_at IS NULL AND body IS NOT NULL AND char_length(body) BETWEEN 1 AND 4000) OR
        (deleted_at IS NOT NULL AND body IS NULL)
    )
);

-- Exactly one message per (conversation, seq): the ordering authority.
CREATE UNIQUE INDEX messages_conversation_seq_key ON messages (conversation_id, seq);
-- Each counter value is held by exactly one message's current mod_seq; also serves sync queries.
CREATE UNIQUE INDEX messages_conversation_mod_seq_key ON messages (conversation_id, mod_seq);
-- Client retries of the same send return the original message instead of duplicating it.
CREATE UNIQUE INDEX messages_idempotency_key ON messages (sender_device_id, idempotency_key);

-- +goose Down
DROP TABLE messages;
