package config

import "testing"

func TestLoad(t *testing.T) {
	t.Setenv("APP_PORT", "9090")
	t.Setenv("DATABASE_URL", " postgres://sentinel ")
	t.Setenv("FIREBASE_PROJECT_ID", " jawir-dev ")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AppPort != 9090 || cfg.DatabaseURL != "postgres://sentinel" || cfg.FirebaseProjectID != "jawir-dev" {
		t.Fatalf("Load() = %+v", cfg)
	}
}

func TestLoadRejectsInvalidConfiguration(t *testing.T) {
	for _, test := range []struct {
		name      string
		port      string
		database  string
		projectID string
	}{
		{name: "invalid port", port: "0", database: "postgres://sentinel", projectID: "jawir-dev"},
		{name: "missing database", database: "", projectID: "jawir-dev"},
		{name: "missing Firebase project", database: "postgres://sentinel", projectID: ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("APP_PORT", test.port)
			t.Setenv("DATABASE_URL", test.database)
			t.Setenv("FIREBASE_PROJECT_ID", test.projectID)
			if _, err := Load(); err == nil {
				t.Fatal("Load() error = nil")
			}
		})
	}
}
