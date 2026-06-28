-- +goose Up
-- Released product schemas pushed from dev_portal at "Release to Production".
-- This is the cloud-side mirror of dev_portal.schema_versions and the single
-- source of truth ZTP and the app profile endpoint project from. Append-only;
-- a given (tid,pid,version) is upserted idempotently on content_hash.
CREATE TABLE released_products (
    tid          TEXT        NOT NULL,
    pid          TEXT        NOT NULL,
    version      INT         NOT NULL,
    schema_json  JSONB       NOT NULL,
    content_hash TEXT        NOT NULL,
    published_at TIMESTAMPTZ NOT NULL,
    received_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (tid, pid, version)
);

-- Profile lookups are by pid (newest version first); content_hash gives the
-- idempotency arbiter for re-pushes of identical content.
CREATE INDEX idx_released_products_pid ON released_products (pid, version DESC);
CREATE UNIQUE INDEX idx_released_products_hash ON released_products (tid, pid, content_hash);

-- +goose Down
DROP TABLE IF EXISTS released_products;
