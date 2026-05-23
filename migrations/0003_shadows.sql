-- +goose Up
CREATE TABLE shadows (
    tid            TEXT        NOT NULL REFERENCES tenants(tid),
    did            TEXT        NOT NULL,
    dp_id          INT         NOT NULL,
    desired_value  JSONB,
    reported_value JSONB,
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (tid, did, dp_id),
    FOREIGN KEY (tid, did) REFERENCES devices(tid, did) ON DELETE CASCADE
);

CREATE INDEX idx_shadows_device ON shadows (tid, did);

-- +goose Down
DROP TABLE IF EXISTS shadows;
