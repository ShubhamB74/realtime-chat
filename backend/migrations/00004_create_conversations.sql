-- +goose Up
CREATE TABLE conversations (
    id         uuid        PRIMARY KEY DEFAULT uuidv7(),
    type       text        NOT NULL,
    title      text,
    created_by uuid        REFERENCES users (id) ON DELETE SET NULL,
    -- For direct conversations: the two user ids, sorted and joined with ':'. The unique
    -- constraint guarantees exactly one direct conversation per user pair, even under races.
    direct_key text,
    -- Per-conversation message counter (docs/LLD.md section 3). Unused until Phase 3.
    last_seq   bigint      NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT conversations_type_valid CHECK (type IN ('direct', 'group')),
    CONSTRAINT conversations_shape CHECK (
        (type = 'direct' AND direct_key IS NOT NULL AND title IS NULL) OR
        (type = 'group'  AND direct_key IS NULL AND title IS NOT NULL AND char_length(title) BETWEEN 1 AND 100)
    ),
    CONSTRAINT conversations_last_seq_nonneg CHECK (last_seq >= 0),
    CONSTRAINT conversations_direct_key_key UNIQUE (direct_key)
);

CREATE TRIGGER conversations_set_updated_at
    BEFORE UPDATE ON conversations
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- +goose Down
DROP TABLE conversations;
