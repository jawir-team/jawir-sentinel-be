package logging_test

import (
	"bytes"
	"log/slog"
	"testing"

	"github.com/jawir-team/jawir-sentinel-be/internal/logging"
)

func TestWithWorkerAddsDurableJobCorrelationFields(t *testing.T) {
	var output bytes.Buffer
	logger := logging.WithWorker(logging.New(&output, slog.LevelInfo), logging.WorkerFields{
		OutboxEventID:     "outbox-1",
		AnalysisID:        "analysis-2",
		CaseID:            "case-3",
		RabbitMQMessageID: "message-4",
		Redelivered:       true,
		Attempt:           5,
	})

	logger.Info("job received")
	record := decodeLog(t, output.String())
	want := map[string]any{
		logging.FieldOutboxEventID:     "outbox-1",
		logging.FieldAnalysisID:        "analysis-2",
		logging.FieldCaseID:            "case-3",
		logging.FieldRabbitMQMessageID: "message-4",
		logging.FieldRedelivered:       true,
		logging.FieldAttempt:           float64(5),
	}
	for key, value := range want {
		if record[key] != value {
			t.Errorf("%s = %v, want %v", key, record[key], value)
		}
	}
}

func TestWithWorkerKeepsZeroValueDeliveryIndicators(t *testing.T) {
	var output bytes.Buffer
	logging.WithWorker(logging.New(&output, slog.LevelInfo), logging.WorkerFields{}).Info("job pending")

	record := decodeLog(t, output.String())
	if got, ok := record[logging.FieldRedelivered]; !ok || got != false {
		t.Errorf("redelivered = %v (present %t), want false and present", got, ok)
	}
	if got, ok := record[logging.FieldAttempt]; !ok || got != float64(0) {
		t.Errorf("attempt = %v (present %t), want 0 and present", got, ok)
	}
}

func TestWithPolicyIndexAddsRecoveryCorrelationFields(t *testing.T) {
	var output bytes.Buffer
	logging.WithPolicyIndex(logging.New(&output, slog.LevelInfo), logging.PolicyIndexFields{
		PolicyVersionID: "policy-version-1",
		IndexAttemptID:  "index-attempt-2",
	}).Info("policy indexing started")

	record := decodeLog(t, output.String())
	if record[logging.FieldPolicyVersionID] != "policy-version-1" {
		t.Errorf("policy_version_id = %v, want policy-version-1", record[logging.FieldPolicyVersionID])
	}
	if record[logging.FieldIndexAttemptID] != "index-attempt-2" {
		t.Errorf("index_attempt_id = %v, want index-attempt-2", record[logging.FieldIndexAttemptID])
	}
}
