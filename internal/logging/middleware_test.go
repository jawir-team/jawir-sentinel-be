package logging_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/jawir-team/jawir-sentinel-be/internal/logging"
)

var uuidV4Pattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestRequestIDGeneratesUUIDAndStoresItInContext(t *testing.T) {
	var contextRequestID string
	handler := logging.RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		contextRequestID = logging.RequestIDFromContext(r.Context())
		w.WriteHeader(http.StatusNoContent)
	}))

	response := serveRequest(t, handler, "")
	responseRequestID := response.Header().Get(logging.RequestIDHeader)
	if !uuidV4Pattern.MatchString(responseRequestID) {
		t.Fatalf("generated request ID %q is not a UUID v4", responseRequestID)
	}
	if contextRequestID != responseRequestID {
		t.Errorf("context request ID = %q, response request ID = %q", contextRequestID, responseRequestID)
	}
}

func TestRequestIDPropagatesIncomingValue(t *testing.T) {
	const requestID = "gateway-request-123"
	var contextRequestID string
	handler := logging.RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		contextRequestID = logging.RequestIDFromContext(r.Context())
		w.WriteHeader(http.StatusNoContent)
	}))

	response := serveRequest(t, handler, requestID)
	if got := response.Header().Get(logging.RequestIDHeader); got != requestID {
		t.Errorf("response request ID = %q, want %q", got, requestID)
	}
	if contextRequestID != requestID {
		t.Errorf("context request ID = %q, want %q", contextRequestID, requestID)
	}
}

func TestRequestIDReplacesUnsafeIncomingValues(t *testing.T) {
	tests := []struct {
		name   string
		values []string
	}{
		{name: "blank", values: []string{"   "}},
		{name: "leading whitespace", values: []string{" request-id"}},
		{name: "embedded whitespace", values: []string{"request id"}},
		{name: "control character", values: []string{"request\ninjected"}},
		{name: "list", values: []string{"first,second"}},
		{name: "non ASCII", values: []string{"permintaan-é"}},
		{name: "too long", values: []string{strings.Repeat("a", 129)}},
		{name: "multiple", values: []string{"first", "second"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := logging.RequestID(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNoContent)
			}))
			request := httptest.NewRequest(http.MethodGet, "/", nil)
			for _, value := range tt.values {
				request.Header.Add(logging.RequestIDHeader, value)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)

			if got := response.Header().Get(logging.RequestIDHeader); !uuidV4Pattern.MatchString(got) {
				t.Fatalf("replacement request ID %q is not a UUID v4", got)
			}
		})
	}
}

func httpHandler(log func(context.Context)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log(r.Context())
		w.WriteHeader(http.StatusNoContent)
	})
}

func serveRequest(t *testing.T, handler http.Handler, requestID string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	if requestID != "" {
		request.Header.Set(logging.RequestIDHeader, requestID)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
