-- +goose Up
-- MQTT credentials are now per-tenant (tenants.mq_user / tenants.mq_pass),
-- shared by every device under a TID. The device authenticates with
-- username = tid and is identified at runtime by its clientid = did. Drop the
-- per-device credential columns from inventory.
ALTER TABLE device_inventory DROP COLUMN IF EXISTS mq_user;
ALTER TABLE device_inventory DROP COLUMN IF EXISTS mq_pass;

-- +goose Down
ALTER TABLE device_inventory ADD COLUMN mq_user TEXT NOT NULL DEFAULT '';
ALTER TABLE device_inventory ADD COLUMN mq_pass TEXT NOT NULL DEFAULT '';
