-- +goose Up

-- Opaque, hashed, single-use refresh tokens with family-based reuse detection.
-- family_id groups all rotations of a single login so that reuse of any old
-- token in the chain causes the entire family to be revoked.
CREATE TABLE refresh_tokens (
    id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    family_id   UUID        NOT NULL,
    user_id     TEXT        NOT NULL,
    token_hash  TEXT        NOT NULL UNIQUE,
    expires_at  TIMESTAMPTZ NOT NULL,
    used_at     TIMESTAMPTZ,
    revoked_at  TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_rt_token_hash ON refresh_tokens (token_hash);
CREATE INDEX idx_rt_family     ON refresh_tokens (family_id);

-- +goose Down

DROP TABLE IF EXISTS refresh_tokens;
