package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jawir-team/jawir-sentinel-be/internal/aiconsumer"
	"github.com/jawir-team/jawir-sentinel-be/internal/aiworker"
	"github.com/jawir-team/jawir-sentinel-be/internal/config"
	"github.com/jawir-team/jawir-sentinel-be/internal/database"
	"github.com/jawir-team/jawir-sentinel-be/internal/logging"
	"github.com/jawir-team/jawir-sentinel-be/internal/outboxdispatch"
	"github.com/jawir-team/jawir-sentinel-be/internal/rabbitmq"
	"github.com/jawir-team/jawir-sentinel-be/internal/vertexai"
)

func main() {
	logger := logging.New(os.Stdout, slog.LevelInfo)
	slog.SetDefault(logger)
	if err := run(); err != nil {
		logger.Error("sentinel-worker stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.LoadWorker()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	startupCtx, cancelStartup := context.WithTimeout(ctx, 10*time.Second)
	defer cancelStartup()

	pool, err := database.Open(startupCtx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := database.Migrate(startupCtx, pool); err != nil {
		return err
	}

	broker, err := rabbitmq.Dial(startupCtx, cfg.RabbitMQURL, cfg.RabbitMQAIQueue)
	if err != nil {
		return err
	}
	defer broker.Close()

	dispatcher := outboxdispatch.New(pool, broker)

	// Vertex orchestration seam: replace the nil argument below with an
	// aiconsumer.Executor whose Execute method performs exactly this existing
	// pipeline:
	//
	//   ai.NewBuilder(...).Build -> ai.RenderPrompt ->
	//   vertexai.Client.GenerateContent -> ai.ParseAndValidateCandidate ->
	//   ai.VerifyCandidate
	//
	// Execute returns SuccessResult or FailedCandidate but performs no writes;
	// Processor below then calls the fenced aiworker Finalize*/Technical*
	// methods. Until that adapter is supplied, deliveries are NACKed before a
	// database claim is created, while the outbox dispatcher remains active.
	var executor aiconsumer.Executor
	processor := aiconsumer.New(aiworker.NewWithLeaseSeconds(pool, cfg.AIWorkerLeaseSeconds), executor, technicalMaxRetries())

	workerCtx, cancelWorker := context.WithCancel(ctx)
	defer cancelWorker()
	errCh := make(chan error, 2)
	go func() { errCh <- dispatcher.Run(workerCtx) }()
	go func() { errCh <- broker.Consume(workerCtx, processor.Handle) }()

	slog.Info("sentinel-worker running", "component", "worker", "rabbitmq_queue", cfg.RabbitMQAIQueue)
	select {
	case <-ctx.Done():
		cancelWorker()
		_ = broker.Close()
		return nil
	case err := <-errCh:
		cancelWorker()
		_ = broker.Close()
		if err == nil || errors.Is(err, context.Canceled) {
			return nil
		}
		return err
	}
}

func technicalMaxRetries() int {
	raw := strings.TrimSpace(os.Getenv("AI_TECHNICAL_MAX_RETRIES"))
	if raw == "" {
		return vertexai.DefaultTechnicalMaxRetries
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return vertexai.DefaultTechnicalMaxRetries
	}
	if value < 0 {
		return 0
	}
	if value > vertexai.MaxTechnicalMaxRetries {
		return vertexai.MaxTechnicalMaxRetries
	}
	return value
}
