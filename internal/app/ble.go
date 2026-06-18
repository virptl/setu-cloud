package app

import (
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/setucore/setu-cloud/internal/api/middleware"
	"github.com/setucore/setu-cloud/internal/keystore"
)

// SignBLENonce handles POST /v1/ble/sign.
// Signs device_id‖nonce‖role with the tenant's active P-256 private key.
// Output is raw r‖s (64 bytes = 128 hex chars) as expected by uECC_verify on the device.
func SignBLENonce(db *pgxpool.Pool, ks *keystore.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			DeviceID string `json:"device_id"`
			Nonce    string `json:"nonce"`
			Role     string `json:"role"`
		}
		if err := json.NewDecoder(r.Body).Decode(&b); err != nil || b.DeviceID == "" || b.Nonce == "" {
			writeErr(w, 400, "bad_request", "device_id and nonce required")
			return
		}

		if middleware.RoleFromContext(r.Context()) == "guest" {
			writeErr(w, 403, "forbidden", "guests cannot sign BLE credentials")
			return
		}

		// Role allowlist — only known roles may appear in the signed payload.
		validRoles := map[string]bool{"owner": true, "user": true}
		if b.Role == "" {
			b.Role = "owner"
		} else if !validRoles[b.Role] {
			writeErr(w, 400, "bad_request", "invalid role")
			return
		}

		// Look up the device's TID and PID — also verifies the device exists.
		// The cloud_pk burned into the device at ZTP belongs to its tenant, not the
		// app user's tenant (app JWT always carries ConsumerTID="setu").
		var deviceTID, devicePID string
		db.QueryRow(r.Context(),
			`SELECT tid, pid FROM devices WHERE did=$1`, b.DeviceID).Scan(&deviceTID, &devicePID)
		if deviceTID == "" {
			writeErr(w, 404, "not_found", "device not found")
			return
		}

		// TOFU ownership: the first authenticated non-guest user to sign for a
		// device claims it. Physical BLE proximity is the possession proof.
		uid := middleware.UIDFromContext(r.Context())
		var ownerID string
		db.QueryRow(r.Context(),
			`SELECT owner_id FROM app_devices WHERE did=$1`, b.DeviceID).Scan(&ownerID)

		if ownerID != "" && ownerID != uid {
			writeErr(w, 403, "forbidden", "device already claimed by another user")
			return
		}

		if ownerID == "" {
			// Auto-claim with placeholder metadata; user updates via /devices/adopt.
			db.Exec(r.Context(), `
				INSERT INTO app_devices (id, owner_id, tid, did, pid, name, room, type, icon)
				VALUES ($1, $2, $3, $4, $5, 'New Device', 'Living Room', $6, '')
				ON CONFLICT (did) DO NOTHING`,
				uuid.New(), uid, deviceTID, b.DeviceID, devicePID, deviceTypeForPID(devicePID))
			// Re-read to detect a simultaneous claim race.
			db.QueryRow(r.Context(),
				`SELECT owner_id FROM app_devices WHERE did=$1`, b.DeviceID).Scan(&ownerID)
			if ownerID != uid {
				writeErr(w, 403, "forbidden", "device already claimed by another user")
				return
			}
		}

		tk, err := ks.ActiveKey(r.Context(), deviceTID)
		if err != nil {
			writeErr(w, 500, "internal", "no signing key configured for tenant "+deviceTID)
			return
		}

		// message = device_id‖nonce‖role, no separators
		msg := b.DeviceID + b.Nonce + b.Role
		h := sha256.Sum256([]byte(msg))

		rr, ss, err := ecdsa.Sign(rand.Reader, tk.PrivKey, h[:])
		if err != nil {
			writeErr(w, 500, "internal", "sign failed")
			return
		}

		// Raw r‖s, each left-padded to 32 bytes big-endian.
		sig := make([]byte, 64)
		rr.FillBytes(sig[0:32])
		ss.FillBytes(sig[32:64])
		writeJSON(w, 200, map[string]string{"sig": hex.EncodeToString(sig)})
	}
}
