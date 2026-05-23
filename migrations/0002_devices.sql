-- +goose Up
CREATE TABLE devices (
    tid           TEXT        NOT NULL REFERENCES tenants(tid),
    did           TEXT        NOT NULL,
    pid           TEXT        NOT NULL,
    fw_version    TEXT,
    ip            TEXT,
    rssi          INT,
    is_online     BOOLEAN     NOT NULL DEFAULT false,
    registered_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_seen_at  TIMESTAMPTZ,
    hw_config     JSONB,
    PRIMARY KEY (tid, did)
);

CREATE INDEX idx_devices_tid       ON devices (tid);
CREATE INDEX idx_devices_is_online ON devices (tid, is_online);

-- +goose Down
DROP TABLE IF EXISTS devices;
