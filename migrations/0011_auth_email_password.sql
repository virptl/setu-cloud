-- +goose Up

ALTER TABLE app_users
    ADD COLUMN password_hash         TEXT,
    ADD COLUMN auth_method           TEXT        NOT NULL DEFAULT 'otp',
    ADD COLUMN failed_login_attempts INT         NOT NULL DEFAULT 0,
    ADD COLUMN locked_until          TIMESTAMPTZ;

-- Single-use tokens that prove an email was verified via OTP.
-- Consumed by /auth/register to gate account creation.
CREATE TABLE verification_tokens (
    id          UUID        PRIMARY KEY,
    email       TEXT        NOT NULL,
    token       TEXT        NOT NULL UNIQUE,
    expires_at  TIMESTAMPTZ NOT NULL,
    consumed_at TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_vt_token ON verification_tokens (token);

-- +goose Down

DROP TABLE IF EXISTS verification_tokens;
ALTER TABLE app_users
    DROP COLUMN IF EXISTS locked_until,
    DROP COLUMN IF EXISTS failed_login_attempts,
    DROP COLUMN IF EXISTS auth_method,
    DROP COLUMN IF EXISTS password_hash;
