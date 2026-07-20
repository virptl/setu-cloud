// Package localkey manages per-device LAN local-control keys.
//
// Each device gets a random 16-byte AES-128 key, stored AES-256-GCM encrypted
// (with the server KEK, reusing keystore's helpers) in device_local_keys. The
// cloud pushes it to the device with the MQTT "klk" command and hands the same
// key to the owner's app, so both sides derive the identical LAN session
// (see the firmware's components/local_control and docs/PLATFORM_SPEC.md §7.6).
package localkey

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/setucore/setu-cloud/internal/keystore"
	"github.com/setucore/setu-cloud/internal/mqtt"
)

// KeyLen is the raw local-key length in bytes (AES-128).
const KeyLen = 16

// Wire constants the app needs to build the LAN session; these mirror the
// firmware (components/local_control + docs/PLATFORM_SPEC.md §7.6).
const (
	TCPPort    = 6053
	BeaconPort = 6054
	HKDFSalt   = "setu-lan-v1"
)

// Service is safe for concurrent use.
type Service struct {
	db  *pgxpool.Pool
	kek []byte
	pub *mqtt.Publisher
}

func New(db *pgxpool.Pool, kek []byte, pub *mqtt.Publisher) (*Service, error) {
	if len(kek) != 32 {
		return nil, errors.New("localkey: KEK must be exactly 32 bytes")
	}
	return &Service{db: db, kek: kek, pub: pub}, nil
}

// getOrCreateRaw returns the device's 16-byte key, generating and persisting one
// on first use. Concurrent first-use is resolved by INSERT ... ON CONFLICT.
func (s *Service) getOrCreateRaw(ctx context.Context, tid, did string) ([]byte, error) {
	var enc []byte
	err := s.db.QueryRow(ctx,
		`SELECT key_enc FROM device_local_keys WHERE tid=$1 AND did=$2`, tid, did).Scan(&enc)
	if err == nil {
		return keystore.OpenGCM(s.kek, enc)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}

	raw := make([]byte, KeyLen)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	sealed, err := keystore.SealGCM(s.kek, raw)
	if err != nil {
		return nil, err
	}
	if _, err := s.db.Exec(ctx,
		`INSERT INTO device_local_keys (tid, did, key_enc) VALUES ($1,$2,$3)
		 ON CONFLICT (tid, did) DO NOTHING`, tid, did, sealed); err != nil {
		return nil, err
	}
	// Re-read so a concurrent insert that won the race is honoured.
	if err := s.db.QueryRow(ctx,
		`SELECT key_enc FROM device_local_keys WHERE tid=$1 AND did=$2`, tid, did).Scan(&enc); err != nil {
		return nil, err
	}
	return keystore.OpenGCM(s.kek, enc)
}

// GetHex returns the device's local key as 32 lowercase hex chars, creating it
// if it does not exist yet.
func (s *Service) GetHex(ctx context.Context, tid, did string) (string, error) {
	raw, err := s.getOrCreateRaw(ctx, tid, did)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

// Provision ensures the key exists and pushes it to the device via the "klk"
// MQTT command, recording it in the commands audit table. Safe to call again
// (e.g. after a device is reflashed and its NVS wiped). The device must have a
// devices row (i.e. have connected at least once) for the audit insert to
// succeed.
func (s *Service) Provision(ctx context.Context, tid, pid, did string) error {
	keyHex, err := s.GetHex(ctx, tid, did)
	if err != nil {
		return fmt.Errorf("localkey: get key: %w", err)
	}
	d, _ := json.Marshal(map[string]string{"key": keyHex})

	cmdID := uuid.New().String()
	if _, err := s.db.Exec(ctx,
		`INSERT INTO commands (id, tid, did, command_type, payload, status, issued_at)
		 VALUES ($1,$2,$3,'klk',$4,'pending',NOW())`,
		cmdID, tid, did, d); err != nil {
		return fmt.Errorf("localkey: record command: %w", err)
	}
	if err := s.pub.Publish(tid, pid, did, "klk", cmdID, d); err != nil {
		return fmt.Errorf("localkey: publish klk: %w", err)
	}
	return nil
}
