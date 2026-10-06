package httpapi_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jawir-team/jawir-sentinel-be/internal/httpapi"
)

func TestWriteErrorPublicMapping(t *testing.T) {
	tests := []struct {
		name   string
		code   httpapi.ErrorCode
		status int
	}{
		{"invalid request", httpapi.CodeInvalidRequest, http.StatusBadRequest},
		{"unauthorized", httpapi.CodeUnauthorized, http.StatusUnauthorized},
		{"forbidden", httpapi.CodeForbidden, http.StatusForbidden},
		{"segregation of duties", httpapi.CodeSegregationOfDutiesViolation, http.StatusForbidden},
		{"user not found", httpapi.CodeUserNotFound, http.StatusNotFound},
		{"case not found", httpapi.CodeCaseNotFound, http.StatusNotFound},
		{"policy not found", httpapi.CodePolicyNotFound, http.StatusNotFound},
		{"analysis not found", httpapi.CodeAnalysisNotFound, http.StatusNotFound},
		{"conflict", httpapi.CodeConflict, http.StatusConflict},
		{"cardinality violation", httpapi.CodeCardinalityViolation, http.StatusConflict},
		{"invalid state transition", httpapi.CodeInvalidStateTransition, http.StatusConflict},
		{"validation error", httpapi.CodeValidationError, http.StatusBadRequest},
		{"stale analysis", httpapi.CodeStaleAnalysis, http.StatusConflict},
		{"policy indexing failed", httpapi.CodePolicyIndexingFailed, http.StatusBadGateway},
		{"internal error", httpapi.CodeInternalError, http.StatusInternalServerError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			httpapi.WriteError(response, httpapi.NewError(tt.code, "", nil))

			if response.Code != tt.status {
				t.Fatalf("status = %d, want %d", response.Code, tt.status)
			}
			if got := response.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
				t.Fatalf("Content-Type = %q, want application/json; charset=utf-8", got)
			}

			body := decodeError(t, response)
			if body.Error.Code != tt.code {
				t.Errorf("error code = %q, want %q", body.Error.Code, tt.code)
			}
			if body.Error.Message == "" {
				t.Error("error message must not be empty")
			}
			if body.Error.Details == nil || len(body.Error.Details) != 0 {
				t.Errorf("details = %#v, want an empty object", body.Error.Details)
			}
		})
	}
}

func TestWriteErrorPreservesClientSafeContext(t *testing.T) {
	response := httptest.NewRecorder()
	err := httpapi.NewError(
		httpapi.CodeInvalidStateTransition,
		"Case must be in CHECKING state.",
		map[string]any{"current_state": "DRAFT"},
	)

	httpapi.WriteError(response, err)

	body := decodeError(t, response)
	if body.Error.Message != "Case must be in CHECKING state." {
		t.Errorf("message = %q", body.Error.Message)
	}
	if body.Error.Details["current_state"] != "DRAFT" {
		t.Errorf("details = %#v", body.Error.Details)
	}
}

func TestWriteErrorKeepsWrappedCausePrivate(t *testing.T) {
	cause := errors.New("vertex payload: credential=provider-secret")
	apiErr := httpapi.WrapError(
		httpapi.CodePolicyIndexingFailed,
		"Policy indexing failed.",
		nil,
		cause,
	)
	err := fmt.Errorf("activate policy: %w", apiErr)
	if !errors.Is(err, cause) {
		t.Fatal("wrapped cause is not available to errors.Is")
	}

	response := httptest.NewRecorder()
	httpapi.WriteError(response, err)

	if response.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusBadGateway)
	}
	if strings.Contains(response.Body.String(), "provider-secret") || strings.Contains(response.Body.String(), "vertex payload") {
		t.Fatalf("private cause leaked in response: %s", response.Body.String())
	}
}

func TestWriteErrorSanitizesNonPublicErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{"nil", nil},
		{"plain error", errors.New("postgres://admin:secret@database")},
		{
			"explicit internal error",
			httpapi.NewError(
				httpapi.CodeInternalError,
				"stack trace and provider-secret",
				map[string]any{"credential": "provider-secret"},
			),
		},
		{"policy conflict semantic status", httpapi.NewError(httpapi.ErrorCode("POLICY_CONFLICT"), "provider-secret", nil)},
		{"governed reanalysis outcome", httpapi.NewError(httpapi.ErrorCode("REANALYSIS_LIMIT_REACHED"), "provider-secret", nil)},
		{"asynchronous analysis failure", httpapi.NewError(httpapi.ErrorCode("AI_ANALYSIS_FAILED"), "provider-secret", nil)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			httpapi.WriteError(response, tt.err)

			if response.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d, want %d", response.Code, http.StatusInternalServerError)
			}
			body := decodeError(t, response)
			if body.Error.Code != httpapi.CodeInternalError {
				t.Errorf("error code = %q, want %q", body.Error.Code, httpapi.CodeInternalError)
			}
			if body.Error.Message != "An internal error occurred." {
				t.Errorf("message = %q, want generic internal message", body.Error.Message)
			}
			if len(body.Error.Details) != 0 {
				t.Errorf("details = %#v, want empty object", body.Error.Details)
			}
			if strings.Contains(response.Body.String(), "secret") || strings.Contains(response.Body.String(), "postgres") {
				t.Fatalf("internal detail leaked in response: %s", response.Body.String())
			}
		})
	}
}

func TestWriteJSONSuccessEnvelope(t *testing.T) {
	response := httptest.NewRecorder()
	httpapi.WriteJSON(response, http.StatusCreated, httpapi.SuccessEnvelope{
		Data: map[string]any{"id": "case-id"},
	})

	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusCreated)
	}
	if got := response.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Fatalf("Content-Type = %q, want application/json; charset=utf-8", got)
	}
	if got, want := response.Body.String(), "{\"data\":{\"id\":\"case-id\"}}\n"; got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}

func TestWriteJSONEncodingFailureIsSafe(t *testing.T) {
	response := httptest.NewRecorder()
	httpapi.WriteJSON(response, http.StatusOK, httpapi.SuccessEnvelope{Data: make(chan int)})

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusInternalServerError)
	}
	body := decodeError(t, response)
	if body.Error.Code != httpapi.CodeInternalError {
		t.Errorf("error code = %q, want %q", body.Error.Code, httpapi.CodeInternalError)
	}
}

func decodeError(t *testing.T, response *httptest.ResponseRecorder) httpapi.ErrorEnvelope {
	t.Helper()

	var body httpapi.ErrorEnvelope
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response body: %v; body=%q", err, response.Body.String())
	}
	return body
}
