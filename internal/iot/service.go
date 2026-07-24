// Package iot provides a shared device-access service used by the Alexa and
// Google Home adapters.  It mirrors the DB queries in internal/app/devices.go
// but returns plain structs rather than HTTP response DTOs.
package iot

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/setucore/setu-cloud/internal/app"
	"github.com/setucore/setu-cloud/internal/config"
	"github.com/setucore/setu-cloud/internal/mqtt"
)

// Device is a user-owned device with its current reported DPS.
type Device struct {
	DID    string
	TID    string
	PID    string
	Name   string
	Room   string
	Type   string
	Online bool
	DPS    map[string]json.RawMessage // dp_id string → raw JSON value
}

// Service provides device listing, state reads, and command dispatch for the
// voice assistant adapters.
type Service struct {
	db    *pgxpool.Pool
	cache *redis.Client
	pub   *mqtt.Publisher
	cfg   *config.Config
}

func New(db *pgxpool.Pool, cache *redis.Client, pub *mqtt.Publisher, cfg *config.Config) *Service {
	return &Service{db: db, cache: cache, pub: pub, cfg: cfg}
}

func (s *Service) DB() *pgxpool.Pool {
	return s.db
}

// ListDevicesForUser returns all devices owned by uid with their current reported DPS.
func (s *Service) ListDevicesForUser(ctx context.Context, uid string) ([]Device, error) {
	rows, err := s.db.Query(ctx, `
		SELECT ad.did, ad.pid, ad.name, ad.room, ad.type,
		       COALESCE(d.is_online, false), COALESCE(d.tid, ad.tid)
		FROM app_devices ad
		LEFT JOIN LATERAL (
		    SELECT is_online, tid FROM devices
		    WHERE did = ad.did
		    ORDER BY is_online DESC, last_seen_at DESC NULLS LAST
		    LIMIT 1
		) d ON true
		WHERE ad.owner_id = $1
		ORDER BY ad.created_at`, uid)
	if err != nil {
		return nil, fmt.Errorf("list devices: %w", err)
	}
	defer rows.Close()

	var out []Device
	for rows.Next() {
		var d Device
		if err := rows.Scan(&d.DID, &d.PID, &d.Name, &d.Room, &d.Type, &d.Online, &d.TID); err != nil {
			continue
		}
		d.DPS = s.reportedDPS(ctx, d.TID, d.DID)
		out = append(out, d)
	}
	return out, nil
}

// ListDevicesForAssistant returns devices owned by uid filtered by assistant platform support ("alexa", "google", etc.).
func (s *Service) ListDevicesForAssistant(ctx context.Context, uid, platform string) ([]Device, error) {
	devices, err := s.ListDevicesForUser(ctx, uid)
	if err != nil {
		return nil, err
	}
	var filtered []Device
	for _, d := range devices {
		if app.IsAssistantSupported(ctx, s.db, d.PID, platform) {
			filtered = append(filtered, d)
		}
	}
	return filtered, nil
}

// OwnsDevice confirms that uid owns did and returns the device's platform tid and pid.
// Returns ok=false if the device doesn't belong to this user.
func (s *Service) OwnsDevice(ctx context.Context, uid, did string) (tid, pid string, ok bool) {
	var appPID string
	if err := s.db.QueryRow(ctx,
		`SELECT pid FROM app_devices WHERE did=$1 AND owner_id=$2`, did, uid).Scan(&appPID); err != nil {
		return "", "", false
	}
	tid = s.cfg.ConsumerTID
	pid = appPID
	// Prefer the live device row (handles cross-tenant provisioning).
	s.db.QueryRow(ctx,
		`SELECT tid, pid FROM devices WHERE did=$1 ORDER BY is_online DESC, last_seen_at DESC NULLS LAST LIMIT 1`,
		did).Scan(&tid, &pid)
	return tid, pid, true
}

// OwnsDeviceForAssistant confirms that uid owns did and that the device supports the specified assistant platform.
func (s *Service) OwnsDeviceForAssistant(ctx context.Context, uid, did, platform string) (tid, pid string, ok bool) {
	tid, pid, ok = s.OwnsDevice(ctx, uid, did)
	if !ok {
		return "", "", false
	}
	if !app.IsAssistantSupported(ctx, s.db, pid, platform) {
		return "", "", false
	}
	return tid, pid, true
}

// GetReportedDPS reads the latest reported shadow DPS for a device.
func (s *Service) GetReportedDPS(ctx context.Context, tid, did string) map[string]json.RawMessage {
	return s.reportedDPS(ctx, tid, did)
}

// IsOnline returns whether the device is currently connected.
func (s *Service) IsOnline(ctx context.Context, tid, did string) bool {
	key := fmt.Sprintf("online:%s:%s", tid, did)
	n, _ := s.cache.Exists(ctx, key).Result()
	return n > 0
}

// SendCommand issues a DP set command for a device already confirmed to belong to uid.
// dps maps dp_id strings ("1", "2") to raw JSON values.
// Returns the command UUID.
func (s *Service) SendCommand(ctx context.Context, uid, did string, dps map[string]json.RawMessage) (string, error) {
	tid, pid, ok := s.OwnsDevice(ctx, uid, did)
	if !ok {
		return "", fmt.Errorf("device not found or not owned by user")
	}

	cmdID := uuid.New().String()
	payload, _ := json.Marshal(dps)

	if _, err := s.db.Exec(ctx,
		`INSERT INTO commands (id, tid, did, command_type, payload, status, issued_at)
		 VALUES ($1, $2, $3, 'set', $4, 'pending', NOW())`,
		cmdID, tid, did, payload); err != nil {
		return "", fmt.Errorf("insert command: %w", err)
	}

	if err := s.pub.Publish(tid, pid, did, "set", cmdID, payload); err != nil {
		return "", fmt.Errorf("mqtt publish: %w", err)
	}

	// Optimistic desired shadow update.
	for dpStr, val := range dps {
		if n, err := strconv.Atoi(dpStr); err == nil {
			s.db.Exec(ctx, `
				INSERT INTO shadows (tid, did, dp_id, desired_value, updated_at)
				VALUES ($1, $2, $3, $4, NOW())
				ON CONFLICT (tid, did, dp_id) DO UPDATE
				  SET desired_value = EXCLUDED.desired_value, updated_at = NOW()`,
				tid, did, n, []byte(val))
		}
	}
	return cmdID, nil
}

// reportedDPS reads reported DP values from Postgres for a device.
func (s *Service) reportedDPS(ctx context.Context, tid, did string) map[string]json.RawMessage {
	rows, err := s.db.Query(ctx,
		`SELECT dp_id, reported_value FROM shadows WHERE tid=$1 AND did=$2`, tid, did)
	if err != nil {
		return map[string]json.RawMessage{}
	}
	defer rows.Close()

	out := map[string]json.RawMessage{}
	for rows.Next() {
		var dpID int
		var raw []byte
		rows.Scan(&dpID, &raw)
		if raw != nil {
			out[strconv.Itoa(dpID)] = json.RawMessage(raw)
		}
	}
	return out
}

// UserEmail returns the email of a user by ID (used by /oauth/userinfo).
func (s *Service) UserEmail(ctx context.Context, uid string) (email, name string, err error) {
	err = s.db.QueryRow(ctx,
		`SELECT COALESCE(email,''), COALESCE(display_name,'') FROM app_users WHERE id=$1`, uid).
		Scan(&email, &name)
	return
}
