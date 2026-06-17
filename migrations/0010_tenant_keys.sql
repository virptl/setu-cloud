-- +goose Up

-- One row per keypair per tenant. Only one row per tenant may have active=true
-- at a time (enforced by the partial unique index below).
-- The private scalar D is stored AES-256-GCM encrypted; the KEK lives only in
-- the server environment (KEY_ENCRYPTION_KEY), never in the database.
CREATE TABLE tenant_keys (
    key_id       UUID        PRIMARY KEY,
    tid          TEXT        NOT NULL REFERENCES tenants(tid),
    pub_key_hex  TEXT        NOT NULL,       -- uncompressed X‖Y, 128 hex chars
    priv_key_enc BYTEA       NOT NULL,       -- AES-256-GCM(KEK, 32-byte D scalar)
    active       BOOLEAN     NOT NULL DEFAULT true,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    rotated_at   TIMESTAMPTZ                 -- set when superseded by a newer key
);

-- Exactly one active key per tenant at any time.
CREATE UNIQUE INDEX idx_tenant_keys_active ON tenant_keys (tid) WHERE active = true;
CREATE INDEX idx_tenant_keys_tid ON tenant_keys (tid, created_at DESC);

-- +goose Down
DROP TABLE IF EXISTS tenant_keys;
