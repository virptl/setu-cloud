-- +goose Up
-- Index on ts alone enables efficient range-delete during the daily cleanup job
-- (DELETE FROM device_events WHERE ts < NOW() - INTERVAL '90 days').
-- The existing idx_device_events_device (tid, did, ts DESC) is used for
-- per-device queries; this covers table-wide time-range scans.
CREATE INDEX idx_device_events_ts ON device_events (ts);

-- +goose Down
DROP INDEX IF EXISTS idx_device_events_ts;
