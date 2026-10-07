package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jawir-team/jawir-sentinel-be/internal/auth"
	"github.com/jawir-team/jawir-sentinel-be/internal/config"
	"github.com/jawir-team/jawir-sentinel-be/internal/database"
	db "github.com/jawir-team/jawir-sentinel-be/internal/db/sqlc"
	"github.com/jawir-team/jawir-sentinel-be/internal/handler"
	"github.com/jawir-team/jawir-sentinel-be/internal/httpapi"
	"github.com/jawir-team/jawir-sentinel-be/internal/logging"
	"github.com/jawir-team/jawir-sentinel-be/internal/reanalysis"
	"github.com/jawir-team/jawir-sentinel-be/internal/storage"
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
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	verifier, err := auth.NewVerifier(ctx, cfg.FirebaseProjectID)
	if err != nil {
		return err
	}
	pool, err := database.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := database.Migrate(ctx, pool); err != nil {
		return err
	}
	queries := db.New(pool)
	txStore := handler.NewTxQueries(pool)
	reanalysisService := reanalysis.New(pool)
	fileStorage := storage.NewFromEnv()
	server, err := newServerWithStores(cfg.AppPort, verifier, queries, queries, queries, queries, txStore, txStore, reanalysisService, queries, fileStorage, queries)
	if err != nil {
		return err
	}
	slog.Info("sentinel-api listening", "address", server.Addr, "component", "api")
	return server.ListenAndServe()
}

func newServer(port int) (*http.Server, error) {
	return newServerWithAuth(port, nil, nil)
}

func newServerWithAuth(port int, verifier auth.TokenVerifier, users auth.UserStore) (*http.Server, error) {
	meUnits, _ := users.(handler.MeUnitStore)
	units, _ := users.(handler.UnitStore)
	caseTypes, _ := users.(handler.CaseTypeStore)
	caseStore, _ := users.(handler.CaseStore)
	caseParticipantStore, _ := users.(handler.CaseParticipantStore)
	userAPI, _ := users.(handler.UserStore)
	policyVersions, _ := users.(handler.PolicyVersionStore)
	return newServerWithStores(port, verifier, users, meUnits, units, caseTypes, caseStore, caseParticipantStore, nil, policyVersions, nil, userAPI)
}

func newServerWithStores(
	port int,
	verifier auth.TokenVerifier,
	users auth.UserStore,
	meUnits handler.MeUnitStore,
	units handler.UnitStore,
	caseTypes handler.CaseTypeStore,
	caseStore handler.CaseStore,
	caseParticipantStore handler.CaseParticipantStore,
	reanalysisOrchestrator handler.ReanalysisOrchestrator,
	policyVersionStore handler.PolicyVersionStore,
	fileStorage storage.Store,
	userStores ...handler.UserStore,
) (*http.Server, error) {
	if port < 1 || port > 65535 {
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
	protected.With(auth.RequireAuth).Get("/v1/case-types", handler.ListCaseTypes(caseTypes))
	protected.With(auth.RequireAdmin).Post("/v1/case-types", handler.CreateCaseType(caseTypes))
	policyStore, _ := caseParticipantStore.(handler.PolicyStore)
	protected.With(auth.RequireAuth).Get("/v1/policies", handler.ListPolicies(policyStore))
	protected.With(auth.RequireAdmin).Post("/v1/policies", handler.CreatePolicy(policyStore))
	protected.With(auth.RequireAuth).Get("/v1/policies/{id}/versions", handler.ListPolicyVersions(policyVersionStore))
	protected.With(auth.RequireAdmin).Post("/v1/policies/{id}/versions", handler.CreatePolicyVersion(policyVersionStore))
	protected.With(auth.RequireAuth).Get("/v1/policies/{id}/versions/{version_id}", handler.GetPolicyVersion(policyVersionStore))
	policyActivationStore, _ := caseParticipantStore.(handler.PolicyActivationStore)
	protected.With(auth.RequireAdmin).Post("/v1/policies/{id}/versions/{version_id}/claim-index", handler.ClaimPolicyVersionIndex(policyActivationStore))
	protected.With(auth.RequireAdmin).Post("/v1/policies/{id}/versions/{version_id}/index-result", handler.CompletePolicyVersionIndex(policyActivationStore))
	protected.With(auth.RequireAdmin).Post("/v1/policies/{id}/versions/{version_id}/activate", handler.ActivatePolicyVersion(policyActivationStore))
	// Policy version content is immutable. A change is created through POST as
	// a new version, so PUT, PATCH, and DELETE routes are intentionally absent.
	protected.With(auth.RequireAuth).Get("/v1/cases", handler.ListCases(caseStore))
	protected.With(auth.RequireAuth).Post("/v1/cases", handler.CreateCase(caseStore))
	protected.With(auth.RequireAuth).Get("/v1/cases/{id}", handler.GetCase(caseStore))
	protected.With(auth.RequireAuth).Patch("/v1/cases/{id}", handler.UpdateCase(caseStore))
	submitCaseStore, _ := caseParticipantStore.(handler.SubmitCaseStore)
	protected.With(auth.RequireAuth).Post("/v1/cases/{id}/submit", handler.SubmitCase(submitCaseStore))
	closeCaseStore, _ := caseParticipantStore.(handler.CloseCaseStore)
	protected.With(auth.RequireAuth).Post("/v1/cases/{id}/close", handler.CloseCase(closeCaseStore))
	checkerDecisionStore, _ := caseParticipantStore.(handler.CheckerDecisionStore)
	protected.With(auth.RequireAuth).Post("/v1/cases/{id}/checker-decisions", handler.RecordCheckerDecision(handler.CheckerDecider{
		Store: checkerDecisionStore, Reanalysis: reanalysisOrchestrator,
	}))
	protected.With(auth.RequireAuth).Post("/v1/cases/{id}/signer-decisions", handler.RecordSignerDecision(handler.SignerDecider{
		Store: checkerDecisionStore, Reanalysis: reanalysisOrchestrator,
	}))
	checkerStatusStore, _ := caseStore.(handler.CheckerStatusStore)
	protected.With(auth.RequireAuth).Get("/v1/cases/{id}/checker-status", handler.GetCheckerStatus(checkerStatusStore))
	evidenceStore, _ := caseParticipantStore.(handler.EvidenceStore)
	protected.With(auth.RequireAuth).Get("/v1/cases/{id}/evidences", handler.ListCaseEvidences(evidenceStore))
	protected.With(auth.RequireAuth).Post("/v1/cases/{id}/evidences", handler.AddCaseEvidence(evidenceStore))
	protected.With(auth.RequireAuth).Post("/v1/cases/{id}/evidences/upload-url", handler.IssueEvidenceUploadURL(evidenceStore, fileStorage))
	protected.With(auth.RequireAuth).Post("/v1/cases/{id}/evidences/file", handler.RegisterFileEvidence(evidenceStore, fileStorage))
	protected.With(auth.RequireAuth).Post("/v1/cases/{id}/participants", handler.AssignCaseParticipant(caseParticipantStore))
	protected.With(auth.RequireAuth).Delete("/v1/cases/{id}/participants/{participant_id}", handler.UnassignCaseParticipant(caseParticipantStore))
	var userStore handler.UserStore
	if len(userStores) > 0 {
		userStore = userStores[0]
	}
	protected.With(auth.RequireAuth).Get("/v1/users", handler.ListUsers(userStore))
	protected.With(auth.RequireAdmin).Post("/v1/users", handler.CreateUser(userStore))
	protected.With(auth.RequireAdmin).Patch("/v1/users/{id}", handler.UpdateUser(userStore))
	// Catch-all: auth middleware must run even for undefined /api paths,
	// so register a wildcard route instead of relying on NotFound (which
	// bypasses middleware on mounted routers).
	protected.HandleFunc("/*", func(w http.ResponseWriter, r *http.Request) {
		httpapi.WriteError(w, httpapi.NewError(httpapi.CodeInvalidRequest, "not found", nil))
	})
	router.Mount("/api", protected)

	return &http.Server{
		Addr:              fmt.Sprintf(":%d", port),
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}, nil
}
