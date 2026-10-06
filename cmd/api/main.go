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
	"github.com/jawir-team/jawir-sentinel-be/internal/database"
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
	server, err := newServer()
	if err != nil {
		return err
	}
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
	slog.Info("sentinel-api listening", "address", server.Addr, "component", "api")
	return server.ListenAndServe()
}

func newServer() (*http.Server, error) {
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

	return &http.Server{
		Addr:              fmt.Sprintf(":%d", n),
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}, nil
}
