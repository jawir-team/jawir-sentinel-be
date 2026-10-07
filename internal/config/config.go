// Package config loads and validates process configuration.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

const defaultAppPort = 8080

type Config struct {
	AppPort           int
	DatabaseURL       string
	FirebaseProjectID string
}

func Load() (Config, error) {
	cfg := Config{
		AppPort:           defaultAppPort,
		DatabaseURL:       strings.TrimSpace(os.Getenv("DATABASE_URL")),
		FirebaseProjectID: strings.TrimSpace(os.Getenv("FIREBASE_PROJECT_ID")),
	}

	if raw := strings.TrimSpace(os.Getenv("APP_PORT")); raw != "" {
		port, err := strconv.Atoi(raw)
		if err != nil || port < 1 || port > 65535 {
			return Config{}, fmt.Errorf("APP_PORT must be an integer between 1 and 65535")
		}
		cfg.AppPort = port
	}
	if cfg.DatabaseURL == "" {
		return Config{}, errors.New("DATABASE_URL is required")
	}
	if cfg.FirebaseProjectID == "" {
		return Config{}, errors.New("FIREBASE_PROJECT_ID is required")
	}
	return cfg, nil
}
