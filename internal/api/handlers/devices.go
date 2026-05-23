package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/setucore/setu-cloud/internal/api/middleware"
	"github.com/setucore/setu-cloud/internal/mqtt"
	"github.com/setucore/setu-cloud/internal/registry"
	"github.com/setucore/setu-cloud/internal/shadow"
)

// ListDevices returns all devices for the authenticated tenant.
func ListDevices(reg *registry.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tid := middleware.TIDFromContext(r.Context())
		devices, err := reg.List(r.Context(), tid)
		if err != nil {
			http.Error(w, `{"error":"internal"}`, http.StatusInternalServerError)
			return
		}
		if devices == nil {
			devices = []registry.Device{}
		}
		writeJSON(w, http.StatusOK, devices)
	}
}

// GetDevice returns a single device with its current shadow.
func GetDevice(reg *registry.Service, shd *shadow.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tid := middleware.TIDFromContext(r.Context())
		did := chi.URLParam(r, "did")

		device, err := reg.Get(r.Context(), tid, did)
		if err != nil {
			http.Error(w, `{"error":"not_found"}`, http.StatusNotFound)
			return
		}

		shadowData, _ := shd.GetShadow(r.Context(), tid, did)

		writeJSON(w, http.StatusOK, map[string]interface{}{
			"device": device,
			"shadow": shadowData,
		})
	}
}

type commandRequest struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

// IssueCommand creates a command record and publishes it to the device.
func IssueCommand(db *pgxpool.Pool, pub *mqtt.Publisher, shd *shadow.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tid := middleware.TIDFromContext(r.Context())
		did := chi.URLParam(r, "did")

		var req commandRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Type == "" {
			http.Error(w, `{"error":"bad_request"}`, http.StatusBadRequest)
			return
		}

		cmdID := uuid.New().String()

		// Fetch device PID for topic construction.
		var pid string
		db.QueryRow(r.Context(), `SELECT pid FROM devices WHERE tid=$1 AND did=$2`, tid, did).Scan(&pid)
		if pid == "" {
			http.Error(w, `{"error":"device_not_found"}`, http.StatusNotFound)
			return
		}

		// Persist command as pending.
		if _, err := db.Exec(r.Context(), `
			INSERT INTO commands (id, tid, did, command_type, payload, status, issued_at)
			VALUES ($1, $2, $3, $4, $5, 'pending', NOW())
		`, cmdID, tid, did, req.Type, []byte(req.Payload)); err != nil {
			http.Error(w, `{"error":"internal"}`, http.StatusInternalServerError)
			return
		}

		// Publish to device — fresh timestamp is set inside publisher.
		if err := pub.Publish(tid, pid, did, req.Type, cmdID, req.Payload); err != nil {
			http.Error(w, `{"error":"mqtt_publish_failed"}`, http.StatusInternalServerError)
			return
		}

		// Optimistically set desired shadow for set commands.
		if req.Type == "set" {
			var dps map[string]json.RawMessage
			if json.Unmarshal(req.Payload, &dps) == nil {
				for dpStr, val := range dps {
					var dpID int
					if _, err := fmt.Sscanf(dpStr, "%d", &dpID); err == nil {
						shd.SetDesired(r.Context(), tid, did, dpID, val)
					}
				}
			}
		}

		writeJSON(w, http.StatusAccepted, map[string]interface{}{
			"id":        cmdID,
			"status":    "pending",
			"issued_at": time.Now().Unix(),
		})
	}
}

// ListEvents returns recent device events.
func ListEvents(db *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tid := middleware.TIDFromContext(r.Context())
		did := chi.URLParam(r, "did")

		rows, err := db.Query(r.Context(), `
			SELECT id, tid, did, event_type, payload, ts
			  FROM device_events
			 WHERE tid=$1 AND did=$2
			 ORDER BY ts DESC
			 LIMIT 100
		`, tid, did)
		if err != nil {
			http.Error(w, `{"error":"internal"}`, http.StatusInternalServerError)
			return
		}
		defer rows.Close()

		type event struct {
			ID        int64           `json:"id"`
			TID       string          `json:"tid"`
			DID       string          `json:"did"`
			EventType string          `json:"event_type"`
			Payload   json.RawMessage `json:"payload"`
			TS        time.Time       `json:"ts"`
		}
		var events []event
		for rows.Next() {
			var e event
			rows.Scan(&e.ID, &e.TID, &e.DID, &e.EventType, &e.Payload, &e.TS)
			events = append(events, e)
		}
		if events == nil {
			events = []event{}
		}
		writeJSON(w, http.StatusOK, events)
	}
}

func writeJSON(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}
