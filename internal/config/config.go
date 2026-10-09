// Package config loads and validates process configuration.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

const (
	defaultAppPort              = 8080
	defaultRabbitMQAIQueue      = "sentinel.ai_analysis"
	defaultAIWorkerLeaseSeconds = 300
)

type Config struct {
	AppPort              int
	DatabaseURL          string
	FirebaseProjectID    string
	RabbitMQURL          string
	RabbitMQAIQueue      string
	AIWorkerLeaseSeconds int
}

func Load() (Config, error) {
	cfg, err := load()
	if err != nil {
		return Config{}, err
	}
	if cfg.FirebaseProjectID == "" {
		return Config{}, errors.New("FIREBASE_PROJECT_ID is required")
	}
	return cfg, nil
}

// LoadWorker loads the settings shared by the durable worker. RabbitMQ is
// intentionally optional for the API process, but a worker cannot run without
// it.
func LoadWorker() (Config, error) {
	cfg, err := load()
	if err != nil {
		return Config{}, err
	}
	if cfg.RabbitMQURL == "" {
		return Config{}, errors.New("RABBITMQ_URL is required for the worker")
	}
	return cfg, nil
}

func load() (Config, error) {
	cfg := Config{
		AppPort:              defaultAppPort,
		DatabaseURL:          strings.TrimSpace(os.Getenv("DATABASE_URL")),
		FirebaseProjectID:    strings.TrimSpace(os.Getenv("FIREBASE_PROJECT_ID")),
		RabbitMQURL:          strings.TrimSpace(os.Getenv("RABBITMQ_URL")),
		RabbitMQAIQueue:      strings.TrimSpace(os.Getenv("RABBITMQ_AI_QUEUE")),
		AIWorkerLeaseSeconds: defaultAIWorkerLeaseSeconds,
	}
	if cfg.RabbitMQAIQueue == "" {
		cfg.RabbitMQAIQueue = defaultRabbitMQAIQueue
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
	if raw := strings.TrimSpace(os.Getenv("AI_WORKER_LEASE_SECONDS")); raw != "" {
		seconds, err := strconv.Atoi(raw)
		if err != nil || seconds <= 0 {
			return Config{}, errors.New("AI_WORKER_LEASE_SECONDS must be a positive integer")
		}
		cfg.AIWorkerLeaseSeconds = seconds
	}
	return cfg, nil
}
