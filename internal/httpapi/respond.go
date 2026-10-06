package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
)

// SuccessEnvelope is the standard response shape for successful requests.
type SuccessEnvelope struct {
	Data any `json:"data"`
}

// ErrorEnvelope is the standard response shape for failed requests.
type ErrorEnvelope struct {
	Error ErrorBody `json:"error"`
}

// ErrorBody contains only information that is safe to return to a client.
type ErrorBody struct {
	Code    ErrorCode      `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details"`
}

// WriteJSON writes an envelope as JSON. If the envelope cannot be encoded, it
// writes a safe INTERNAL_ERROR response instead.
func WriteJSON(w http.ResponseWriter, status int, envelope any) {
	payload, err := json.Marshal(envelope)
	if err != nil {
		writeInternalError(w)
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(append(payload, '\n'))
}

// WriteError maps a typed API error to the public HTTP contract. Unknown,
// unsupported, and nil errors are deliberately reduced to a generic 500 so
// internal error text cannot cross the HTTP boundary.
func WriteError(w http.ResponseWriter, err error) {
	code := CodeInternalError
	message := defaultMessage(code)
	details := map[string]any{}
	status := http.StatusInternalServerError

	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr != nil {
		if mappedStatus, ok := statusForCode(apiErr.Code); ok {
			status = mappedStatus
			code = apiErr.Code

			// INTERNAL_ERROR is always generic, even if it was constructed with
			// unsafe text or details by mistake.
			if code != CodeInternalError {
				message = apiErr.Message
				if message == "" {
					message = defaultMessage(code)
				}
				if apiErr.Details != nil {
					details = apiErr.Details
				}
			}
		}
	}

	WriteJSON(w, status, ErrorEnvelope{
		Error: ErrorBody{
			Code:    code,
			Message: message,
			Details: details,
		},
	})
}

func writeInternalError(w http.ResponseWriter) {
	payload := []byte(`{"error":{"code":"INTERNAL_ERROR","message":"An internal error occurred.","details":{}}}` + "\n")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusInternalServerError)
	_, _ = w.Write(payload)
}
