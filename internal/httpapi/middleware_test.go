package httpapi_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jawir-team/jawir-sentinel-be/internal/httpapi"
)

func TestRecovererWritesSafeInternalError(t *testing.T) {
	handler := httpapi.Recoverer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("provider-secret stack detail")
	}))
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/panic", nil))

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusInternalServerError)
	}
	body := decodeError(t, response)
	if body.Error.Code != httpapi.CodeInternalError {
		t.Errorf("error code = %q, want %q", body.Error.Code, httpapi.CodeInternalError)
	}
	if strings.Contains(response.Body.String(), "provider-secret") || strings.Contains(response.Body.String(), "stack") {
		t.Fatalf("panic detail leaked in response: %s", response.Body.String())
	}
}

func TestRecovererPassesSuccessfulResponse(t *testing.T) {
	handler := httpapi.Recoverer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		httpapi.WriteJSON(w, http.StatusOK, httpapi.SuccessEnvelope{Data: "healthy"})
	}))
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/health", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	if got, want := response.Body.String(), "{\"data\":\"healthy\"}\n"; got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}
