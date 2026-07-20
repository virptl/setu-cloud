package app

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/setucore/setu-cloud/internal/api/middleware"
)

type RoomDTO struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Icon      string `json:"icon"`
	CreatedAt string `json:"created_at"`
}

// Rooms handles GET /v1/rooms — returns persistent user rooms.
func Rooms(db *pgxpool.Pool) http.HandlerFunc {
	defaultRooms := []struct {
		Name string
		Icon string
	}{
		{"Living Room", "tv"},
		{"Kitchen", "plug"},
		{"Bedroom", "moon"},
	}

	return func(w http.ResponseWriter, r *http.Request) {
		uid := middleware.UIDFromContext(r.Context())
		tid := middleware.TIDFromContext(r.Context())
		if uid == "" {
			writeErr(w, 401, "unauthorized", "login required")
			return
		}

		rows, err := db.Query(r.Context(),
			`SELECT id, name, icon, created_at FROM app_rooms WHERE user_id=$1 ORDER BY created_at ASC`, uid)
		if err != nil {
			writeErr(w, 500, "internal", err.Error())
			return
		}
		defer rows.Close()

		var rooms []RoomDTO
		for rows.Next() {
			var rDTO RoomDTO
			var createdAt interface{}
			if err := rows.Scan(&rDTO.ID, &rDTO.Name, &rDTO.Icon, &createdAt); err == nil {
				rooms = append(rooms, rDTO)
			}
		}

		// First-time user seeding default rooms
		if len(rooms) == 0 {
			for _, dr := range defaultRooms {
				var rDTO RoomDTO
				id := uuid.New().String()
				err := db.QueryRow(r.Context(),
					`INSERT INTO app_rooms (id, user_id, tid, name, icon)
					 VALUES ($1, $2, $3, $4, $5)
					 RETURNING id, name, icon, created_at`,
					id, uid, tid, dr.Name, dr.Icon).Scan(&rDTO.ID, &rDTO.Name, &rDTO.Icon, &rDTO.CreatedAt)
				if err == nil {
					rooms = append(rooms, rDTO)
				}
			}
		}

		if rooms == nil {
			rooms = []RoomDTO{}
		}

		writeJSON(w, 200, rooms)
	}
}

// CreateRoom handles POST /v1/rooms — adds a custom room.
func CreateRoom(db *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uid := middleware.UIDFromContext(r.Context())
		tid := middleware.TIDFromContext(r.Context())
		if uid == "" {
			writeErr(w, 401, "unauthorized", "login required")
			return
		}

		var req struct {
			Name string `json:"name"`
			Icon string `json:"icon"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
			writeErr(w, 400, "bad_request", "name is required")
			return
		}
		if req.Icon == "" {
			req.Icon = "room"
		}

		var room RoomDTO
		id := uuid.New().String()
		err := db.QueryRow(r.Context(),
			`INSERT INTO app_rooms (id, user_id, tid, name, icon)
			 VALUES ($1, $2, $3, $4, $5)
			 RETURNING id, name, icon, created_at`,
			id, uid, tid, req.Name, req.Icon).Scan(&room.ID, &room.Name, &room.Icon, &room.CreatedAt)
		if err != nil {
			writeErr(w, 500, "internal", err.Error())
			return
		}

		writeJSON(w, 201, room)
	}
}

// DeleteRoom handles DELETE /v1/rooms/{id}.
func DeleteRoom(db *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uid := middleware.UIDFromContext(r.Context())
		id := chi.URLParam(r, "id")

		res, err := db.Exec(r.Context(), `DELETE FROM app_rooms WHERE id=$1 AND user_id=$2`, id, uid)
		if err != nil {
			writeErr(w, 500, "internal", err.Error())
			return
		}
		if res.RowsAffected() == 0 {
			writeErr(w, 404, "not_found", "room not found")
			return
		}

		w.WriteHeader(http.StatusNoContent)
	}
}
