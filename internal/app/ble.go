package app

import (
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"

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
		if b.Role == "" {
			b.Role = "owner"
		}

		// Refuse to sign for a device already claimed by a different user.
		uid := middleware.UIDFromContext(r.Context())
		var ownerID string
		db.QueryRow(r.Context(),
			`SELECT owner_id FROM app_devices WHERE did=$1`, b.DeviceID).Scan(&ownerID)
		if ownerID != "" && ownerID != uid {
			writeErr(w, 403, "forbidden", "device already claimed by another user")
			return
		}

		// Look up the device's own TID — the cloud_pk burned into the device
		// at ZTP time belongs to the device's tenant, not the app user's tenant.
		// (App JWT always carries ConsumerTID="setu" regardless of device TID.)
		var deviceTID string
		db.QueryRow(r.Context(),
			`SELECT tid FROM devices WHERE did=$1`, b.DeviceID).Scan(&deviceTID)
		if deviceTID == "" {
			// Device not in devices table yet (pre-ZTP claim). Fall back to user's TID.
			deviceTID = middleware.TIDFromContext(r.Context())
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
