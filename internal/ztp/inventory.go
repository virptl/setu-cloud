package ztp

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrNotFound         = errors.New("device not in inventory")
	ErrAlreadyProvisioned = errors.New("device already provisioned")
)

type inventoryEntry struct {
	MAC      string
	TID      string
	DID      string
	PID      string
	MQUser   string
	MQPass   string
	HWConfig json.RawMessage
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
	err = tx.QueryRow(ctx, `
		UPDATE device_inventory
		   SET provisioned_at = NOW()
		 WHERE mac = $1 AND provisioned_at IS NULL
		RETURNING mac, tid, did, pid, mq_user, mq_pass, hw_config
	`, mac).Scan(&e.MAC, &e.TID, &e.DID, &e.PID, &e.MQUser, &e.MQPass, &e.HWConfig)

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

	// Warm the devices table before the device connects to MQTT.
	_, err = tx.Exec(ctx, `
		INSERT INTO devices (tid, did, pid, is_online, registered_at)
		VALUES ($1, $2, $3, false, NOW())
		ON CONFLICT (tid, did) DO NOTHING
	`, e.TID, e.DID, e.PID)
	if err != nil {
		return nil, err
	}

	return &e, tx.Commit(ctx)
}
