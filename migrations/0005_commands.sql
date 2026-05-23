-- +goose Up
CREATE TABLE commands (
    id           UUID        PRIMARY KEY,
    tid          TEXT        NOT NULL,
    did          TEXT        NOT NULL,
    command_type TEXT        NOT NULL CHECK (command_type IN ('set','get','ota','cfg','rot')),
    payload      JSONB,
    status       TEXT        NOT NULL DEFAULT 'pending'
                             CHECK (status IN ('pending','acked_ok','acked_fail','timeout')),
    issued_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    acked_at     TIMESTAMPTZ,
    FOREIGN KEY (tid, did) REFERENCES devices(tid, did) ON DELETE CASCADE
);

CREATE INDEX idx_commands_device  ON commands (tid, did, issued_at DESC);
CREATE INDEX idx_commands_pending ON commands (tid, status) WHERE status = 'pending';

-- +goose Down
DROP TABLE IF EXISTS commands;
