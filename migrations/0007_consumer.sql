-- +goose Up

-- Consumer tenant that owns all first-party app devices.
-- api_key_hash is a bcrypt of a random string; the app never uses tenant auth.
INSERT INTO tenants (tid, name, api_key_hash)
VALUES ('setu', 'Setu Consumer', '$2a$10$7EqJtq98hPqEX7fNZaFWoOhi5sfHc.RhVQ8e0m3a2k3aQv8m8Qb1m')
ON CONFLICT (tid) DO NOTHING;

CREATE TABLE app_users (
    id                UUID        PRIMARY KEY,
    email             TEXT        UNIQUE,            -- NULL for pure guests
    email_verified_at TIMESTAMPTZ,
    display_name      TEXT,
    is_guest          BOOLEAN     NOT NULL DEFAULT false,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE otp_codes (
    id          UUID        PRIMARY KEY,
    email       TEXT        NOT NULL,
    code_hash   TEXT        NOT NULL,                -- bcrypt of the 6-digit code
    expires_at  TIMESTAMPTZ NOT NULL,
    attempts    INT         NOT NULL DEFAULT 0,
    consumed_at TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_otp_email ON otp_codes (email, created_at DESC);

-- Maps an app user to a platform device (did under tenant 'setu').
CREATE TABLE app_devices (
    id          UUID        PRIMARY KEY,
    owner_id    UUID        NOT NULL REFERENCES app_users(id) ON DELETE CASCADE,
    tid         TEXT        NOT NULL DEFAULT 'setu',
    did         TEXT        NOT NULL,
    pid         TEXT        NOT NULL,
    name        TEXT        NOT NULL,
    room        TEXT        NOT NULL DEFAULT 'Living Room',
    type        TEXT        NOT NULL,                -- lighting|plug|climate|security|entertainment|sensors
    icon        TEXT        NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (did)
);
CREATE INDEX idx_app_devices_owner ON app_devices (owner_id);

-- +goose Down
DROP TABLE IF EXISTS app_devices;
DROP TABLE IF EXISTS otp_codes;
DROP TABLE IF EXISTS app_users;
