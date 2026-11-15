-- +goose Up
-- Requires PostgreSQL 18+ for uuidv7() (time-ordered UUIDs for index locality).

-- +goose StatementBegin
CREATE FUNCTION set_updated_at() RETURNS trigger AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TABLE users (
    id            uuid        PRIMARY KEY DEFAULT uuidv7(),
    email         text        NOT NULL,
    display_name  text        NOT NULL,
    password_hash text        NOT NULL,
    status        text        NOT NULL DEFAULT 'active',
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT users_email_length        CHECK (char_length(email) BETWEEN 3 AND 320),
    CONSTRAINT users_email_has_at        CHECK (position('@' IN email) > 1),
    CONSTRAINT users_display_name_length CHECK (char_length(display_name) BETWEEN 1 AND 100),
    CONSTRAINT users_status_valid        CHECK (status IN ('active', 'disabled'))
);

-- Case-insensitive uniqueness without requiring the citext extension.
CREATE UNIQUE INDEX users_email_lower_key ON users (lower(email));

CREATE TRIGGER users_set_updated_at
    BEFORE UPDATE ON users
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- +goose Down
DROP TABLE users;
DROP FUNCTION set_updated_at();
