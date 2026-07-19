package app

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/setucore/setu-cloud/internal/api/middleware"
	"github.com/setucore/setu-cloud/internal/config"
	"github.com/setucore/setu-cloud/internal/localkey"
	"github.com/setucore/setu-cloud/internal/mqtt"
	"github.com/setucore/setu-cloud/internal/registry"
)

// DiscoveryRefresher is implemented by proactive.Service and called after a device
// is claimed or adopted so voice assistants re-discover the new device.
type DiscoveryRefresher interface {
	TriggerDiscoveryRefresh(userID string)
}

type deviceDTO struct {
	ID           string         `json:"id"`
	DID          string         `json:"did"`
	PID          string         `json:"pid"`
	Name         string         `json:"name"`
	Room         string         `json:"room"`
	Type         string         `json:"type"`
	Icon         string         `json:"icon"`
	On           bool           `json:"on"`
	Offline      bool           `json:"offline"`
	Metric       string         `json:"metric"`
	DPS          map[string]any `json:"dps"`
	Capabilities []Capability   `json:"capabilities"`
}

// ListDevices handles GET /v1/devices.
func ListDevices(db *pgxpool.Pool, reg *registry.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uid := middleware.UIDFromContext(r.Context())
		// Join on did only — the device's MQTT tid (from firmware factory config)
		// may differ from app_devices.tid, so we prefer the most recently seen row.
		rows, err := db.Query(r.Context(), `
			SELECT ad.did, ad.pid, ad.name, ad.room, ad.type, ad.icon,
			       COALESCE(d.is_online, false), COALESCE(d.tid, ad.tid), COALESCE(d.schema_version, 0)
			FROM app_devices ad
			LEFT JOIN LATERAL (
			    SELECT is_online, tid, schema_version FROM devices
			    WHERE did = ad.did
			    ORDER BY is_online DESC, last_seen_at DESC NULLS LAST
			    LIMIT 1
			) d ON true
			WHERE ad.owner_id = $1
			ORDER BY ad.created_at`, uid)
		if err != nil {
			writeErr(w, 500, "internal", err.Error())
			return
		}
		defer rows.Close()

		out := []deviceDTO{}
		for rows.Next() {
			var did, pid, name, room, typ, icon, deviceTID string
			var online bool
			var schemaVersion int
			rows.Scan(&did, &pid, &name, &room, &typ, &icon, &online, &deviceTID, &schemaVersion)
			// The devices.is_online column only updates on explicit boo/reg events;
			// $SYS connect events and /shd online:bool only touch the Redis cache
			// (see internal/registry). Redis is the fresher signal, so it wins here
			// the same way registry.Get/List override the DB value.
			online = reg.IsOnlineCached(r.Context(), deviceTID, did)
			dps := reportedDPS(r.Context(), db, deviceTID, did)
			on, _ := dps["1"].(bool)
			out = append(out, deviceDTO{
				ID: did, DID: did, PID: pid, Name: name, Room: room, Type: typ, Icon: icon,
				On: on, Offline: !online, Metric: metricFor(typ, on, dps),
				DPS: dps, Capabilities: ResolveCapabilities(r.Context(), db, pid, schemaVersion),
			})
		}
		writeJSON(w, 200, out)
	}
}

// ClaimDevice handles POST /v1/devices/claim.
func ClaimDevice(db *pgxpool.Pool, cfg *config.Config, refresher ...DiscoveryRefresher) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uid := middleware.UIDFromContext(r.Context())
		var b struct {
			Name string `json:"name"`
			Room string `json:"room"`
			Type string `json:"type"`
			Icon string `json:"icon"`
			Did  string `json:"did"`
		}
		if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
			writeErr(w, 400, "bad_request", "invalid json")
			return
		}
		if b.Type == "" || b.Name == "" {
			writeErr(w, 400, "bad_request", "name and type required")
			return
		}
		prof := profileForType(b.Type)
		did := b.Did
		if did == "" {
			did = "mock" + uuid.New().String()[:8]
		}
		if b.Room == "" {
			b.Room = "Living Room"
		}

		// For real provisioned devices the did already exists — use its pid.
		pid := prof.PID
		var existingPID string
		db.QueryRow(r.Context(),
			`SELECT pid FROM devices WHERE tid=$1 AND did=$2`, cfg.ConsumerTID, did).Scan(&existingPID)
		if existingPID != "" {
			pid = existingPID
		}

		// Platform device row (idempotent — real devices already have this row from ZTP).
		if _, err := db.Exec(r.Context(),
			`INSERT INTO devices (tid, did, pid, is_online) VALUES ($1, $2, $3, false)
			 ON CONFLICT (tid, did) DO NOTHING`,
			cfg.ConsumerTID, did, pid); err != nil {
			writeErr(w, 500, "internal", err.Error())
			return
		}
		// App device row owned by this user.
		if _, err := db.Exec(r.Context(),
			`INSERT INTO app_devices (id, owner_id, tid, did, pid, name, room, type, icon)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
			uuid.New(), uid, cfg.ConsumerTID, did, pid, b.Name, b.Room, b.Type, b.Icon); err != nil {
			writeErr(w, 409, "conflict", "device already claimed")
			return
		}
		if len(refresher) > 0 && refresher[0] != nil {
			go refresher[0].TriggerDiscoveryRefresh(uid)
		}
		writeJSON(w, 201, deviceDTO{
			ID: did, DID: did, PID: pid, Name: b.Name, Room: b.Room, Type: b.Type, Icon: b.Icon,
			On: false, Offline: true, Metric: "Off",
			DPS:          map[string]any{"1": false},
			Capabilities: prof.Caps,
		})
	}
}

// Command handles POST /v1/devices/{id}/command.
func Command(db *pgxpool.Pool, pub *mqtt.Publisher, cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uid := middleware.UIDFromContext(r.Context())
		did := chi.URLParam(r, "id")
		var b struct {
			DPS map[string]json.RawMessage `json:"dps"`
		}
		if err := json.NewDecoder(r.Body).Decode(&b); err != nil || len(b.DPS) == 0 {
			writeErr(w, 400, "bad_request", "dps required")
			return
		}
		var pid string
		if err := db.QueryRow(r.Context(),
			`SELECT pid FROM app_devices WHERE did=$1 AND owner_id=$2`, did, uid).Scan(&pid); err != nil {
			writeErr(w, 404, "device_not_found", "not yours or missing")
			return
		}
		// Use the device's actual tid and pid from the devices table (may differ from app_devices).
		deviceTID := cfg.ConsumerTID
		var realTID, realPID string
		db.QueryRow(r.Context(),
			`SELECT tid, pid FROM devices WHERE did=$1 ORDER BY is_online DESC, last_seen_at DESC NULLS LAST LIMIT 1`,
			did).Scan(&realTID, &realPID)
		if realTID != "" {
			deviceTID = realTID
		}
		if realPID != "" {
			pid = realPID
		}
		// Validate color dp values: must be {r,g,b} with each channel 0–255.
		if dpKinds := dpKindsForPID(pid); dpKinds != nil {
			for dp, val := range b.DPS {
				if dpKinds[dp] == "color" {
					var rgb struct {
						R int `json:"r"`
						G int `json:"g"`
						B int `json:"b"`
					}
					if err := json.Unmarshal(val, &rgb); err != nil ||
						rgb.R < 0 || rgb.R > 255 || rgb.G < 0 || rgb.G > 255 || rgb.B < 0 || rgb.B > 255 {
						writeErr(w, 400, "bad_request", "color dp requires {\"r\":0-255,\"g\":0-255,\"b\":0-255}")
						return
					}
				}
			}
		}

		cmdID := uuid.New().String()
		payload, _ := json.Marshal(b.DPS)
		if _, err := db.Exec(r.Context(),
			`INSERT INTO commands (id, tid, did, command_type, payload, status, issued_at)
			 VALUES ($1, $2, $3, 'set', $4, 'pending', NOW())`,
			cmdID, deviceTID, did, payload); err != nil {
			writeErr(w, 500, "database_error", err.Error())
			return
		}
		if err := pub.Publish(deviceTID, pid, did, "set", cmdID, payload); err != nil {
			writeErr(w, 500, "mqtt_publish_failed", err.Error())
			return
		}
		// Optimistic desired shadow.
		for dp, val := range b.DPS {
			if n, err := strconv.Atoi(dp); err == nil {
				db.Exec(r.Context(), `
					INSERT INTO shadows (tid, did, dp_id, desired_value, updated_at)
					VALUES ($1, $2, $3, $4, NOW())
					ON CONFLICT (tid, did, dp_id) DO UPDATE
					  SET desired_value = EXCLUDED.desired_value, updated_at = NOW()`,
					deviceTID, did, n, []byte(val))
			}
		}
		writeJSON(w, 202, map[string]any{"id": cmdID, "status": "pending"})
	}
}

// AdoptDevice handles POST /v1/devices/adopt — links a real provisioned device to a user.
func AdoptDevice(db *pgxpool.Pool, cfg *config.Config, lk *localkey.Service, refresher ...DiscoveryRefresher) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uid := middleware.UIDFromContext(r.Context())
		var b struct {
			MAC  string `json:"mac"`
			Name string `json:"name"`
			Room string `json:"room"`
			Icon string `json:"icon"`
		}
		if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
			writeErr(w, 400, "bad_request", "invalid json")
			return
		}
		// Normalise MAC.
		mac := strings.ToLower(strings.NewReplacer(":", "", "-", "").Replace(strings.TrimSpace(b.MAC)))
		if len(mac) != 12 {
			writeErr(w, 400, "bad_request", "invalid mac address")
			return
		}
		if b.Name == "" {
			writeErr(w, 400, "bad_request", "name required")
			return
		}
		if b.Room == "" {
			b.Room = "Living Room"
		}

		// Look up provisioned device by Wi-Fi MAC or BLE MAC (ESP32: BLE = Wi-Fi + 2).
		var did, pid, invTID string
		err := db.QueryRow(r.Context(), `
			SELECT did, pid, tid FROM device_inventory
			WHERE (mac=$1 OR ble_mac=$1) AND provisioned_at IS NOT NULL
		`, mac).Scan(&did, &pid, &invTID)
		if err != nil {
			writeErr(w, 404, "not_found", "device not provisioned or not in inventory")
			return
		}

		prof := profileForType(deviceTypeForPID(pid))

		// Link to this user's account (using ON CONFLICT to allow claiming transfer or updating metadata).
		_, err = db.Exec(r.Context(), `
			INSERT INTO app_devices (id, owner_id, tid, did, pid, name, room, type, icon)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
			ON CONFLICT (did) DO UPDATE SET
			  owner_id = EXCLUDED.owner_id,
			  name = EXCLUDED.name,
			  room = EXCLUDED.room,
			  icon = EXCLUDED.icon
		`, uuid.New(), uid, cfg.ConsumerTID, did, pid, b.Name, b.Room, deviceTypeForPID(pid), b.Icon)
		if err != nil {
			writeErr(w, 500, "internal_error", err.Error())
			return
		}

		// Provision the LAN local-control key and push "klk" so the device can
		// be driven on-LAN during a cloud outage. Best-effort and off the
		// request path: a fresh context (the request's is cancelled once we
		// respond) and errors only logged — the app can retry via
		// POST /v1/devices/{id}/local-key/provision if the device was offline.
		if lk != nil {
			go func() {
				if err := lk.Provision(context.Background(), invTID, pid, did); err != nil {
					log.Printf("adopt: localkey provision for did=%s: %v", did, err)
				}
			}()
		}

		dps := reportedDPS(r.Context(), db, cfg.ConsumerTID, did)
		on, _ := dps["1"].(bool)
		typ := deviceTypeForPID(pid)
		if len(refresher) > 0 && refresher[0] != nil {
			go refresher[0].TriggerDiscoveryRefresh(uid)
		}
		writeJSON(w, 201, deviceDTO{
			ID: did, DID: did, PID: pid, Name: b.Name, Room: b.Room, Type: typ, Icon: b.Icon,
			On: on, Offline: false, Metric: metricFor(typ, on, dps),
			DPS: dps, Capabilities: prof.Caps,
		})
	}
}

// LinkedAccounts handles GET /v1/linked-accounts.
// Returns which voice platforms the current user has linked.
func LinkedAccounts(db *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uid := middleware.UIDFromContext(r.Context())
		rows, err := db.Query(r.Context(),
			`SELECT platform FROM linked_accounts WHERE user_id=$1 AND unlinked_at IS NULL`, uid)
		if err != nil {
			writeErr(w, 500, "internal", err.Error())
			return
		}
		defer rows.Close()
		result := map[string]bool{"alexa": false, "google": false}
		for rows.Next() {
			var platform string
			rows.Scan(&platform)
			result[platform] = true
		}
		writeJSON(w, 200, result)
	}
}

// DeleteDevice handles DELETE /v1/devices/{id}.
func DeleteDevice(db *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uid := middleware.UIDFromContext(r.Context())
		did := chi.URLParam(r, "id")
		db.Exec(r.Context(),
			`DELETE FROM app_devices WHERE did=$1 AND owner_id=$2`, did, uid)
		w.WriteHeader(http.StatusNoContent)
	}
}

// reportedDPS reads the reported shadow for a device from Postgres.
func reportedDPS(ctx context.Context, db *pgxpool.Pool, tid, did string) map[string]any {
	rows, err := db.Query(ctx,
		`SELECT dp_id, reported_value FROM shadows WHERE tid=$1 AND did=$2`, tid, did)
	if err != nil {
		return map[string]any{}
	}
	defer rows.Close()

	out := map[string]any{}
	for rows.Next() {
		var dpID int
		var raw []byte
		rows.Scan(&dpID, &raw)
		if raw != nil {
			var val any
			json.Unmarshal(raw, &val)
			out[strconv.Itoa(dpID)] = val
		}
	}
	return out
}
