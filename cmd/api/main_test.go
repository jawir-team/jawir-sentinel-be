package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jawir-team/jawir-sentinel-be/internal/auth"
	db "github.com/jawir-team/jawir-sentinel-be/internal/db/sqlc"
	"github.com/jawir-team/jawir-sentinel-be/internal/logging"
)

type meRouteVerifier struct {
	uid      string
	calls    int
	gotToken string
}

func (f *meRouteVerifier) VerifyIDToken(_ context.Context, token string) (string, error) {
	f.calls++
	f.gotToken = token
	return f.uid, nil
}

type meRouteStore struct {
	user db.User
	unit db.Unit

	userCalls int
	gotUID    string
	unitCalls int
	gotUnitID pgtype.UUID
}

func (f *meRouteStore) GetUserByFirebaseUID(_ context.Context, uid string) (db.User, error) {
	f.userCalls++
	f.gotUID = uid
	return f.user, nil
}

func (f *meRouteStore) GetUnit(_ context.Context, id pgtype.UUID) (db.Unit, error) {
	f.unitCalls++
	f.gotUnitID = id
	return f.unit, nil
}

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
			for _, path := range []string{"/health", "/healthz"} {
				response := httptest.NewRecorder()
				server.Handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
				if response.Code != http.StatusOK {
					t.Fatalf("unauthenticated GET %s = %d, want 200", path, response.Code)
				}
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

func TestServerProtectsAPIRoutes(t *testing.T) {
	t.Setenv("APP_PORT", "8080")
	server, err := newServer()
	if err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodGet, "/api/private", nil)
	request.Header.Set("Authorization", "Bearer token")
	response := httptest.NewRecorder()
	server.Handler.ServeHTTP(response, request)

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("GET /api/private = %d, want 401", response.Code)
	}
}

func TestServerServesAuthenticatedMe(t *testing.T) {
	for _, role := range []string{auth.SystemRoleUser, auth.SystemRoleAdmin} {
		t.Run(role, func(t *testing.T) {
			t.Setenv("APP_PORT", "8080")
			userID := meRouteUUID(1)
			unitID := meRouteUUID(2)
			storedUser := db.User{
				ID:          userID,
				UnitID:      unitID,
				FirebaseUID: "firebase-route-user",
				Name:        "Route User",
				Email:       "route.user@example.test",
				Status:      "ACTIVE",
				SystemRole:  role,
			}
			verifier := &meRouteVerifier{uid: storedUser.FirebaseUID}
			store := &meRouteStore{
				user: storedUser,
				unit: db.Unit{
					ID:   unitID,
					Code: "UNIT-ROUTE",
					Name: "Route Unit",
				},
			}
			server, err := newServerWithAuth(verifier, store)
			if err != nil {
				t.Fatal(err)
			}

			request := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
			request.Header.Set("Authorization", "Bearer route-token")
			response := httptest.NewRecorder()
			server.Handler.ServeHTTP(response, request)

			if response.Code != http.StatusOK {
				t.Fatalf("GET /api/v1/me = %d, want 200; body=%s", response.Code, response.Body.String())
			}
			if got := response.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
				t.Errorf("Content-Type = %q, want application/json; charset=utf-8", got)
			}

			var body struct {
				Data struct {
					ID         string `json:"id"`
					Name       string `json:"name"`
					Email      string `json:"email"`
					SystemRole string `json:"system_role"`
					Unit       struct {
						ID   string `json:"id"`
						Code string `json:"code"`
						Name string `json:"name"`
					} `json:"unit"`
				} `json:"data"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode response body: %v; body=%q", err, response.Body.String())
			}
			if body.Data.ID != "00000000-0000-0000-0000-000000000001" {
				t.Errorf("data.id = %q, want user UUID", body.Data.ID)
			}
			if body.Data.Name != storedUser.Name || body.Data.Email != storedUser.Email {
				t.Errorf("data identity = (%q, %q), want (%q, %q)", body.Data.Name, body.Data.Email, storedUser.Name, storedUser.Email)
			}
			if body.Data.SystemRole != role {
				t.Errorf("data.system_role = %q, want %q", body.Data.SystemRole, role)
			}
			if body.Data.Unit.ID != "00000000-0000-0000-0000-000000000002" || body.Data.Unit.Code != store.unit.Code || body.Data.Unit.Name != store.unit.Name {
				t.Errorf("data.unit = %+v, want ID=%s code=%q name=%q", body.Data.Unit, "00000000-0000-0000-0000-000000000002", store.unit.Code, store.unit.Name)
			}

			if verifier.calls != 1 || verifier.gotToken != "route-token" {
				t.Errorf("verifier calls = %d, token = %q; want 1, route-token", verifier.calls, verifier.gotToken)
			}
			if store.userCalls != 1 || store.gotUID != storedUser.FirebaseUID {
				t.Errorf("user lookup calls = %d, UID = %q; want 1, %q", store.userCalls, store.gotUID, storedUser.FirebaseUID)
			}
			if store.unitCalls != 1 || store.gotUnitID != unitID {
				t.Errorf("unit lookup calls = %d, unit ID = %+v; want 1, %+v", store.unitCalls, store.gotUnitID, unitID)
			}
		})
	}
}

func TestServerDoesNotExposeUnversionedMeRoute(t *testing.T) {
	t.Setenv("APP_PORT", "8080")
	storedUser := db.User{
		ID:          meRouteUUID(1),
		UnitID:      meRouteUUID(2),
		FirebaseUID: "firebase-route-user",
		Name:        "Route User",
		Email:       "route.user@example.test",
		Status:      "ACTIVE",
		SystemRole:  auth.SystemRoleUser,
	}
	verifier := &meRouteVerifier{uid: storedUser.FirebaseUID}
	store := &meRouteStore{user: storedUser}
	server, err := newServerWithAuth(verifier, store)
	if err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	request.Header.Set("Authorization", "Bearer route-token")
	response := httptest.NewRecorder()
	server.Handler.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("GET /api/me = %d, want 400; body=%s", response.Code, response.Body.String())
	}
	if store.unitCalls != 0 {
		t.Errorf("unit lookup calls = %d, want 0", store.unitCalls)
	}
}

func meRouteUUID(last byte) pgtype.UUID {
	var id [16]byte
	id[15] = last
	return pgtype.UUID{Bytes: id, Valid: true}
}
