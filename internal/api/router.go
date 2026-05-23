package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/setucore/setu-cloud/internal/api/handlers"
	"github.com/setucore/setu-cloud/internal/api/middleware"
	"github.com/setucore/setu-cloud/internal/config"
	"github.com/setucore/setu-cloud/internal/mqtt"
	"github.com/setucore/setu-cloud/internal/registry"
	"github.com/setucore/setu-cloud/internal/shadow"
	"github.com/setucore/setu-cloud/internal/ws"
	"github.com/setucore/setu-cloud/internal/ztp"
)

func NewRouter(
	db *pgxpool.Pool,
	cache *redis.Client,
	pub *mqtt.Publisher,
	hub *ws.Hub,
	cfg *config.Config,
) http.Handler {
	r := chi.NewRouter()
	r.Use(chimw.RealIP)
	r.Use(chimw.Logger)
	r.Use(chimw.Recoverer)

	reg := registry.New(db, cache)
	shd := shadow.New(db, cache)

	// ── Health ────────────────────────────────────────────────────────────────
	r.Get("/health", handlers.Health(db, cache))

	// ── Auth ──────────────────────────────────────────────────────────────────
	r.Post("/auth/token", handlers.Token(db, cfg.JWTSecret))

	// ── Factory ZTP — no JWT, network-isolated ────────────────────────────────
	r.Post("/factory/provision", ztp.HandleProvision(db, cfg))

	// ── Authenticated routes ──────────────────────────────────────────────────
	r.Group(func(r chi.Router) {
		r.Use(middleware.Auth(cfg.JWTSecret))

		r.Get("/devices", handlers.ListDevices(reg))
		r.Get("/devices/{did}", handlers.GetDevice(reg, shd))
		r.Post("/devices/{did}/commands", handlers.IssueCommand(db, pub, shd))
		r.Get("/devices/{did}/events", handlers.ListEvents(db))

		// WebSocket — JWT via query param ?token= or Authorization header
		r.Get("/ws", ws.HandleWS(hub))
	})

	return r
}
