// Package config loads and validates process configuration.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	defaultAppPort              = 8080
	defaultRabbitMQAIQueue      = "sentinel.ai_analysis"
	defaultAIWorkerLeaseSeconds = 300

	DefaultMaxReanalysis           = int32(3)
	DefaultTechnicalMaxRetries     = 3
	MaxTechnicalMaxRetries         = 10
	DefaultPolicyRetrievalTopK     = int32(8)
	DefaultPolicyIndexLeaseSeconds = int64(900)
	maxPolicyIndexLeaseSeconds     = int64(time.Duration(1<<63-1) / time.Second)
)

type Config struct {
	AppPort                 int
	DatabaseURL             string
	FirebaseProjectID       string
	RabbitMQURL             string
	RabbitMQAIQueue         string
	AIWorkerLeaseSeconds    int
	MaxReanalysis           int32
	TechnicalMaxRetries     int
	PolicyRetrievalTopK     int32
	PolicyIndexLeaseSeconds int64
	GCPProjectID            string
	VertexAILocation        string
	VertexAIModel           string
	VertexEmbeddingModel    string
	GCSBucket               string
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

// LoadVertexAI loads the centralized runtime settings used by the Vertex AI
// compatibility constructor without requiring database or process-specific
// API/worker settings.
func LoadVertexAI() (Config, error) {
	return loadRuntime()
}

func load() (Config, error) {
	cfg, err := loadRuntime()
	if err != nil {
		return Config{}, err
	}
	cfg.AppPort = defaultAppPort
	cfg.DatabaseURL = strings.TrimSpace(os.Getenv("DATABASE_URL"))
	cfg.FirebaseProjectID = strings.TrimSpace(os.Getenv("FIREBASE_PROJECT_ID"))
	cfg.RabbitMQURL = strings.TrimSpace(os.Getenv("RABBITMQ_URL"))
	cfg.RabbitMQAIQueue = strings.TrimSpace(os.Getenv("RABBITMQ_AI_QUEUE"))
	cfg.AIWorkerLeaseSeconds = defaultAIWorkerLeaseSeconds
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

func loadRuntime() (Config, error) {
	cfg := Config{
		MaxReanalysis:           DefaultMaxReanalysis,
		TechnicalMaxRetries:     DefaultTechnicalMaxRetries,
		PolicyRetrievalTopK:     DefaultPolicyRetrievalTopK,
		PolicyIndexLeaseSeconds: DefaultPolicyIndexLeaseSeconds,
		GCPProjectID:            strings.TrimSpace(os.Getenv("GCP_PROJECT_ID")),
		VertexAILocation:        strings.TrimSpace(os.Getenv("VERTEX_AI_LOCATION")),
		VertexAIModel:           strings.TrimSpace(os.Getenv("VERTEX_AI_MODEL")),
		VertexEmbeddingModel:    strings.TrimSpace(os.Getenv("VERTEX_EMBEDDING_MODEL")),
		GCSBucket:               strings.TrimSpace(os.Getenv("GCS_BUCKET")),
	}

	var err error
	if cfg.MaxReanalysis, err = int32Setting("MAX_REANALYSIS", DefaultMaxReanalysis, 0); err != nil {
		return Config{}, err
	}
	if cfg.TechnicalMaxRetries, err = intSetting("AI_TECHNICAL_MAX_RETRIES", DefaultTechnicalMaxRetries, 0, MaxTechnicalMaxRetries); err != nil {
		return Config{}, err
	}
	if cfg.PolicyRetrievalTopK, err = int32Setting("POLICY_RETRIEVAL_TOP_K", DefaultPolicyRetrievalTopK, 1); err != nil {
		return Config{}, err
	}
	if cfg.PolicyIndexLeaseSeconds, err = int64Setting("POLICY_INDEX_LEASE_SECONDS", DefaultPolicyIndexLeaseSeconds, 1, maxPolicyIndexLeaseSeconds); err != nil {
		return Config{}, err
	}

	for _, required := range []struct {
		name  string
		value string
	}{
		{name: "GCP_PROJECT_ID", value: cfg.GCPProjectID},
		{name: "VERTEX_AI_LOCATION", value: cfg.VertexAILocation},
		{name: "VERTEX_AI_MODEL", value: cfg.VertexAIModel},
	} {
		if required.value == "" {
			return Config{}, fmt.Errorf("%s is required", required.name)
		}
	}
	if strings.ContainsAny(cfg.VertexAILocation, "/?#") || strings.ContainsAny(cfg.VertexAILocation, " \t\r\n") {
		return Config{}, errors.New("VERTEX_AI_LOCATION is invalid")
	}
	return cfg, nil
}

func intSetting(name string, defaultValue, minValue, maxValue int) (int, error) {
	raw, set := os.LookupEnv(name)
	if !set {
		return defaultValue, nil
	}
	raw = strings.TrimSpace(raw)
	value, err := strconv.Atoi(raw)
	if err != nil || value < minValue || value > maxValue {
		return 0, fmt.Errorf("%s must be an integer between %d and %d", name, minValue, maxValue)
	}
	return value, nil
}

func int32Setting(name string, defaultValue, minValue int32) (int32, error) {
	raw, set := os.LookupEnv(name)
	if !set {
		return defaultValue, nil
	}
	raw = strings.TrimSpace(raw)
	value, err := strconv.ParseInt(raw, 10, 32)
	if err != nil || value < int64(minValue) {
		return 0, fmt.Errorf("%s must be an integer between %d and %d", name, minValue, int64(1<<31-1))
	}
	return int32(value), nil
}

func int64Setting(name string, defaultValue, minValue, maxValue int64) (int64, error) {
	raw, set := os.LookupEnv(name)
	if !set {
		return defaultValue, nil
	}
	raw = strings.TrimSpace(raw)
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < minValue || value > maxValue {
		return 0, fmt.Errorf("%s must be an integer between %d and %d", name, minValue, maxValue)
	}
	return value, nil
}
