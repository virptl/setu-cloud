-- +goose Up
CREATE TABLE device_events (
    id         BIGSERIAL   PRIMARY KEY,
    tid        TEXT        NOT NULL,
    did        TEXT        NOT NULL,
    event_type TEXT        NOT NULL CHECK (event_type IN ('reg','boo','rpt','ack','ota_done','ota_err')),
    payload    JSONB,
    ts         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    FOREIGN KEY (tid, did) REFERENCES devices(tid, did) ON DELETE CASCADE
);

CREATE INDEX idx_device_events_device ON device_events (tid, did, ts DESC);

-- +goose Down
DROP TABLE IF EXISTS device_events;
