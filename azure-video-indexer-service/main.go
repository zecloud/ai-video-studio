package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	cfg, err := LoadConfig()
	if err != nil {
		logger.Error("configuration error", "error", redactURLsInText(err.Error()))
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	obs := newObservability(ctx, logger)
	defer func() {
		_ = obs.Shutdown(context.Background())
	}()

	blobSvc, err := NewAzureBlobServiceFromDefaultCredential(cfg)
	if err != nil {
		logger.Error("blob service error", "error", redactURLsInText(err.Error()))
		_ = obs.Shutdown(context.Background())
		os.Exit(1)
	}
	blobSvc.obs = obs

	viClient, err := NewManagedIdentityVideoIndexerClient(videoIndexerConfigFromConfig(cfg), nil)
	if err != nil {
		logger.Error("video indexer client error", "error", redactURLsInText(err.Error()))
		_ = obs.Shutdown(context.Background())
		os.Exit(1)
	}
	viClient.obs = obs

	planner, err := NewEditPlannerFromEnv(obs)
	if err != nil {
		logger.Error("edit planner error", "error", redactURLsInText(err.Error()))
		_ = obs.Shutdown(context.Background())
		os.Exit(1)
	}
	signalExtractor := NewMediaSignalExtractor(nil, MediaSignalConfig{})
	signalExtractor.obs = obs
	normalizer := NewVideoIndexNormalizer(signalExtractor)
	normalizer.obs = obs
	pipeline := NewAzureVideoIndexerPipeline(viClient, normalizer, planner, nil)
	pipeline.obs = obs
	oneDrive := NewOneDriveClient(cfg.GraphBaseURL, nil)
	oneDrive.obs = obs
	store := NewAzureJobStore(blobSvc.Client(), cfg.JobContainer)
	manager := NewJobManager(JobManagerConfig{QueueSize: cfg.QueueSize, WorkerConcurrency: cfg.WorkerConcurrency}, store, oneDrive, blobSvc, pipeline, nil)
	manager.obs = obs
	if err := manager.Start(ctx, cfg.WorkerConcurrency); err != nil {
		logger.Error("job manager error", "error", redactURLsInText(err.Error()))
		_ = obs.Shutdown(context.Background())
		os.Exit(1)
	}
	defer manager.Close()

	server := NewServer(cfg, manager)
	server.obs = obs
	server.blobSvc = blobSvc
	server.videoIndexer = viClient
	server.planner = planner
	server.readiness = newDefaultReadinessChecker(cfg, blobSvc, viClient, planner, exec.LookPath)
	httpServer := &http.Server{
		Addr:    cfg.ListenAddr,
		Handler: server.Handler(),
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = obs.Shutdown(shutdownCtx)
		_ = httpServer.Shutdown(shutdownCtx)
	}()

	logger.Info("azure-video-indexer-service listening", "listen_addr", cfg.ListenAddr, "telemetry_mode", obs.mode)
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Error("server exited", "error", redactURLsInText(err.Error()))
		_ = obs.Shutdown(context.Background())
		os.Exit(1)
	}
}

func videoIndexerConfigFromConfig(cfg Config) VideoIndexerConfig {
	return VideoIndexerConfig{
		SubscriptionID: cfg.VideoIndexerSubscriptionID,
		ResourceGroup:  cfg.VideoIndexerResourceGroup,
		AccountName:    cfg.VideoIndexerAccountName,
		AccountID:      cfg.VideoIndexerAccountID,
		Location:       cfg.VideoIndexerLocation,
		PollTimeout:    cfg.VideoIndexerTimeout,
	}
}
