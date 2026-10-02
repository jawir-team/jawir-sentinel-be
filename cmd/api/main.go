package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
)

func main() {
	server, err := newServer()
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("sentinel-api listening on %s", server.Addr)
	log.Fatal(server.ListenAndServe())
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
