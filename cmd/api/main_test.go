package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jawir-team/jawir-sentinel-be/internal/logging"
)

func TestServer(t *testing.T) {
	for _, tt := range []struct {
		port string
		addr string
	}{
		{"", ":8080"},
		{"9090", ":9090"},
		{"1", ":1"},
		{"65535", ":65535"},
		{"0", ""},
		{"-1", ""},
		{"65536", ""},
		{"invalid", ""},
	} {
		t.Run("port="+tt.port, func(t *testing.T) {
			t.Setenv("APP_PORT", tt.port)
			server, err := newServer()
			if tt.addr == "" {
				if err == nil {
					t.Fatal("expected invalid port to be rejected")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if server.Addr != tt.addr {
				t.Fatalf("address = %q, want %q", server.Addr, tt.addr)
			}
			response := httptest.NewRecorder()
			server.Handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/health", nil))
			if response.Code != http.StatusOK {
				t.Fatalf("unauthenticated GET /health = %d, want 200", response.Code)
			}
		})
	}
}

func TestServerPropagatesRequestID(t *testing.T) {
	t.Setenv("APP_PORT", "8080")
	server, err := newServer()
	if err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodGet, "/health", nil)
	request.Header.Set(logging.RequestIDHeader, "gateway-request-123")
	response := httptest.NewRecorder()
	server.Handler.ServeHTTP(response, request)

	if got := response.Header().Get(logging.RequestIDHeader); got != "gateway-request-123" {
		t.Errorf("response request ID = %q, want gateway-request-123", got)
	}
}
