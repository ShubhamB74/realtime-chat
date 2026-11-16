-- +goose Up
-- Conversation-level settings, one row per conversation. ai_enabled implements the
-- conversation-level AI control required by ADR-004; it defaults to OFF.
CREATE TABLE conversation_settings (
    conversation_id uuid        PRIMARY KEY REFERENCES conversations (id) ON DELETE CASCADE,
    ai_enabled      boolean     NOT NULL DEFAULT false,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);

CREATE TRIGGER conversation_settings_set_updated_at
    BEFORE UPDATE ON conversation_settings
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- +goose Down
DROP TABLE conversation_settings;
