-- +goose Up
-- Per-device LAN local-control key (see firmware components/local_control +
-- docs/PLATFORM_SPEC.md §7.6). 16-byte AES-128 key stored AES-256-GCM encrypted
-- with the server KEK; delivered to the device via the "klk" MQTT command and to
-- the owner's app via GET /v1/devices/{id}/local-key.
CREATE TABLE device_local_keys (
    tid        TEXT        NOT NULL,
    did        TEXT        NOT NULL,
    key_enc    BYTEA       NOT NULL,   -- AES-256-GCM(KEK, 16-byte local key)
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    rotated_at TIMESTAMPTZ,
    PRIMARY KEY (tid, did)
);

-- Allow the "klk" command type in the audit table.
ALTER TABLE commands DROP CONSTRAINT commands_command_type_check;
ALTER TABLE commands ADD CONSTRAINT commands_command_type_check
    CHECK (command_type IN ('set','get','ota','cfg','rot','diag_set','klk'));

-- +goose Down
ALTER TABLE commands DROP CONSTRAINT commands_command_type_check;
ALTER TABLE commands ADD CONSTRAINT commands_command_type_check
    CHECK (command_type IN ('set','get','ota','cfg','rot','diag_set'));

DROP TABLE IF EXISTS device_local_keys;
