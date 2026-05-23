package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

// Device is the full device record from PostgreSQL.
type Device struct {
	TID          string
	DID          string
	PID          string
	FWVersion    string
	IP           string
	RSSI         int
	IsOnline     bool
	RegisteredAt time.Time
	LastSeenAt   *time.Time
	HWConfig     json.RawMessage
}

type Service struct {
	db    *pgxpool.Pool
	cache *redis.Client
}

func New(db *pgxpool.Pool, cache *redis.Client) *Service {
	return &Service{db: db, cache: cache}
}

// Upsert inserts or updates a device record. Called on reg/boo.
func (s *Service) Upsert(ctx context.Context, tid, did, pid, fwVer string, rssi int) error {
	_, err := s.db.Exec(ctx, `
		INSERT INTO devices (tid, did, pid, fw_version, rssi, is_online, registered_at, last_seen_at)
		VALUES ($1, $2, $3, $4, $5, true, NOW(), NOW())
		ON CONFLICT (tid, did) DO UPDATE
		  SET fw_version  = EXCLUDED.fw_version,
		      rssi        = EXCLUDED.rssi,
		      is_online   = true,
		      last_seen_at = NOW()
	`, tid, did, pid, fwVer, rssi)
	return err
}

// SetOnline updates the is_online flag and last_seen_at timestamp.
func (s *Service) SetOnline(ctx context.Context, tid, did string, online bool) error {
	_, err := s.db.Exec(ctx, `
		UPDATE devices SET is_online=$1, last_seen_at=NOW() WHERE tid=$2 AND did=$3
	`, online, tid, did)
	return err
}

// Get returns a single device. Returns pgx.ErrNoRows if not found.
func (s *Service) Get(ctx context.Context, tid, did string) (*Device, error) {
	var d Device
	err := s.db.QueryRow(ctx, `
		SELECT tid, did, pid, COALESCE(fw_version,''), COALESCE(ip,''),
		       COALESCE(rssi,0), is_online, registered_at, last_seen_at, hw_config
		  FROM devices
		 WHERE tid=$1 AND did=$2
	`, tid, did).Scan(
		&d.TID, &d.DID, &d.PID, &d.FWVersion, &d.IP,
		&d.RSSI, &d.IsOnline, &d.RegisteredAt, &d.LastSeenAt, &d.HWConfig,
	)
	if err == pgx.ErrNoRows {
		return nil, fmt.Errorf("device %s/%s not found", tid, did)
	}
	return &d, err
}

// List returns all devices for a tenant.
func (s *Service) List(ctx context.Context, tid string) ([]Device, error) {
	rows, err := s.db.Query(ctx, `
		SELECT tid, did, pid, COALESCE(fw_version,''), COALESCE(ip,''),
		       COALESCE(rssi,0), is_online, registered_at, last_seen_at, hw_config
		  FROM devices
		 WHERE tid=$1
		 ORDER BY registered_at DESC
	`, tid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var devices []Device
	for rows.Next() {
		var d Device
		if err := rows.Scan(
			&d.TID, &d.DID, &d.PID, &d.FWVersion, &d.IP,
			&d.RSSI, &d.IsOnline, &d.RegisteredAt, &d.LastSeenAt, &d.HWConfig,
		); err != nil {
			return nil, err
		}
		devices = append(devices, d)
	}
	return devices, rows.Err()
}
