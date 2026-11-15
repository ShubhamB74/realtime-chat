-- +goose Up
CREATE TABLE devices (
    id           uuid        PRIMARY KEY DEFAULT uuidv7(),
    user_id      uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    name         text        NOT NULL,
    platform     text        NOT NULL,
    last_seen_at timestamptz,
    revoked_at   timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT devices_name_length   CHECK (char_length(name) BETWEEN 1 AND 100),
    CONSTRAINT devices_platform_valid CHECK (platform IN ('web', 'ios', 'android', 'desktop', 'other')),
    -- Lets user_sessions reference (device, user) together so a session can never
    -- point at a device that belongs to a different user.
    CONSTRAINT devices_id_user_key   UNIQUE (id, user_id)
);

CREATE INDEX devices_user_id_idx ON devices (user_id);

CREATE TRIGGER devices_set_updated_at
    BEFORE UPDATE ON devices
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- +goose Down
DROP TABLE devices;
