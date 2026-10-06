package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jawir-team/jawir-sentinel-be/internal/auth"
	"github.com/jawir-team/jawir-sentinel-be/internal/database"
	db "github.com/jawir-team/jawir-sentinel-be/internal/db/sqlc"
	"github.com/jawir-team/jawir-sentinel-be/internal/handler"
	"github.com/jawir-team/jawir-sentinel-be/internal/httpapi"
	"github.com/jawir-team/jawir-sentinel-be/internal/logging"
)

func main() {
	logger := logging.New(os.Stdout, slog.LevelInfo)
	slog.SetDefault(logger)
	if err := run(); err != nil {
		logger.Error("sentinel-api stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool, err := database.Open(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := database.Migrate(ctx, pool); err != nil {
		return err
	}
	queries := db.New(pool)
	server, err := newServerWithStores(auth.NewVerifier(), queries, queries, queries)
	if err != nil {
		return err
	}
	slog.Info("sentinel-api listening", "address", server.Addr, "component", "api")
	return server.ListenAndServe()
}

func newServer() (*http.Server, error) {
	return newServerWithAuth(auth.NewVerifier(), nil)
}

func newServerWithAuth(verifier auth.TokenVerifier, users auth.UserStore) (*http.Server, error) {
	meUnits, _ := users.(handler.MeUnitStore)
	units, _ := users.(handler.UnitStore)
	return newServerWithStores(verifier, users, meUnits, units)
}

func newServerWithStores(
	verifier auth.TokenVerifier,
	users auth.UserStore,
	meUnits handler.MeUnitStore,
	units handler.UnitStore,
) (*http.Server, error) {
	port := os.Getenv("APP_PORT")
	if port == "" {
		port = "8080"
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return nil, fmt.Errorf("APP_PORT must be an integer between 1 and 65535")
	}

	router := chi.NewRouter()
	router.Use(logging.RequestID)
	router.Use(httpapi.Recoverer)
	router.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	router.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	protected := chi.NewRouter()
	protected.Use(auth.Middleware(verifier, users))
	protected.Get("/v1/me", handler.GetMe(meUnits))
	protected.With(auth.RequireAuth).Get("/v1/units", handler.ListUnits(units))
	protected.With(auth.RequireAdmin).Post("/v1/units", handler.CreateUnit(units))
	// Catch-all: auth middleware must run even for undefined /api paths,
	// so register a wildcard route instead of relying on NotFound (which
	// bypasses middleware on mounted routers).
	protected.HandleFunc("/*", func(w http.ResponseWriter, r *http.Request) {
		httpapi.WriteError(w, httpapi.NewError(httpapi.CodeInvalidRequest, "not found", nil))
	})
	router.Mount("/api", protected)

	return &http.Server{
		Addr:              fmt.Sprintf(":%d", n),
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}, nil
}
