-- +goose Up
-- Production batches: a manufacturing run of devices seeded for a released
-- (tid, pid, schema_version).
CREATE TABLE batches (
    id              UUID        PRIMARY KEY,
    tid             TEXT        NOT NULL REFERENCES tenants(tid),
    pid             TEXT        NOT NULL,
    schema_version  INT         NOT NULL,
    qty             INT         NOT NULL,
    integrator_note TEXT        NOT NULL DEFAULT '',
    created_by      TEXT        NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_batches_tid_pid ON batches (tid, pid);

-- Extend device_inventory into the full inventory lifecycle. A device may be
-- reserved by qty before its MAC is known, so MAC becomes nullable and the
-- primary key moves to (tid, did).
ALTER TABLE device_inventory ADD COLUMN schema_version INT;
ALTER TABLE device_inventory ADD COLUMN status         TEXT NOT NULL DEFAULT 'inventory'; -- inventory|provisioned|activated|claimed|retired
ALTER TABLE device_inventory ADD COLUMN batch_id       UUID REFERENCES batches(id);

ALTER TABLE device_inventory DROP CONSTRAINT device_inventory_pkey;        -- was (mac)
ALTER TABLE device_inventory ALTER COLUMN mac DROP NOT NULL;
-- Plain UNIQUE keeps ON CONFLICT (mac) working and allows many NULLs (reserved
-- devices awaiting a MAC), since SQL treats NULLs as distinct.
ALTER TABLE device_inventory ADD CONSTRAINT device_inventory_mac_key UNIQUE (mac);
ALTER TABLE device_inventory DROP CONSTRAINT device_inventory_tid_did_key;
ALTER TABLE device_inventory ADD PRIMARY KEY (tid, did);

-- Backfill status for rows that predate the lifecycle column.
UPDATE device_inventory
   SET status = CASE WHEN provisioned_at IS NOT NULL THEN 'provisioned' ELSE 'inventory' END;

CREATE INDEX idx_device_inventory_status ON device_inventory (tid, pid, status);
CREATE INDEX idx_device_inventory_batch  ON device_inventory (batch_id);

-- Bind each device record to the schema it was manufactured against.
ALTER TABLE devices ADD COLUMN schema_version INT;

-- +goose Down
ALTER TABLE devices DROP COLUMN IF EXISTS schema_version;

DROP INDEX IF EXISTS idx_device_inventory_batch;
DROP INDEX IF EXISTS idx_device_inventory_status;
ALTER TABLE device_inventory DROP CONSTRAINT device_inventory_pkey;        -- (tid,did)
ALTER TABLE device_inventory ADD CONSTRAINT device_inventory_tid_did_key UNIQUE (tid, did);
ALTER TABLE device_inventory DROP CONSTRAINT device_inventory_mac_key;
DELETE FROM device_inventory WHERE mac IS NULL;
ALTER TABLE device_inventory ALTER COLUMN mac SET NOT NULL;
ALTER TABLE device_inventory ADD PRIMARY KEY (mac);
ALTER TABLE device_inventory DROP COLUMN IF EXISTS batch_id;
ALTER TABLE device_inventory DROP COLUMN IF EXISTS status;
ALTER TABLE device_inventory DROP COLUMN IF EXISTS schema_version;

DROP TABLE IF EXISTS batches;
