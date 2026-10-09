package config

import "testing"

func TestLoad(t *testing.T) {
	t.Setenv("APP_PORT", "9090")
	t.Setenv("DATABASE_URL", " postgres://sentinel ")
	t.Setenv("FIREBASE_PROJECT_ID", " jawir-dev ")
	t.Setenv("RABBITMQ_URL", " amqp://rabbitmq ")
	t.Setenv("RABBITMQ_AI_QUEUE", " custom.ai ")
	t.Setenv("AI_WORKER_LEASE_SECONDS", "45")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AppPort != 9090 || cfg.DatabaseURL != "postgres://sentinel" || cfg.FirebaseProjectID != "jawir-dev" ||
		cfg.RabbitMQURL != "amqp://rabbitmq" || cfg.RabbitMQAIQueue != "custom.ai" || cfg.AIWorkerLeaseSeconds != 45 {
		t.Fatalf("Load() = %+v", cfg)
	}
}

func TestLoadWorkerDefaults(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://sentinel")
	t.Setenv("FIREBASE_PROJECT_ID", "")
	t.Setenv("RABBITMQ_URL", "amqp://rabbitmq")
	t.Setenv("RABBITMQ_AI_QUEUE", "")
	t.Setenv("AI_WORKER_LEASE_SECONDS", "")

	cfg, err := LoadWorker()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RabbitMQAIQueue != "sentinel.ai_analysis" || cfg.AIWorkerLeaseSeconds != 300 {
		t.Fatalf("LoadWorker() = %+v", cfg)
	}
}

func TestLoadWorkerRequiresRabbitMQ(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://sentinel")
	t.Setenv("RABBITMQ_URL", "")
	if _, err := LoadWorker(); err == nil || err.Error() != "RABBITMQ_URL is required for the worker" {
		t.Fatalf("LoadWorker() error = %v", err)
	}
}

func TestLoadRejectsInvalidConfiguration(t *testing.T) {
	for _, test := range []struct {
		name      string
		port      string
		database  string
		projectID string
		lease     string
	}{
		{name: "invalid port", port: "0", database: "postgres://sentinel", projectID: "jawir-dev"},
		{name: "missing database", database: "", projectID: "jawir-dev"},
		{name: "missing Firebase project", database: "postgres://sentinel", projectID: ""},
		{name: "invalid worker lease", database: "postgres://sentinel", projectID: "jawir-dev", lease: "0"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("APP_PORT", test.port)
			t.Setenv("DATABASE_URL", test.database)
			t.Setenv("FIREBASE_PROJECT_ID", test.projectID)
			t.Setenv("AI_WORKER_LEASE_SECONDS", test.lease)
			if _, err := Load(); err == nil {
				t.Fatal("Load() error = nil")
			}
		})
	}
}
