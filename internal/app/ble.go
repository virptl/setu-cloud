package app

import (
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/setucore/setu-cloud/internal/api/middleware"
	"github.com/setucore/setu-cloud/internal/keystore"
)

var macNormRe = regexp.MustCompile(`^[0-9a-fA-F]{2}[:\-]?[0-9a-fA-F]{2}[:\-]?[0-9a-fA-F]{2}[:\-]?[0-9a-fA-F]{2}[:\-]?[0-9a-fA-F]{2}[:\-]?[0-9a-fA-F]{2}$`)

// normMAC strips separators and lowercases a MAC string, returning "" if not a MAC.
func normMAC(s string) string {
	if !macNormRe.MatchString(s) {
		return ""
	}
	return strings.ToLower(strings.NewReplacer(":", "", "-", "").Replace(s))
}

// resolveDeviceID returns (did, tid, pid) for whatever the app sends as device_id:
// - any MAC format (Wi-Fi or BLE) → look up via device_inventory
// - full or truncated UUID DID     → prefix-match in devices table
func resolveDeviceID(r *http.Request, db *pgxpool.Pool, raw string) (did, tid, pid string) {
	// MAC path: normalise and look up in device_inventory by wifi_mac or ble_mac.
	if mac := normMAC(raw); mac != "" {
		db.QueryRow(r.Context(), `
			SELECT di.did, d.tid, d.pid
			FROM device_inventory di
			JOIN devices d ON d.did = di.did
			WHERE (di.mac = $1 OR di.ble_mac = $1) AND di.provisioned_at IS NOT NULL
			LIMIT 1
		`, mac).Scan(&did, &tid, &pid)
		return
	}
	// DID path: accept full UUID or firmware-truncated prefix (23 chars).
	db.QueryRow(r.Context(), `
		SELECT did, tid, pid FROM devices
		WHERE LEFT(did, char_length($1)) = $1
		LIMIT 1
	`, raw).Scan(&did, &tid, &pid)
	return
}

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

		// Resolve device_id to a canonical DID — accepts any MAC format (Wi-Fi or
		// BLE, any separator/case) or a full/truncated UUID DID.
		// resolvedDID is used for all DB lookups; b.DeviceID is kept as-received
		// so the signing message matches what the firmware actually stored in NVS
		// (firmware truncates the UUID to 23 chars due to a 24-byte NVS buffer).
		resolvedDID, deviceTID, devicePID := resolveDeviceID(r, db, b.DeviceID)
		if deviceTID == "" {
			writeErr(w, 404, "not_found", "device not found")
			return
		}

		// TOFU ownership: the first authenticated non-guest user to sign for a
		// device claims it. Physical BLE proximity is the possession proof.
		uid := middleware.UIDFromContext(r.Context())
		var ownerID string
		db.QueryRow(r.Context(),
			`SELECT owner_id FROM app_devices WHERE did=$1`, resolvedDID).Scan(&ownerID)

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
				uuid.New(), uid, deviceTID, resolvedDID, devicePID, deviceTypeForPID(devicePID))
			// Re-read to detect a simultaneous claim race.
			db.QueryRow(r.Context(),
				`SELECT owner_id FROM app_devices WHERE did=$1`, resolvedDID).Scan(&ownerID)
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
