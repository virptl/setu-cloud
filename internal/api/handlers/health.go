package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

func Health(db *pgxpool.Pool, cache *redis.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		status := map[string]string{"db": "ok", "redis": "ok"}
		code := http.StatusOK

		if err := db.Ping(r.Context()); err != nil {
			status["db"] = "error"
			code = http.StatusServiceUnavailable
		}
		if err := cache.Ping(r.Context()).Err(); err != nil {
			status["redis"] = "error"
			code = http.StatusServiceUnavailable
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(status)
	}
}
