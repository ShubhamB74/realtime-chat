-- +goose Up
-- Membership is soft-removed (removed_at) so history and audit questions stay answerable.
-- A removed member is "not a member" for every authorization check.
CREATE TABLE conversation_members (
    conversation_id uuid        NOT NULL REFERENCES conversations (id) ON DELETE CASCADE,
    user_id         uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    role            text        NOT NULL DEFAULT 'member',
    read_seq        bigint      NOT NULL DEFAULT 0,
    joined_at       timestamptz NOT NULL DEFAULT now(),
    removed_at      timestamptz,
    PRIMARY KEY (conversation_id, user_id),
    CONSTRAINT conversation_members_role_valid    CHECK (role IN ('admin', 'member')),
    CONSTRAINT conversation_members_read_seq_nonneg CHECK (read_seq >= 0),
    CONSTRAINT conversation_members_removed_after_join CHECK (removed_at IS NULL OR removed_at >= joined_at)
);

-- "Conversations of this user": the hottest query (listing and every authorization check).
CREATE INDEX conversation_members_user_active_idx
    ON conversation_members (user_id, conversation_id) WHERE removed_at IS NULL;

-- +goose Down
DROP TABLE conversation_members;
