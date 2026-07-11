-- +goose Up
-- Soft-retire flag for a released product. Set when the dev portal deletes a
-- released product (so the cloud record is archived, not destroyed — devices in
-- the field keep working), and toggleable by staff from the admin portal.
-- NULL = active, non-NULL = retired.
ALTER TABLE released_products ADD COLUMN retired_at TIMESTAMPTZ;

-- +goose Down
ALTER TABLE released_products DROP COLUMN IF EXISTS retired_at;
