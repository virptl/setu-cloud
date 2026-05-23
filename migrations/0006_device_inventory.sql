-- +goose Up
CREATE TABLE device_inventory (
    mac            TEXT        PRIMARY KEY,             -- 12-char lowercase hex, no separators
    tid            TEXT        NOT NULL REFERENCES tenants(tid),
    did            TEXT        NOT NULL,
    pid            TEXT        NOT NULL,
    mq_user        TEXT        NOT NULL,
    mq_pass        TEXT        NOT NULL,               -- plaintext device token (delivery only)
    hw_config      JSONB       NOT NULL,
    registered_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    provisioned_at TIMESTAMPTZ,                        -- NULL = not yet provisioned
    UNIQUE (tid, did)
);

CREATE INDEX idx_device_inventory_tid           ON device_inventory (tid);
CREATE INDEX idx_device_inventory_unprovisioned ON device_inventory (provisioned_at)
    WHERE provisioned_at IS NULL;

-- +goose Down
DROP TABLE IF EXISTS device_inventory;
