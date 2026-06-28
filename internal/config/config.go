package config

import (
	"fmt"
	"os"
)

type Config struct {
	ListenAddr string

	DatabaseURL string

	RedisURL string

	JWTSecret string

	MQTTBrokerURL  string
	MQTTClientID   string
	MQTTUsername   string
	MQTTPassword   string
	MQTTCACertFile string

	// URI sent to devices in ZTP response — may differ from MQTTBrokerURL
	DeviceMQTTBrokerURI string

	FactoryProvToken string
	CloudPubkeyHex   string
	CloudPrivKeyHex  string // P-256 private scalar D, 64 hex chars (legacy — use KeyEncryptionKey + tenant_keys table)

	// KeyEncryptionKey wraps per-tenant private keys stored in tenant_keys.
	// Must be exactly 64 hex chars (32 bytes / AES-256).
	// Generate with: openssl rand -hex 32
	KeyEncryptionKey string

	// Consumer module
	ConsumerTID   string
	OTPDevMode    bool
	OTPTTLMinutes int

	// SMTP — used for OTP email delivery when OTPDevMode is false.
	SMTPHost     string
	SMTPPort     string
	SMTPUser     string
	SMTPPassword string
	SMTPFrom     string

	// Voice assistant integrations
	// GoogleSAToken is a short-lived Bearer token for Google Home Graph API (Report State / requestSync).
	// In production, rotate this externally (e.g. via a cron that exchanges a service account key).
	GoogleSAToken string

	// AdminServiceToken guards the service-to-service /admin endpoints
	// (released-products ingest from dev_portal, inventory seeding).
	AdminServiceToken string

	// EMQX HTTP API — used to provision per-device MQTT credentials when seeding
	// inventory. Optional; when EMQXKey is empty, EMQX user creation is skipped.
	EMQXBaseURL string
	EMQXKey     string
	EMQXSecret  string
}

func Load() (*Config, error) {
	c := &Config{
		ListenAddr:          env("LISTEN_ADDR", ":8080"),
		DatabaseURL:         must("DATABASE_URL"),
		RedisURL:            env("REDIS_URL", "redis://localhost:6379"),
		JWTSecret:           must("JWT_SECRET"),
		MQTTBrokerURL:       must("MQTT_BROKER_URL"),
		MQTTClientID:        env("MQTT_CLIENT_ID", "setu-cloud"),
		MQTTUsername:        env("MQTT_USERNAME", ""),
		MQTTPassword:        env("MQTT_PASSWORD", ""),
		MQTTCACertFile:      env("MQTT_CA_CERT_FILE", ""),
		DeviceMQTTBrokerURI: env("DEVICE_MQTT_BROKER_URI", ""),
		FactoryProvToken:    must("FACTORY_PROV_TOKEN"),
		CloudPubkeyHex:      env("CLOUD_PUBKEY_HEX", ""),
		CloudPrivKeyHex:     env("CLOUD_PRIVKEY_HEX", ""),
		KeyEncryptionKey:    env("KEY_ENCRYPTION_KEY", ""),
		ConsumerTID:         env("CONSUMER_TID", "setu"),
		OTPDevMode:          env("OTP_DEV_MODE", "false") == "true",
		OTPTTLMinutes:       10,
		SMTPHost:            env("SMTP_HOST", "smtp.gmail.com"),
		SMTPPort:            env("SMTP_PORT", "465"),
		SMTPUser:            env("SMTP_USER", ""),
		SMTPPassword:        env("SMTP_PASSWORD", ""),
		SMTPFrom:            env("SMTP_FROM", ""),
		GoogleSAToken:       env("GOOGLE_SA_TOKEN", ""),
		AdminServiceToken:   env("ADMIN_SERVICE_TOKEN", ""),
		EMQXBaseURL:         env("EMQX_API_URL", "http://localhost:18083"),
		EMQXKey:             env("EMQX_API_KEY", ""),
		EMQXSecret:          env("EMQX_API_SECRET", ""),
	}

	if c.DeviceMQTTBrokerURI == "" {
		c.DeviceMQTTBrokerURI = c.MQTTBrokerURL
	}

	if c.SMTPFrom == "" {
		c.SMTPFrom = c.SMTPUser
	}

	if !c.OTPDevMode && c.SMTPUser != "" && c.SMTPPassword == "" {
		panic("SMTP_PASSWORD is required when SMTP_USER is set")
	}

	return c, nil
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func must(key string) string {
	v := os.Getenv(key)
	if v == "" {
		panic(fmt.Sprintf("required env var %s is not set", key))
	}
	return v
}
