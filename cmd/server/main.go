package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/setucore/setu-cloud/internal/api"
	"github.com/setucore/setu-cloud/internal/cache"
	"github.com/setucore/setu-cloud/internal/config"
	"github.com/setucore/setu-cloud/internal/db"
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

	// ── HTTP server ───────────────────────────────────────────────────────────
	pub := internalmqtt.NewPublisher(mqttClient)
	handler := api.NewRouter(pool, redisClient, pub, hub, cfg)

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
