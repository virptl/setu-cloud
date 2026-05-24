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
		FactoryProvToken:    env("FACTORY_PROV_TOKEN", ""),
		CloudPubkeyHex:      env("CLOUD_PUBKEY_HEX", ""),
	}

	if c.DeviceMQTTBrokerURI == "" {
		c.DeviceMQTTBrokerURI = c.MQTTBrokerURL
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
