-- +goose Up
ALTER TABLE device_inventory ADD COLUMN ble_mac TEXT UNIQUE;

CREATE INDEX idx_device_inventory_ble_mac ON device_inventory (ble_mac)
    WHERE ble_mac IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS idx_device_inventory_ble_mac;
ALTER TABLE device_inventory DROP COLUMN IF EXISTS ble_mac;
