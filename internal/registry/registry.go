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
	TID           string
	DID           string
	PID           string
	FWVersion     string
	IP            string
	RSSI          int
	IsOnline      bool
	RegisteredAt  time.Time
	LastSeenAt    *time.Time
	HWConfig      json.RawMessage
	SchemaVersion *int
	// Diagnostics — nil/zero until the device has diagnostics reporting
	// enabled and has sent at least one reg/boo with a "diag" object.
	LastResetReason *string
	CrashCount      *int
	MinFreeHeap     *int
}

type Service struct {
	db    *pgxpool.Pool
	cache *redis.Client
}

func New(db *pgxpool.Pool, cache *redis.Client) *Service {
	return &Service{db: db, cache: cache}
}

func onlineKey(tid, did string) string {
	return fmt.Sprintf("online:%s:%s", tid, did)
}

// SetOnlineCached records online state in Redis.
//
// online=true:  key is set with NO expiry. The device stays online until an
//
//	explicit offline signal (LWT, clean shutdown, $SYS disconnect event).
//	We no longer rely on TTL expiry because idle devices (no DP changes) never
//	refresh their /shd, so a TTL would falsely mark them offline.
//
// online=false: key is deleted immediately.
func (s *Service) SetOnlineCached(ctx context.Context, tid, did string, online bool) error {
	if online {
		return s.cache.Set(ctx, onlineKey(tid, did), "1", 0).Err() // 0 = no expiry
	}
	return s.cache.Del(ctx, onlineKey(tid, did)).Err()
}

// IsOnlineCached checks the Redis TTL key — O(1), no DB round-trip.
func (s *Service) IsOnlineCached(ctx context.Context, tid, did string) bool {
	n, _ := s.cache.Exists(ctx, onlineKey(tid, did)).Result()
	return n > 0
}

// Upsert inserts or updates a device record. Called on reg/boo.
// Also sets is_online=true in Postgres so the persistent record is correct.
func (s *Service) Upsert(ctx context.Context, tid, did, pid, fwVer string, rssi int) error {
	_, err := s.db.Exec(ctx, `
		INSERT INTO devices (tid, did, pid, fw_version, rssi, is_online, registered_at, last_seen_at)
		VALUES ($1, $2, $3, $4, $5, true, NOW(), NOW())
		ON CONFLICT (tid, did) DO UPDATE
		  SET fw_version   = EXCLUDED.fw_version,
		      rssi         = EXCLUDED.rssi,
		      is_online    = true,
		      last_seen_at = NOW()
	`, tid, did, pid, fwVer, rssi)
	return err
}

// SetOnline updates the is_online flag in Postgres.
// Only call this on explicit boo/reg events — not on every /shd.
func (s *Service) SetOnline(ctx context.Context, tid, did string, online bool) error {
	_, err := s.db.Exec(ctx,
		`UPDATE devices SET is_online=$1, last_seen_at=NOW() WHERE tid=$2 AND did=$3`,
		online, tid, did)
	return err
}

// UpdateDiag persists the latest reset-reason/crash-count/min-free-heap
// telemetry from a reg/boo "diag" object. Called only when that object was
// actually present — see mqtt.Router.updateDiag.
func (s *Service) UpdateDiag(ctx context.Context, tid, did string, resetReason string, crashCount, minFreeHeap int) error {
	_, err := s.db.Exec(ctx,
		`UPDATE devices SET last_reset_reason=$1, crash_count=$2, min_free_heap=$3 WHERE tid=$4 AND did=$5`,
		resetReason, crashCount, minFreeHeap, tid, did)
	return err
}

// Get returns a single device. is_online is overridden from Redis if available.
func (s *Service) Get(ctx context.Context, tid, did string) (*Device, error) {
	var d Device
	err := s.db.QueryRow(ctx, `
		SELECT tid, did, pid, COALESCE(fw_version,''), COALESCE(ip,''),
		       COALESCE(rssi,0), is_online, registered_at, last_seen_at, hw_config, schema_version,
		       last_reset_reason, crash_count, min_free_heap
		  FROM devices
		 WHERE tid=$1 AND did=$2
	`, tid, did).Scan(
		&d.TID, &d.DID, &d.PID, &d.FWVersion, &d.IP,
		&d.RSSI, &d.IsOnline, &d.RegisteredAt, &d.LastSeenAt, &d.HWConfig, &d.SchemaVersion,
		&d.LastResetReason, &d.CrashCount, &d.MinFreeHeap,
	)
	if err == pgx.ErrNoRows {
		return nil, fmt.Errorf("device %s/%s not found", tid, did)
	}
	if err != nil {
		return nil, err
	}
	// Override with fresh Redis signal — more accurate than the DB flag.
	d.IsOnline = s.IsOnlineCached(ctx, tid, did)
	return &d, nil
}

// List returns all devices for a tenant with online status from Redis.
func (s *Service) List(ctx context.Context, tid string) ([]Device, error) {
	rows, err := s.db.Query(ctx, `
		SELECT tid, did, pid, COALESCE(fw_version,''), COALESCE(ip,''),
		       COALESCE(rssi,0), is_online, registered_at, last_seen_at, hw_config, schema_version,
		       last_reset_reason, crash_count, min_free_heap
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
			&d.RSSI, &d.IsOnline, &d.RegisteredAt, &d.LastSeenAt, &d.HWConfig, &d.SchemaVersion,
			&d.LastResetReason, &d.CrashCount, &d.MinFreeHeap,
		); err != nil {
			return nil, err
		}
		// Override is_online from Redis for each device.
		d.IsOnline = s.IsOnlineCached(ctx, d.TID, d.DID)
		devices = append(devices, d)
	}
	return devices, rows.Err()
}
