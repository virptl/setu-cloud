-- +goose Up
CREATE TABLE tenants (
    tid          TEXT        PRIMARY KEY,
    name         TEXT        NOT NULL,
    api_key_hash TEXT        NOT NULL,   -- bcrypt hash of the raw API key
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- +goose Down
DROP TABLE IF EXISTS tenants;
