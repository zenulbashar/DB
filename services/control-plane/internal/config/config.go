// Package config loads control-plane settings from the environment.
package config

import (
	"fmt"
	"os"
)

type Config struct {
	Port           string
	DatabaseURL    string
	BootstrapToken string
	GatewayToken   string // NDB_GATEWAY_TOKEN — authenticates the gateway wake call (ADR-014)
	AdminToken     string // NDB_ADMIN_TOKEN — platform-operator surface /v1/admin (ADR-018)
	Env            string // dev|staging|prod
	Version        string
	KEKs           string // NDB_KEKS keyring spec, "1:<base64>,…" (SECURITY_MODEL §5)
	ActiveKEK      string
}

func Load() (Config, error) {
	c := Config{
		Port:           getenv("PORT", "8080"),
		DatabaseURL:    os.Getenv("DATABASE_URL"),
		BootstrapToken: os.Getenv("NDB_BOOTSTRAP_TOKEN"),
		GatewayToken:   os.Getenv("NDB_GATEWAY_TOKEN"),
		AdminToken:     os.Getenv("NDB_ADMIN_TOKEN"),
		Env:            getenv("NDB_ENV", "dev"),
		Version:        getenv("NDB_VERSION", "dev"),
		KEKs:           os.Getenv("NDB_KEKS"),
		ActiveKEK:      os.Getenv("NDB_ACTIVE_KEK"),
	}
	if c.DatabaseURL == "" {
		return c, fmt.Errorf("DATABASE_URL is required")
	}
	return c, nil
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
