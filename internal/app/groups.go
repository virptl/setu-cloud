package app

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/setucore/setu-cloud/internal/api/middleware"
	"github.com/setucore/setu-cloud/internal/config"
	"github.com/setucore/setu-cloud/internal/mqtt"
)

type GroupDTO struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Icon      string   `json:"icon"`
	DIDs      []string `json:"dids"`
	CreatedAt string   `json:"created_at"`
}

// Groups handles GET /v1/groups — returns user device groups.
func Groups(db *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uid := middleware.UIDFromContext(r.Context())
		if uid == "" {
			writeErr(w, 401, "unauthorized", "login required")
			return
		}

		rows, err := db.Query(r.Context(),
			`SELECT g.id, g.name, g.icon, g.created_at, COALESCE(array_agg(gd.did) FILTER (WHERE gd.did IS NOT NULL), '{}')
			 FROM app_groups g
			 LEFT JOIN app_group_devices gd ON g.id = gd.group_id
			 WHERE g.user_id=$1
			 GROUP BY g.id, g.name, g.icon, g.created_at
			 ORDER BY g.created_at DESC`, uid)
		if err != nil {
			writeErr(w, 500, "internal", err.Error())
			return
		}
		defer rows.Close()

		var list []GroupDTO
		for rows.Next() {
			var dto GroupDTO
			var createdAt interface{}
			if err := rows.Scan(&dto.ID, &dto.Name, &dto.Icon, &createdAt, &dto.DIDs); err == nil {
				if dto.DIDs == nil {
					dto.DIDs = []string{}
				}
				list = append(list, dto)
			}
		}

		if list == nil {
			list = []GroupDTO{}
		}

		writeJSON(w, 200, list)
	}
}

// CreateGroup handles POST /v1/groups — creates a device group.
func CreateGroup(db *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uid := middleware.UIDFromContext(r.Context())
		tid := middleware.TIDFromContext(r.Context())
		if uid == "" {
			writeErr(w, 401, "unauthorized", "login required")
			return
		}

		var req struct {
			Name string   `json:"name"`
			Icon string   `json:"icon"`
			DIDs []string `json:"dids"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
			writeErr(w, 400, "bad_request", "name is required")
			return
		}
		if req.Icon == "" {
			req.Icon = "folder"
		}

		groupID := uuid.New().String()
		tx, err := db.Begin(r.Context())
		if err != nil {
			writeErr(w, 500, "internal", err.Error())
			return
		}
		defer tx.Rollback(r.Context())

		var dto GroupDTO
		err = tx.QueryRow(r.Context(),
			`INSERT INTO app_groups (id, user_id, tid, name, icon)
			 VALUES ($1, $2, $3, $4, $5)
			 RETURNING id, name, icon, created_at`,
			groupID, uid, tid, req.Name, req.Icon).Scan(&dto.ID, &dto.Name, &dto.Icon, &dto.CreatedAt)
		if err != nil {
			writeErr(w, 500, "internal", err.Error())
			return
		}

		for _, did := range req.DIDs {
			tx.Exec(r.Context(), `INSERT INTO app_group_devices (group_id, did) VALUES ($1, $2) ON CONFLICT DO NOTHING`, groupID, did)
		}
		tx.Commit(r.Context())

		dto.DIDs = req.DIDs
		if dto.DIDs == nil {
			dto.DIDs = []string{}
		}

		writeJSON(w, 201, dto)
	}
}

// GroupCommand handles POST /v1/groups/{id}/command — sends command to all devices in a group.
func GroupCommand(db *pgxpool.Pool, pub *mqtt.Publisher, cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uid := middleware.UIDFromContext(r.Context())
		tid := middleware.TIDFromContext(r.Context())
		id := chi.URLParam(r, "id")

		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body) == 0 {
			writeErr(w, 400, "bad_request", "payload is required")
			return
		}

		rows, err := db.Query(r.Context(),
			`SELECT gd.did, d.pid FROM app_group_devices gd
			 JOIN app_groups g ON g.id = gd.group_id
			 JOIN devices d ON d.did = gd.did
			 WHERE g.id=$1 AND g.user_id=$2`, id, uid)
		if err != nil {
			writeErr(w, 500, "internal", err.Error())
			return
		}
		defer rows.Close()

		payload, _ := json.Marshal(body)
		sentCount := 0
		for rows.Next() {
			var did, pid string
			if err := rows.Scan(&did, &pid); err == nil {
				cmdID := uuid.New().String()
				db.Exec(r.Context(),
					`INSERT INTO commands (id, tid, did, command_type, payload, status, issued_at)
					 VALUES ($1, $2, $3, 'set', $4, 'pending', NOW())`,
					cmdID, tid, did, payload)
				if pub != nil {
					pub.Publish(tid, pid, did, "set", cmdID, payload)
					sentCount++
				}
			}
		}

		writeJSON(w, 200, map[string]any{"status": "ok", "sent_to_devices": sentCount})
	}
}

// DeleteGroup handles DELETE /v1/groups/{id}.
func DeleteGroup(db *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uid := middleware.UIDFromContext(r.Context())
		id := chi.URLParam(r, "id")

		res, err := db.Exec(r.Context(), `DELETE FROM app_groups WHERE id=$1 AND user_id=$2`, id, uid)
		if err != nil {
			writeErr(w, 500, "internal", err.Error())
			return
		}
		if res.RowsAffected() == 0 {
			writeErr(w, 404, "not_found", "group not found")
			return
		}

		w.WriteHeader(http.StatusNoContent)
	}
}
