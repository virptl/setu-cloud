package ztp

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/setucore/setu-cloud/internal/schema"
)

var (
	ErrNotFound           = errors.New("device not in inventory")
	ErrAlreadyProvisioned = errors.New("device already provisioned")
)

type inventoryEntry struct {
	MAC           string
	TID           string
	DID           string
	PID           string
	MQUser        string
	MQPass        string
	HWConfig      json.RawMessage
	SchemaVersion *int
}

// claimDevice atomically marks the device as provisioned and upserts it into devices.
// Returns ErrNotFound or ErrAlreadyProvisioned on the expected failure cases.
func claimDevice(ctx context.Context, db *pgxpool.Pool, mac string) (*inventoryEntry, error) {
	tx, err := db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	var e inventoryEntry
	// MQTT credentials are shared per tenant — join tenants for mq_user/mq_pass.
	// The device is identified at runtime by its clientid = did, not the username.
	err = tx.QueryRow(ctx, `
		UPDATE device_inventory di
		   SET provisioned_at = NOW(),
		       status = 'provisioned'
		  FROM tenants t
		 WHERE di.mac = $1 AND di.provisioned_at IS NULL AND t.tid = di.tid
		RETURNING di.mac, di.tid, di.did, di.pid,
		          COALESCE(t.mq_user, ''), COALESCE(t.mq_pass, ''),
		          di.hw_config, di.schema_version
	`, mac).Scan(&e.MAC, &e.TID, &e.DID, &e.PID, &e.MQUser, &e.MQPass, &e.HWConfig, &e.SchemaVersion)

	if errors.Is(err, pgx.ErrNoRows) {
		// Distinguish "never registered" from "already provisioned".
		var exists bool
		db.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM device_inventory WHERE mac=$1)`, mac,
		).Scan(&exists)
		if exists {
			return nil, ErrAlreadyProvisioned
		}
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	// Warm the devices table before the device connects to MQTT, binding the
	// schema version the device was manufactured against.
	_, err = tx.Exec(ctx, `
		INSERT INTO devices (tid, did, pid, schema_version, is_online, registered_at)
		VALUES ($1, $2, $3, $4, false, NOW())
		ON CONFLICT (tid, did) DO UPDATE
		  SET pid = EXCLUDED.pid, schema_version = EXCLUDED.schema_version
	`, e.TID, e.DID, e.PID, e.SchemaVersion)
	if err != nil {
		return nil, err
	}

	return &e, tx.Commit(ctx)
}

// projectFirmwareConfig loads the released schema for (tid, pid, version) and
// projects it to the firmware hw_config the device parses.
func projectFirmwareConfig(ctx context.Context, db *pgxpool.Pool, tid, pid string, version int) (json.RawMessage, error) {
	var raw []byte
	err := db.QueryRow(ctx,
		`SELECT schema_json FROM released_products WHERE tid=$1 AND pid=$2 AND version=$3`,
		tid, pid, version).Scan(&raw)
	if err != nil {
		return nil, err
	}
	art, err := schema.Parse(raw)
	if err != nil {
		return nil, err
	}
	return json.Marshal(art.FirmwareConfig())
}
