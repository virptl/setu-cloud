package app

import "net/http"

// Rooms handles GET /v1/rooms — static list for MVP.
func Rooms() http.HandlerFunc {
	rooms := []map[string]string{
		{"name": "Living Room", "icon": "tv"},
		{"name": "Kitchen", "icon": "plug"},
		{"name": "Bedroom", "icon": "moon"},
	}
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, rooms)
	}
}

// Scenes handles GET /v1/scenes — stub returns empty list.
func Scenes() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, []any{})
	}
}

// Automations handles GET /v1/automations — stub returns empty list.
func Automations() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, []any{})
	}
}

// RunScene handles POST /v1/scenes/{id}/run — stub.
func RunScene() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}
}

// PatchAutomation handles PATCH /v1/automations/{id} — stub.
func PatchAutomation() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}
}
