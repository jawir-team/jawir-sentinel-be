package logging_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/jawir-team/jawir-sentinel-be/internal/logging"
)

func TestNewWritesStructuredJSONAtConfiguredLevel(t *testing.T) {
	var output bytes.Buffer
	logger := logging.New(&output, slog.LevelWarn)

	logger.Info("filtered", "component", "test")
	if output.Len() != 0 {
		t.Fatalf("info log was not filtered: %s", output.String())
	}

	logger.Warn("worker delayed", "component", "worker")
	record := decodeLog(t, output.String())
	if record["level"] != "WARN" {
		t.Errorf("level = %v, want WARN", record["level"])
	}
	if record["message"] != "worker delayed" {
		t.Errorf("message = %v, want worker delayed", record["message"])
	}
	if record["component"] != "worker" {
		t.Errorf("component = %v, want worker", record["component"])
	}
	if _, ok := record["timestamp"]; !ok {
		t.Error("timestamp field is missing")
	}
	if _, ok := record["msg"]; ok {
		t.Error("unexpected slog msg key; want documented message key")
	}
}

func TestWithAddsRequestIDFromContext(t *testing.T) {
	var output bytes.Buffer
	logger := logging.New(&output, slog.LevelInfo)
	previous := slog.Default()
	slog.SetDefault(logger)
	t.Cleanup(func() { slog.SetDefault(previous) })

	handler := logging.RequestID(httpHandler(func(ctx context.Context) {
		logging.With(ctx).Info("request handled")
	}))
	serveRequest(t, handler, "client-request-123")

	record := decodeLog(t, output.String())
	if record[logging.FieldRequestID] != "client-request-123" {
		t.Errorf("request_id = %v, want client-request-123", record[logging.FieldRequestID])
	}
}

func TestWithWithoutRequestIDDoesNotAddEmptyField(t *testing.T) {
	var output bytes.Buffer
	logging.WithLogger(context.Background(), logging.New(&output, slog.LevelInfo)).Info("background event")

	record := decodeLog(t, output.String())
	if _, ok := record[logging.FieldRequestID]; ok {
		t.Errorf("unexpected empty request_id field: %v", record[logging.FieldRequestID])
	}
}

func TestSensitiveFieldsAreRedacted(t *testing.T) {
	const secret = "sentinel-secret-value"
	var output bytes.Buffer
	logger := logging.New(&output, slog.LevelInfo)

	logger.Info("safe message",
		"prompt", secret,
		"evidence_content", secret,
		"policy_content", secret,
		"credentials", map[string]string{"password": secret},
		"payload", secret,
		"custom_sensitive_value", logging.Redact(secret),
		logging.FieldPolicyVersionID, "policy-version-safe",
		slog.Group("authentication", "access_token", secret),
	)

	if strings.Contains(output.String(), secret) {
		t.Fatalf("sensitive value leaked in log: %s", output.String())
	}
	record := decodeLog(t, output.String())
	for _, key := range []string{"prompt", "evidence_content", "policy_content", "credentials", "payload", "custom_sensitive_value"} {
		if record[key] != logging.RedactedValue {
			t.Errorf("%s = %v, want %s", key, record[key], logging.RedactedValue)
		}
	}
	if record[logging.FieldPolicyVersionID] != "policy-version-safe" {
		t.Errorf("policy_version_id = %v, want safe correlation value", record[logging.FieldPolicyVersionID])
	}
}

func decodeLog(t *testing.T, line string) map[string]any {
	t.Helper()
	var record map[string]any
	if err := json.Unmarshal([]byte(line), &record); err != nil {
		t.Fatalf("decode log %q: %v", line, err)
	}
	return record
}
