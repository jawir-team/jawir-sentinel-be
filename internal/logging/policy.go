package logging

import "log/slog"

const (
	FieldPolicyVersionID = "policy_version_id"
	FieldIndexAttemptID  = "index_attempt_id"
)

// PolicyIndexFields contains the safe correlation metadata for a single
// policy-indexing claim or recovery attempt.
type PolicyIndexFields struct {
	PolicyVersionID string
	IndexAttemptID  string
}

// WithPolicyIndex returns a logger scoped to one policy-indexing attempt.
func WithPolicyIndex(logger *slog.Logger, fields PolicyIndexFields) *slog.Logger {
	if logger == nil {
		logger = slog.Default()
	}
	return logger.With(
		FieldPolicyVersionID, fields.PolicyVersionID,
		FieldIndexAttemptID, fields.IndexAttemptID,
	)
}
