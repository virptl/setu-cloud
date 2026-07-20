package app

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/setucore/setu-cloud/internal/api/middleware"
)

type AutomationTrigger struct {
	Type      string         `json:"type"` // "schedule" or "device"
	Cron      string         `json:"cron,omitempty"`
	DID       string         `json:"did,omitempty"`
	Condition map[string]any `json:"condition,omitempty"`
}

type AutomationDTO struct {
	ID        string              `json:"id"`
	Name      string              `json:"name"`
	Enabled   bool                `json:"enabled"`
	Triggers  []AutomationTrigger `json:"triggers"`
	Actions   []SceneAction       `json:"actions"`
	CreatedAt string              `json:"created_at"`
}

// Automations handles GET /v1/automations — returns user automations.
func Automations(db *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uid := middleware.UIDFromContext(r.Context())
		if uid == "" {
			writeErr(w, 401, "unauthorized", "login required")
			return
		}

		rows, err := db.Query(r.Context(),
			`SELECT id, name, enabled, triggers, actions, created_at FROM app_automations WHERE user_id=$1 ORDER BY created_at DESC`, uid)
		if err != nil {
			writeErr(w, 500, "internal", err.Error())
			return
		}
		defer rows.Close()

		var list []AutomationDTO
		for rows.Next() {
			var dto AutomationDTO
			var triggersRaw, actionsRaw []byte
			var createdAt interface{}
			if err := rows.Scan(&dto.ID, &dto.Name, &dto.Enabled, &triggersRaw, &actionsRaw, &createdAt); err == nil {
				json.Unmarshal(triggersRaw, &dto.Triggers)
				json.Unmarshal(actionsRaw, &dto.Actions)
				list = append(list, dto)
			}
		}

		if list == nil {
			list = []AutomationDTO{}
		}

		writeJSON(w, 200, list)
	}
}

// CreateAutomation handles POST /v1/automations — creates a new automation rule.
func CreateAutomation(db *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uid := middleware.UIDFromContext(r.Context())
		tid := middleware.TIDFromContext(r.Context())
		if uid == "" {
			writeErr(w, 401, "unauthorized", "login required")
			return
		}

		var req struct {
			Name     string              `json:"name"`
			Enabled  *bool               `json:"enabled"`
			Triggers []AutomationTrigger `json:"triggers"`
			Actions  []SceneAction       `json:"actions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
			writeErr(w, 400, "bad_request", "name is required")
			return
		}

		enabled := true
		if req.Enabled != nil {
			enabled = *req.Enabled
		}
		if req.Triggers == nil {
			req.Triggers = []AutomationTrigger{}
		}
		if req.Actions == nil {
			req.Actions = []SceneAction{}
		}

		trigJSON, _ := json.Marshal(req.Triggers)
		actJSON, _ := json.Marshal(req.Actions)
		id := uuid.New().String()

		var dto AutomationDTO
		dto.Enabled = enabled
		dto.Triggers = req.Triggers
		dto.Actions = req.Actions

		err := db.QueryRow(r.Context(),
			`INSERT INTO app_automations (id, user_id, tid, name, enabled, triggers, actions)
			 VALUES ($1, $2, $3, $4, $5, $6, $7)
			 RETURNING id, name, created_at`,
			id, uid, tid, req.Name, enabled, trigJSON, actJSON).Scan(&dto.ID, &dto.Name, &dto.CreatedAt)

		if err != nil {
			writeErr(w, 500, "internal", err.Error())
			return
		}

		writeJSON(w, 201, dto)
	}
}

// PatchAutomation handles PATCH /v1/automations/{id} — toggles or updates automation.
func PatchAutomation(db *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uid := middleware.UIDFromContext(r.Context())
		id := chi.URLParam(r, "id")

		var req struct {
			Name    *string `json:"name"`
			Enabled *bool   `json:"enabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, 400, "bad_request", "invalid json")
			return
		}

		if req.Enabled != nil {
			db.Exec(r.Context(), `UPDATE app_automations SET enabled=$1 WHERE id=$2 AND user_id=$3`, *req.Enabled, id, uid)
		}
		if req.Name != nil {
			db.Exec(r.Context(), `UPDATE app_automations SET name=$1 WHERE id=$2 AND user_id=$3`, *req.Name, id, uid)
		}

		w.WriteHeader(http.StatusNoContent)
	}
}

// DeleteAutomation handles DELETE /v1/automations/{id}.
func DeleteAutomation(db *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uid := middleware.UIDFromContext(r.Context())
		id := chi.URLParam(r, "id")

		res, err := db.Exec(r.Context(), `DELETE FROM app_automations WHERE id=$1 AND user_id=$2`, id, uid)
		if err != nil {
			writeErr(w, 500, "internal", err.Error())
			return
		}
		if res.RowsAffected() == 0 {
			writeErr(w, 404, "not_found", "automation not found")
			return
		}

		w.WriteHeader(http.StatusNoContent)
	}
}
