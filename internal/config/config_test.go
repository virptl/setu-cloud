package config_test

import (
	"os"
	"testing"

	"github.com/setucore/setu-cloud/internal/config"
)

func TestLoadConfig_RequiredEnvVars(t *testing.T) {
	// Clear relevant env vars
	os.Unsetenv("DATABASE_URL")
	os.Unsetenv("JWT_SECRET")
	os.Unsetenv("MQTT_BROKER_URL")
	os.Unsetenv("FACTORY_PROV_TOKEN")

	defer func() {
		if r := recover(); r == nil {
			t.Errorf("expected Load() to panic when required env vars are missing")
		}
	}()

	config.Load()
}

func TestLoadConfig_Success(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://user:pass@localhost:5432/setu")
	t.Setenv("JWT_SECRET", "super-secret-jwt-key")
	t.Setenv("MQTT_BROKER_URL", "tcp://localhost:1883")
	t.Setenv("FACTORY_PROV_TOKEN", "factory-secret-token")
	t.Setenv("OTP_DEV_MODE", "false")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error loading config: %v", err)
	}

	if cfg.DatabaseURL != "postgres://user:pass@localhost:5432/setu" {
		t.Errorf("unexpected DatabaseURL: %s", cfg.DatabaseURL)
	}
	if cfg.JWTSecret != "super-secret-jwt-key" {
		t.Errorf("unexpected JWTSecret: %s", cfg.JWTSecret)
	}
	if cfg.MQTTBrokerURL != "tcp://localhost:1883" {
		t.Errorf("unexpected MQTTBrokerURL: %s", cfg.MQTTBrokerURL)
	}
	if cfg.FactoryProvToken != "factory-secret-token" {
		t.Errorf("unexpected FactoryProvToken: %s", cfg.FactoryProvToken)
	}
	if cfg.OTPDevMode != false {
		t.Errorf("expected OTPDevMode to be false")
	}
}

func TestLoadConfig_OTPDevModeTrue(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://user:pass@localhost:5432/setu")
	t.Setenv("JWT_SECRET", "super-secret-jwt-key")
	t.Setenv("MQTT_BROKER_URL", "tcp://localhost:1883")
	t.Setenv("FACTORY_PROV_TOKEN", "factory-secret-token")
	t.Setenv("OTP_DEV_MODE", "true")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error loading config: %v", err)
	}

	if !cfg.OTPDevMode {
		t.Errorf("expected OTPDevMode to be true when env OTP_DEV_MODE=true")
	}
}
