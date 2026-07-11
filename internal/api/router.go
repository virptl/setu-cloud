package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/setucore/setu-cloud/internal/alexa"
	"github.com/setucore/setu-cloud/internal/api/handlers"
	"github.com/setucore/setu-cloud/internal/api/middleware"
	"github.com/setucore/setu-cloud/internal/app"
	"github.com/setucore/setu-cloud/internal/config"
	"github.com/setucore/setu-cloud/internal/google"
	"github.com/setucore/setu-cloud/internal/iot"
	"github.com/setucore/setu-cloud/internal/keystore"
	"github.com/setucore/setu-cloud/internal/mqtt"
	"github.com/setucore/setu-cloud/internal/oauth"
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
	ks *keystore.Service,
	oauthStore *oauth.Store,
	iotSvc *iot.Service,
	discoveryRefresher app.DiscoveryRefresher,
) http.Handler {
	r := chi.NewRouter()
	r.Use(chimw.RealIP)
	r.Use(chimw.Logger)
	r.Use(chimw.Recoverer)

	reg := registry.New(db, cache)
	shd := shadow.New(db, cache)

	oauthH := oauth.NewHandlers(oauthStore, iotSvc, db, cfg.JWTSecret)
	alexaH := alexa.NewHandler(iotSvc, oauthStore)
	googleH := google.NewHandler(iotSvc, oauthStore)

	// ── Health ────────────────────────────────────────────────────────────────
	r.Get("/health", handlers.Health(db, cache))

	// ── Auth ──────────────────────────────────────────────────────────────────
	r.Post("/auth/token", handlers.Token(db, cfg.JWTSecret))

	// ── Factory ZTP — no JWT, network-isolated ────────────────────────────────
	r.Post("/factory/provision", ztp.HandleProvision(db, cfg, ks))

	// ── Admin / service-to-service (released products + inventory seeding) ─────
	r.Group(func(r chi.Router) {
		r.Use(middleware.ServiceToken(cfg.AdminServiceToken))

		r.Post("/admin/released-products", handlers.UpsertReleasedProduct(db))
		r.Get("/admin/released-products", handlers.ListReleasedProducts(db))
		r.Post("/admin/released-products/retire", handlers.RetireReleasedProduct(db))

		r.Post("/admin/inventory/batches", handlers.CreateBatch(db, cfg))
		r.Get("/admin/inventory", handlers.ListInventory(db))
		r.Get("/admin/inventory/{did}/ztp", handlers.PreviewProvision(db, cfg, ks))
		r.Post("/admin/devices/{did}/ota", handlers.AdminIssueOTA(db, pub, ks))
		r.Get("/admin/batches", handlers.ListBatches(db))
	})

	// ── Authenticated routes ──────────────────────────────────────────────────
	r.Group(func(r chi.Router) {
		r.Use(middleware.Auth(cfg.JWTSecret))

		r.Get("/devices", handlers.ListDevices(reg))
		r.Get("/devices/{did}", handlers.GetDevice(reg, shd))
		r.Post("/devices/{did}/commands", handlers.IssueCommand(db, pub, shd))
		r.Get("/devices/{did}/events", handlers.ListEvents(db))
	})

	// WebSocket — consumer JWT via ?token= or Authorization header
	r.Group(func(r chi.Router) {
		r.Use(middleware.AuthUser(cfg.JWTSecret))
		r.Get("/ws", ws.HandleWS(hub))
	})

	// ── OAuth2 Account Linking ────────────────────────────────────────────────
	r.Route("/oauth", func(r chi.Router) {
		r.HandleFunc("/authorize", oauthH.Authorize)
		r.Post("/token", oauthH.Token)
		r.Get("/userinfo", oauthH.UserInfo)
		r.Post("/revoke", oauthH.Revoke)
	})

	// ── Alexa Smart Home Skill ────────────────────────────────────────────────
	r.Post("/alexa/smarthome", alexaH.ServeHTTP)

	// ── Google Home Action ────────────────────────────────────────────────────
	r.Post("/google/smarthome", googleH.ServeHTTP)

	// ── Consumer app routes (/v1) ─────────────────────────────────────────────
	r.Route("/v1", func(r chi.Router) {
		// Public auth endpoints
		r.Post("/auth/otp/request", app.RequestOTP(db, cfg))
		r.Post("/auth/otp/verify", app.VerifyOTP(db, cfg))
		r.Post("/auth/register", app.Register(db, cfg))
		r.Post("/auth/login", app.Login(db, cfg))
		r.Post("/auth/guest", app.Guest(db, cfg))
		r.Post("/auth/refresh", app.Refresh(db, cfg))
		r.Post("/auth/logout", app.Logout(db))

		// Authenticated (app user JWT)
		r.Group(func(r chi.Router) {
			r.Use(middleware.AuthUser(cfg.JWTSecret))
			r.Get("/devices", app.ListDevices(db, reg))
			r.Post("/devices/claim", app.ClaimDevice(db, cfg, discoveryRefresher))
			r.Post("/devices/adopt", app.AdoptDevice(db, cfg, discoveryRefresher))
			r.Post("/ble/sign", app.SignBLENonce(db, ks))
			r.Post("/devices/{id}/command", app.Command(db, pub, cfg))
			r.Delete("/devices/{id}", app.DeleteDevice(db))
			r.Post("/auth/delete/otp", app.RequestDeleteOTP(db, cfg))
			r.Delete("/auth/delete", app.DeleteAccount(db))
			r.Get("/linked-accounts", app.LinkedAccounts(db))
			r.Get("/products/{pid}/profile", app.GetProductProfile(db))
			r.Get("/rooms", app.Rooms())
			r.Get("/scenes", app.Scenes())
			r.Get("/automations", app.Automations())
			r.Post("/scenes/{id}/run", app.RunScene())
			r.Patch("/automations/{id}", app.PatchAutomation())
		})
	})

	return r
}
