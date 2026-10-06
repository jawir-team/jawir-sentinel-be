package logging

import "log/slog"

const (
	FieldOutboxEventID     = "outbox_event_id"
	FieldAnalysisID        = "analysis_id"
	FieldCaseID            = "case_id"
	FieldRabbitMQMessageID = "rabbitmq_message_id"
	FieldRedelivered       = "redelivered"
	FieldAttempt           = "attempt"
)

// WorkerFields contains the safe correlation metadata that must accompany a
// durable worker job throughout dispatch, delivery, retry, and completion.
// Payloads and full database/message objects must not be logged.
type WorkerFields struct {
	OutboxEventID     string
	AnalysisID        string
	CaseID            string
	RabbitMQMessageID string
	Redelivered       bool
	Attempt           int
}

// WithWorker returns a logger scoped to one durable worker job.
func WithWorker(logger *slog.Logger, fields WorkerFields) *slog.Logger {
	if logger == nil {
		logger = slog.Default()
	}
	return logger.With(
		FieldOutboxEventID, fields.OutboxEventID,
		FieldAnalysisID, fields.AnalysisID,
		FieldCaseID, fields.CaseID,
		FieldRabbitMQMessageID, fields.RabbitMQMessageID,
		FieldRedelivered, fields.Redelivered,
		FieldAttempt, fields.Attempt,
	)
}
