package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"encoding/hex"

	"github.com/setucore/setu-cloud/internal/api"
	"github.com/setucore/setu-cloud/internal/cache"
	"github.com/setucore/setu-cloud/internal/config"
	"github.com/setucore/setu-cloud/internal/db"
	"github.com/setucore/setu-cloud/internal/keystore"
	internalmqtt "github.com/setucore/setu-cloud/internal/mqtt"
	"github.com/setucore/setu-cloud/internal/ws"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// ── Database ──────────────────────────────────────────────────────────────
	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	defer pool.Close()
	log.Println("PostgreSQL connected")

	// ── Redis ─────────────────────────────────────────────────────────────────
	redisClient, err := cache.Connect(ctx, cfg.RedisURL)
	if err != nil {
		log.Fatalf("redis: %v", err)
	}
	defer redisClient.Close()
	log.Println("Redis connected")

	// ── Keystore ──────────────────────────────────────────────────────────────
	kek, err := hex.DecodeString(cfg.KeyEncryptionKey)
	if err != nil || len(kek) != 32 {
		log.Fatal("KEY_ENCRYPTION_KEY must be exactly 64 hex chars (32 bytes). Generate with: openssl rand -hex 32")
	}
	ks, err := keystore.New(pool, kek)
	if err != nil {
		log.Fatalf("keystore: %v", err)
	}
	log.Println("Keystore initialised")

	// ── MQTT ──────────────────────────────────────────────────────────────────
	mqttClient, err := internalmqtt.NewClient(
		cfg.MQTTBrokerURL,
		cfg.MQTTClientID,
		cfg.MQTTUsername,
		cfg.MQTTPassword,
		cfg.MQTTCACertFile,
	)
	if err != nil {
		log.Fatalf("mqtt: %v", err)
	}
	log.Println("MQTT connected:", cfg.MQTTBrokerURL)

	router := internalmqtt.NewRouter(pool, redisClient)
	internalmqtt.Subscribe(mqttClient, router)

	// ── WebSocket hub ─────────────────────────────────────────────────────────
	hub := ws.NewHub(redisClient)
	go hub.RunRedisSubscriber(ctx)

	// ── device_events cleanup ─────────────────────────────────────────────────
	// Runs once at startup (catches any backlog) then every 24 hours.
	// Requires idx_device_events_ts (migration 0009).
	go func() {
		cleanup := func() {
			ct, err := pool.Exec(ctx,
				`DELETE FROM device_events WHERE ts < NOW() - INTERVAL '90 days'`)
			if err != nil {
				log.Printf("device_events cleanup: %v", err)
			} else if ct.RowsAffected() > 0 {
				log.Printf("device_events cleanup: removed %d rows", ct.RowsAffected())
			}
		}
		cleanup()
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				cleanup()
			}
		}
	}()

	// ── HTTP server ───────────────────────────────────────────────────────────
	pub := internalmqtt.NewPublisher(mqttClient)
	handler := api.NewRouter(pool, redisClient, pub, hub, cfg, ks)

	srv := &http.Server{
		Addr:         cfg.ListenAddr,
		Handler:      handler,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	go func() {
		log.Printf("setu-cloud listening on %s", cfg.ListenAddr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server: %v", err)
		}
	}()

	// ── Graceful shutdown ─────────────────────────────────────────────────────
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("Shutting down...")

	shutCtx, shutCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutCancel()
	srv.Shutdown(shutCtx)
	cancel()
	log.Println("Done.")
}
