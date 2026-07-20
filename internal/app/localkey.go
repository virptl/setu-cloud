package app

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/setucore/setu-cloud/internal/api/middleware"
	"github.com/setucore/setu-cloud/internal/config"
	"github.com/setucore/setu-cloud/internal/localkey"
)

// ownedDeviceRoute resolves the device named in the {id} URL param, verifying it
// belongs to the authenticated user, and returns the device's real tid/pid (as
// used for MQTT topics). ok is false when the device is missing or not theirs.
func ownedDeviceRoute(r *http.Request, db *pgxpool.Pool, cfg *config.Config) (did, tid, pid string, ok bool) {
	uid := middleware.UIDFromContext(r.Context())
	did = chi.URLParam(r, "id")
	if err := db.QueryRow(r.Context(),
		`SELECT pid FROM app_devices WHERE did=$1 AND owner_id=$2`, did, uid).Scan(&pid); err != nil {
		return "", "", "", false
	}
	tid = cfg.ConsumerTID
	var rTID, rPID string
	db.QueryRow(r.Context(),
		`SELECT tid, pid FROM devices WHERE did=$1 ORDER BY is_online DESC, last_seen_at DESC NULLS LAST LIMIT 1`,
		did).Scan(&rTID, &rPID)
	if rTID != "" {
		tid = rTID
	}
	if rPID != "" {
		pid = rPID
	}
	return did, tid, pid, true
}

// GetLocalKey handles GET /v1/devices/{id}/local-key — returns the LAN control
// key plus the connection parameters the app needs to drive the device on-LAN
// (see docs/PLATFORM_SPEC.md §7.6). Owner-gated.
func GetLocalKey(db *pgxpool.Pool, cfg *config.Config, lk *localkey.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		did, tid, _, ok := ownedDeviceRoute(r, db, cfg)
		if !ok {
			writeErr(w, 404, "device_not_found", "not yours or missing")
			return
		}
		keyHex, err := lk.GetHex(r.Context(), tid, did)
		if err != nil {
			writeErr(w, 500, "internal_error", err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{
			"key":         keyHex,
			"tcp_port":    localkey.TCPPort,
			"beacon_port": localkey.BeaconPort,
			"hkdf_salt":   localkey.HKDFSalt,
		})
	}
}

// ProvisionLocalKey handles POST /v1/devices/{id}/local-key/provision —
// (re)sends the "klk" command so the device stores its LAN key in NVS. Use after
// a reflash / NVS wipe, or if the automatic push on adopt was missed. Owner-gated.
func ProvisionLocalKey(db *pgxpool.Pool, cfg *config.Config, lk *localkey.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		did, tid, pid, ok := ownedDeviceRoute(r, db, cfg)
		if !ok {
			writeErr(w, 404, "device_not_found", "not yours or missing")
			return
		}
		if err := lk.Provision(r.Context(), tid, pid, did); err != nil {
			writeErr(w, 500, "provision_failed", err.Error())
			return
		}
		writeJSON(w, 202, map[string]any{"status": "sent"})
	}
}
