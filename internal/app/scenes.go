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

type SceneAction struct {
	DID string         `json:"did"`
	DPS map[string]any `json:"dps"`
}

type SceneDTO struct {
	ID        string        `json:"id"`
	Name      string        `json:"name"`
	Icon      string        `json:"icon"`
	Actions   []SceneAction `json:"actions"`
	CreatedAt string        `json:"created_at"`
}

// Scenes handles GET /v1/scenes — returns list of scenes.
func Scenes(db *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uid := middleware.UIDFromContext(r.Context())
		if uid == "" {
			writeErr(w, 401, "unauthorized", "login required")
			return
		}

		rows, err := db.Query(r.Context(),
			`SELECT id, name, icon, actions, created_at FROM app_scenes WHERE user_id=$1 ORDER BY created_at DESC`, uid)
		if err != nil {
			writeErr(w, 500, "internal", err.Error())
			return
		}
		defer rows.Close()

		var scenes []SceneDTO
		for rows.Next() {
			var sDTO SceneDTO
			var actionsRaw []byte
			var createdAt interface{}
			if err := rows.Scan(&sDTO.ID, &sDTO.Name, &sDTO.Icon, &actionsRaw, &createdAt); err == nil {
				json.Unmarshal(actionsRaw, &sDTO.Actions)
				scenes = append(scenes, sDTO)
			}
		}

		if scenes == nil {
			scenes = []SceneDTO{}
		}

		writeJSON(w, 200, scenes)
	}
}

// CreateScene handles POST /v1/scenes — creates a new scene.
func CreateScene(db *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uid := middleware.UIDFromContext(r.Context())
		tid := middleware.TIDFromContext(r.Context())
		if uid == "" {
			writeErr(w, 401, "unauthorized", "login required")
			return
		}

		var req struct {
			Name    string        `json:"name"`
			Icon    string        `json:"icon"`
			Actions []SceneAction `json:"actions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
			writeErr(w, 400, "bad_request", "name is required")
			return
		}
		if req.Icon == "" {
			req.Icon = "play"
		}
		if req.Actions == nil {
			req.Actions = []SceneAction{}
		}

		actionsJSON, _ := json.Marshal(req.Actions)
		id := uuid.New().String()

		var scene SceneDTO
		scene.Actions = req.Actions

		err := db.QueryRow(r.Context(),
			`INSERT INTO app_scenes (id, user_id, tid, name, icon, actions)
			 VALUES ($1, $2, $3, $4, $5, $6)
			 RETURNING id, name, icon, created_at`,
			id, uid, tid, req.Name, req.Icon, actionsJSON).Scan(&scene.ID, &scene.Name, &scene.Icon, &scene.CreatedAt)

		if err != nil {
			writeErr(w, 500, "internal", err.Error())
			return
		}

		writeJSON(w, 201, scene)
	}
}

// RunScene handles POST /v1/scenes/{id}/run — executes all scene actions.
func RunScene(db *pgxpool.Pool, pub *mqtt.Publisher, cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uid := middleware.UIDFromContext(r.Context())
		tid := middleware.TIDFromContext(r.Context())
		id := chi.URLParam(r, "id")

		var actionsRaw []byte
		err := db.QueryRow(r.Context(),
			`SELECT actions FROM app_scenes WHERE id=$1 AND user_id=$2`, id, uid).Scan(&actionsRaw)
		if err != nil {
			writeErr(w, 404, "not_found", "scene not found")
			return
		}

		var actions []SceneAction
		json.Unmarshal(actionsRaw, &actions)

		// Execute scene actions
		for _, action := range actions {
			if action.DID != "" && len(action.DPS) > 0 {
				var pid string
				db.QueryRow(r.Context(), `SELECT pid FROM devices WHERE did=$1`, action.DID).Scan(&pid)
				if pid != "" {
					cmdID := uuid.New().String()
					payload, _ := json.Marshal(action.DPS)
					db.Exec(r.Context(),
						`INSERT INTO commands (id, tid, did, command_type, payload, status, issued_at)
						 VALUES ($1, $2, $3, 'set', $4, 'pending', NOW())`,
						cmdID, tid, action.DID, payload)
					if pub != nil {
						pub.Publish(tid, pid, action.DID, "set", cmdID, payload)
					}
				}
			}
		}

		w.WriteHeader(http.StatusNoContent)
	}
}

// DeleteScene handles DELETE /v1/scenes/{id}.
func DeleteScene(db *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uid := middleware.UIDFromContext(r.Context())
		id := chi.URLParam(r, "id")

		res, err := db.Exec(r.Context(), `DELETE FROM app_scenes WHERE id=$1 AND user_id=$2`, id, uid)
		if err != nil {
			writeErr(w, 500, "internal", err.Error())
			return
		}
		if res.RowsAffected() == 0 {
			writeErr(w, 404, "not_found", "scene not found")
			return
		}

		w.WriteHeader(http.StatusNoContent)
	}
}
