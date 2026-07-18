-- +goose Up
ALTER TABLE commands DROP CONSTRAINT commands_command_type_check;
ALTER TABLE commands ADD CONSTRAINT commands_command_type_check
    CHECK (command_type IN ('set','get','ota','cfg','rot','diag_set'));

ALTER TABLE commands ADD COLUMN ack_ec     INT;
ALTER TABLE commands ADD COLUMN ack_reason TEXT;

ALTER TABLE devices ADD COLUMN last_reset_reason TEXT;
ALTER TABLE devices ADD COLUMN crash_count        INT;
ALTER TABLE devices ADD COLUMN min_free_heap      INT;

-- +goose Down
ALTER TABLE devices DROP COLUMN IF EXISTS min_free_heap;
ALTER TABLE devices DROP COLUMN IF EXISTS crash_count;
ALTER TABLE devices DROP COLUMN IF EXISTS last_reset_reason;

ALTER TABLE commands DROP COLUMN IF EXISTS ack_reason;
ALTER TABLE commands DROP COLUMN IF EXISTS ack_ec;

ALTER TABLE commands DROP CONSTRAINT commands_command_type_check;
ALTER TABLE commands ADD CONSTRAINT commands_command_type_check
    CHECK (command_type IN ('set','get','ota','cfg','rot'));
