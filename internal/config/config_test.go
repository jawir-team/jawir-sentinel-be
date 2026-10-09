package config

import (
	"os"
	"strings"
	"testing"
)

func TestLoadValidOverrides(t *testing.T) {
	setValidBaseEnv(t)
	t.Setenv("APP_PORT", "9090")
	t.Setenv("DATABASE_URL", " postgres://sentinel ")
	t.Setenv("FIREBASE_PROJECT_ID", " jawir-dev ")
	t.Setenv("RABBITMQ_URL", " amqp://rabbitmq ")
	t.Setenv("RABBITMQ_AI_QUEUE", " custom.ai ")
	t.Setenv("AI_WORKER_LEASE_SECONDS", "45")
	t.Setenv("MAX_REANALYSIS", "0")
	t.Setenv("AI_TECHNICAL_MAX_RETRIES", "10")
	t.Setenv("POLICY_RETRIEVAL_TOP_K", "12")
	t.Setenv("POLICY_INDEX_LEASE_SECONDS", "1200")
	t.Setenv("GCP_PROJECT_ID", " gcp-project ")
	t.Setenv("VERTEX_AI_LOCATION", " asia-southeast1 ")
	t.Setenv("VERTEX_AI_MODEL", " gemini-test ")
	t.Setenv("VERTEX_EMBEDDING_MODEL", " embedding-test ")
	t.Setenv("GCS_BUCKET", " evidence-bucket ")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AppPort != 9090 || cfg.DatabaseURL != "postgres://sentinel" || cfg.FirebaseProjectID != "jawir-dev" ||
		cfg.RabbitMQURL != "amqp://rabbitmq" || cfg.RabbitMQAIQueue != "custom.ai" || cfg.AIWorkerLeaseSeconds != 45 ||
		cfg.MaxReanalysis != 0 || cfg.TechnicalMaxRetries != 10 || cfg.PolicyRetrievalTopK != 12 ||
		cfg.PolicyIndexLeaseSeconds != 1200 || cfg.GCPProjectID != "gcp-project" ||
		cfg.VertexAILocation != "asia-southeast1" || cfg.VertexAIModel != "gemini-test" ||
		cfg.VertexEmbeddingModel != "embedding-test" || cfg.GCSBucket != "evidence-bucket" {
		t.Fatalf("Load() = %+v", cfg)
	}
}

func TestLoadWorkerDefaultsWhenOptionalValuesUnset(t *testing.T) {
	setValidBaseEnv(t)
	for _, name := range []string{
		"APP_PORT",
		"RABBITMQ_AI_QUEUE",
		"AI_WORKER_LEASE_SECONDS",
		"MAX_REANALYSIS",
		"AI_TECHNICAL_MAX_RETRIES",
		"POLICY_RETRIEVAL_TOP_K",
		"POLICY_INDEX_LEASE_SECONDS",
		"VERTEX_EMBEDDING_MODEL",
		"GCS_BUCKET",
	} {
		unsetEnv(t, name)
	}

	cfg, err := LoadWorker()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AppPort != 8080 || cfg.RabbitMQAIQueue != "sentinel.ai_analysis" || cfg.AIWorkerLeaseSeconds != 300 ||
		cfg.MaxReanalysis != DefaultMaxReanalysis || cfg.TechnicalMaxRetries != DefaultTechnicalMaxRetries ||
		cfg.PolicyRetrievalTopK != DefaultPolicyRetrievalTopK || cfg.PolicyIndexLeaseSeconds != DefaultPolicyIndexLeaseSeconds ||
		cfg.VertexEmbeddingModel != "" || cfg.GCSBucket != "" {
		t.Fatalf("LoadWorker() = %+v", cfg)
	}
}

func TestLoadRejectsInvalidRuntimeIntegers(t *testing.T) {
	tests := []struct {
		name     string
		variable string
		value    string
	}{
		{name: "max reanalysis empty", variable: "MAX_REANALYSIS", value: ""},
		{name: "max reanalysis non-integer", variable: "MAX_REANALYSIS", value: "many"},
		{name: "max reanalysis negative", variable: "MAX_REANALYSIS", value: "-1"},
		{name: "max reanalysis overflow", variable: "MAX_REANALYSIS", value: "2147483648"},
		{name: "technical retries empty", variable: "AI_TECHNICAL_MAX_RETRIES", value: ""},
		{name: "technical retries non-integer", variable: "AI_TECHNICAL_MAX_RETRIES", value: "many"},
		{name: "technical retries negative", variable: "AI_TECHNICAL_MAX_RETRIES", value: "-1"},
		{name: "technical retries above maximum", variable: "AI_TECHNICAL_MAX_RETRIES", value: "11"},
		{name: "retrieval top-k empty", variable: "POLICY_RETRIEVAL_TOP_K", value: ""},
		{name: "retrieval top-k non-integer", variable: "POLICY_RETRIEVAL_TOP_K", value: "many"},
		{name: "retrieval top-k negative", variable: "POLICY_RETRIEVAL_TOP_K", value: "-1"},
		{name: "retrieval top-k zero", variable: "POLICY_RETRIEVAL_TOP_K", value: "0"},
		{name: "retrieval top-k overflow", variable: "POLICY_RETRIEVAL_TOP_K", value: "2147483648"},
		{name: "policy lease empty", variable: "POLICY_INDEX_LEASE_SECONDS", value: ""},
		{name: "policy lease non-integer", variable: "POLICY_INDEX_LEASE_SECONDS", value: "many"},
		{name: "policy lease negative", variable: "POLICY_INDEX_LEASE_SECONDS", value: "-1"},
		{name: "policy lease zero", variable: "POLICY_INDEX_LEASE_SECONDS", value: "0"},
		{name: "policy lease above duration range", variable: "POLICY_INDEX_LEASE_SECONDS", value: "9223372037"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			setValidBaseEnv(t)
			t.Setenv(test.variable, test.value)
			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), test.variable) {
				t.Fatalf("Load() error = %v, want error naming %s", err, test.variable)
			}
		})
	}
}

func TestLoadRejectsInvalidRequiredAndProcessConfiguration(t *testing.T) {
	tests := []struct {
		name     string
		variable string
		value    string
	}{
		{name: "invalid port", variable: "APP_PORT", value: "0"},
		{name: "missing database", variable: "DATABASE_URL", value: ""},
		{name: "missing Firebase project", variable: "FIREBASE_PROJECT_ID", value: ""},
		{name: "invalid worker lease", variable: "AI_WORKER_LEASE_SECONDS", value: "0"},
		{name: "missing GCP project", variable: "GCP_PROJECT_ID", value: ""},
		{name: "missing Vertex location", variable: "VERTEX_AI_LOCATION", value: ""},
		{name: "invalid Vertex location", variable: "VERTEX_AI_LOCATION", value: "asia/southeast1"},
		{name: "missing Vertex model", variable: "VERTEX_AI_MODEL", value: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			setValidBaseEnv(t)
			t.Setenv(test.variable, test.value)
			if _, err := Load(); err == nil {
				t.Fatal("Load() error = nil")
			}
		})
	}
}

func TestLoadWorkerRequiresRabbitMQ(t *testing.T) {
	setValidBaseEnv(t)
	t.Setenv("RABBITMQ_URL", "")
	if _, err := LoadWorker(); err == nil || err.Error() != "RABBITMQ_URL is required for the worker" {
		t.Fatalf("LoadWorker() error = %v", err)
	}
}

func setValidBaseEnv(t *testing.T) {
	t.Helper()
	values := map[string]string{
		"APP_PORT":                "",
		"DATABASE_URL":            "postgres://sentinel",
		"FIREBASE_PROJECT_ID":     "jawir-dev",
		"RABBITMQ_URL":            "amqp://rabbitmq",
		"RABBITMQ_AI_QUEUE":       "",
		"AI_WORKER_LEASE_SECONDS": "",
		"GCP_PROJECT_ID":          "gcp-project",
		"VERTEX_AI_LOCATION":      "asia-southeast1",
		"VERTEX_AI_MODEL":         "gemini-test",
		"VERTEX_EMBEDDING_MODEL":  "",
		"GCS_BUCKET":              "",
	}
	for name, value := range values {
		t.Setenv(name, value)
	}
	for _, name := range []string{
		"MAX_REANALYSIS",
		"AI_TECHNICAL_MAX_RETRIES",
		"POLICY_RETRIEVAL_TOP_K",
		"POLICY_INDEX_LEASE_SECONDS",
	} {
		unsetEnv(t, name)
	}
}

func unsetEnv(t *testing.T, name string) {
	t.Helper()
	old, existed := os.LookupEnv(name)
	if err := os.Unsetenv(name); err != nil {
		t.Fatalf("unset %s: %v", name, err)
	}
	t.Cleanup(func() {
		if existed {
			_ = os.Setenv(name, old)
			return
		}
		_ = os.Unsetenv(name)
	})
}
