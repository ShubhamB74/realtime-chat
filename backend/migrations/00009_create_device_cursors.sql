-- +goose Up
-- One row per (device, conversation): the highest sync cursor (mod_seq) that device has told us it
-- applied. Replaces per-message, per-device delivery rows (ADR-007): write volume scales with
-- active devices and conversations, not with messages x recipients. Rows are written by the
-- gateway in coalesced batches, so a crash can lose a couple of seconds of progress, which only
-- causes harmless re-delivery that clients deduplicate.
CREATE TABLE device_cursors (
    device_id       uuid        NOT NULL REFERENCES devices (id) ON DELETE CASCADE,
    conversation_id uuid        NOT NULL REFERENCES conversations (id) ON DELETE CASCADE,
    acked_mod_seq   bigint      NOT NULL DEFAULT 0,
    updated_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (device_id, conversation_id),
    CONSTRAINT device_cursors_nonneg CHECK (acked_mod_seq >= 0)
);

-- "Delivered up to N for this user in this conversation" aggregates over the conversation's rows.
CREATE INDEX device_cursors_conversation_idx ON device_cursors (conversation_id);

-- +goose Down
DROP TABLE device_cursors;
