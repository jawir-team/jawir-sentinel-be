// Package logging provides structured, correlation-friendly logging for API
// and background runtimes.
package logging

import (
	"context"
	"io"
	"log/slog"
	"strings"
)

const (
	// RedactedValue is emitted in place of sensitive log values.
	RedactedValue = "[REDACTED]"

	FieldRequestID = "request_id"
)

// New creates a JSON logger at the requested minimum level. It also applies a
// defensive redaction policy for known sensitive field names.
func New(w io.Writer, level slog.Leveler) *slog.Logger {
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{
		Level:       level,
		ReplaceAttr: replaceAttr,
	}))
}

// With returns the default logger enriched with correlation values from ctx.
func With(ctx context.Context) *slog.Logger {
	return WithLogger(ctx, slog.Default())
}

// WithLogger enriches logger with correlation values from ctx. It is useful
// when a component uses an explicitly injected logger instead of slog.Default.
func WithLogger(ctx context.Context, logger *slog.Logger) *slog.Logger {
	if logger == nil {
		logger = slog.Default()
	}
	if requestID := RequestIDFromContext(ctx); requestID != "" {
		return logger.With(FieldRequestID, requestID)
	}
	return logger
}

// Redact deliberately discards a sensitive value and returns a safe marker.
// Use it for sensitive data whose field name is not covered by the automatic
// field-name policy.
func Redact(_ any) string {
	return RedactedValue
}

func replaceAttr(groups []string, attr slog.Attr) slog.Attr {
	if sensitiveField(groups, attr.Key) {
		attr.Value = slog.StringValue(RedactedValue)
		return attr
	}

	// Match the repository's documented structured logging schema.
	if len(groups) == 0 {
		switch attr.Key {
		case slog.TimeKey:
			attr.Key = "timestamp"
		case slog.MessageKey:
			attr.Key = "message"
		}
	}
	return attr
}

func sensitiveField(groups []string, key string) bool {
	for _, group := range groups {
		if sensitiveKey(group) {
			return true
		}
	}
	return sensitiveKey(key)
}

func sensitiveKey(key string) bool {
	key = normalizeKey(key)
	if key == "" {
		return false
	}

	switch key {
	case "authorization", "cookie", "set_cookie",
		"password", "passwd", "credential", "credentials",
		"secret", "client_secret", "api_key", "private_key",
		"access_token", "refresh_token", "id_token",
		"database_url", "rabbitmq_url",
		"payload", "body", "content", "raw":
		return true
	}

	if strings.Contains(key, "credential") ||
		strings.Contains(key, "password") ||
		strings.Contains(key, "secret") {
		return true
	}
	if strings.Contains(key, "token") &&
		!strings.HasSuffix(key, "_token_count") &&
		key != "token_count" && key != "token_usage" {
		return true
	}

	return sensitiveDomainField(key, "prompt") ||
		sensitiveDomainField(key, "evidence") ||
		sensitiveDomainField(key, "policy")
}

func sensitiveDomainField(key, domain string) bool {
	if !strings.Contains(key, domain) {
		return false
	}
	return !strings.HasSuffix(key, "_id") &&
		!strings.HasSuffix(key, "_version") &&
		!strings.HasSuffix(key, "_count") &&
		!strings.HasSuffix(key, "_status")
}

func normalizeKey(key string) string {
	key = strings.ToLower(strings.TrimSpace(key))
	return strings.NewReplacer("-", "_", ".", "_", " ", "_").Replace(key)
}
