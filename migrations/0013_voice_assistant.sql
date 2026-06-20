-- +goose Up

-- OAuth2 clients registered per voice assistant skill/action.
CREATE TABLE oauth_clients (
    client_id     TEXT PRIMARY KEY,
    client_secret TEXT     NOT NULL,   -- bcrypt hash
    redirect_uris TEXT[]   NOT NULL,
    name          TEXT     NOT NULL,   -- 'Alexa' | 'Google Home'
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Short-lived authorization codes (10-min TTL, single-use).
CREATE TABLE oauth_auth_codes (
    code         TEXT PRIMARY KEY,
    client_id    TEXT        NOT NULL REFERENCES oauth_clients(client_id),
    user_id      UUID        NOT NULL REFERENCES app_users(id) ON DELETE CASCADE,
    redirect_uri TEXT        NOT NULL,
    scope        TEXT        NOT NULL DEFAULT 'devices:read devices:control',
    expires_at   TIMESTAMPTZ NOT NULL,
    used_at      TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Long-lived OAuth access + refresh token pairs.
-- Tokens are stored as SHA-256 hashes (mirrors refresh_tokens pattern).
CREATE TABLE oauth_tokens (
    id                   UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    client_id            TEXT        NOT NULL REFERENCES oauth_clients(client_id),
    user_id              UUID        NOT NULL REFERENCES app_users(id) ON DELETE CASCADE,
    access_token_hash    TEXT        NOT NULL UNIQUE,
    refresh_token_hash   TEXT        NOT NULL UNIQUE,
    scope                TEXT        NOT NULL,
    expires_at           TIMESTAMPTZ NOT NULL,          -- access token expiry (1 h)
    refresh_expires_at   TIMESTAMPTZ NOT NULL,          -- refresh expiry (30 d)
    revoked_at           TIMESTAMPTZ,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_oauth_tokens_user     ON oauth_tokens (user_id, client_id);
CREATE INDEX idx_oauth_tokens_at_hash  ON oauth_tokens (access_token_hash);
CREATE INDEX idx_oauth_tokens_rt_hash  ON oauth_tokens (refresh_token_hash);

-- Tracks which voice platforms a user has linked.
-- alexa_bearer_token: latest token from a directive scope; used for ChangeReport push.
CREATE TABLE linked_accounts (
    user_id              UUID        NOT NULL REFERENCES app_users(id) ON DELETE CASCADE,
    platform             TEXT        NOT NULL,   -- 'alexa' | 'google'
    platform_user_id     TEXT,
    alexa_bearer_token   TEXT,                   -- refreshed on each Alexa directive
    linked_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    unlinked_at          TIMESTAMPTZ,
    PRIMARY KEY (user_id, platform)
);

-- +goose Down

DROP TABLE IF EXISTS linked_accounts;
DROP TABLE IF EXISTS oauth_tokens;
DROP TABLE IF EXISTS oauth_auth_codes;
DROP TABLE IF EXISTS oauth_clients;
