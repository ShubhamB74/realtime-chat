-- +goose Up
-- History visibility rule (ADR-010): a member sees only messages with seq greater than the
-- conversation counter value at the moment they joined. Re-adding a removed member resets it.
ALTER TABLE conversation_members
    ADD COLUMN joined_seq bigint NOT NULL DEFAULT 0,
    ADD CONSTRAINT conversation_members_joined_seq_nonneg CHECK (joined_seq >= 0);

-- +goose Down
ALTER TABLE conversation_members
    DROP CONSTRAINT conversation_members_joined_seq_nonneg,
    DROP COLUMN joined_seq;
