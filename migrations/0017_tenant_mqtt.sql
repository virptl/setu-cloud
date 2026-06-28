-- +goose Up
-- A single shared MQTT credential per tenant (TID). Inventory seeding assigns the
-- same mq_user/mq_pass to every device under a tenant, rather than minting a
-- per-device pair.
ALTER TABLE tenants ADD COLUMN mq_user TEXT;
ALTER TABLE tenants ADD COLUMN mq_pass TEXT;

-- +goose Down
ALTER TABLE tenants DROP COLUMN IF EXISTS mq_pass;
ALTER TABLE tenants DROP COLUMN IF EXISTS mq_user;
