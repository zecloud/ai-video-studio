package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
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
	defer func() { _ = obs.Shutdown(context.Background()) }()

	blobSvc, err := NewAzureBlobServiceFromDefaultCredential(cfg)
	if err != nil {
		logger.Error("blob service error", "error", redactURLsInText(err.Error()))
		os.Exit(1)
	}
	blobSvc.obs = obs

	store := NewAzureJobStore(blobSvc.Client(), cfg.JobContainer)
	var runErr error
	if cfg.ServiceRole == "worker" {
		viClient, err := NewManagedIdentityVideoIndexerClient(videoIndexerConfigFromConfig(cfg), cfg.ManagedIdentityClientID, nil)
		if err != nil {
			logger.Error("video indexer client error", "error", redactURLsInText(err.Error()))
			os.Exit(1)
		}
		viClient.obs = obs
		runErr = runWorker(ctx, logger, cfg, store, blobSvc, viClient, obs)
	} else {
		runErr = runAPI(ctx, logger, cfg, store, blobSvc, obs)
	}
	if runErr != nil {
		logger.Error("service exited with an error", "error", redactURLsInText(runErr.Error()))
		os.Exit(1)
	}
}

func runAPI(ctx context.Context, logger *slog.Logger, cfg Config, store JobStore, blobSvc *AzureBlobService, obs *Observability) error {
	runtime, err := NewDTSRuntime(ctx, cfg, nil)
	if err != nil {
		return fmt.Errorf("starting Durable Task Scheduler runtime: %w", err)
	}
	defer runtime.Close(context.Background())

	oneDrive := NewOneDriveClient(cfg.GraphBaseURL, nil)
	oneDrive.obs = obs
	jobs := NewDurableJobService(store, oneDrive, blobSvc, runtime, nil, cfg.DTSCancellationGrace)
	runtime.SetCancellationReconciler(jobs.ReconcileCancellation)
	startStagedJobReconciler(ctx, logger, jobs)
	server := NewServer(cfg, jobs)
	server.obs = obs
	server.blobSvc = blobSvc
	server.readiness = newAPIReadinessChecker(cfg, blobSvc)

	httpServer := &http.Server{Addr: cfg.ListenAddr, Handler: server.Handler()}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()
	logger.Info("azure-video-indexer API listening", "listen_addr", cfg.ListenAddr, "telemetry_mode", obs.mode)
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("serving HTTP API: %w", err)
	}
	return nil
}

func startStagedJobReconciler(ctx context.Context, logger *slog.Logger, jobs *DurableJobService) {
	reconcile := func() {
		if err := jobs.ReconcileStaged(ctx); err != nil && ctx.Err() == nil {
			logger.Error("reconciling staged durable jobs", "error", redactURLsInText(err.Error()))
		}
	}
	reconcile()
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				reconcile()
			}
		}
	}()
}
func runWorker(ctx context.Context, logger *slog.Logger, cfg Config, store JobStore, blobSvc *AzureBlobService, viClient *VideoIndexerClient, obs *Observability) error {
	planner, err := NewEditPlannerFromEnv(obs)
	if err != nil {
		return fmt.Errorf("creating edit planner: %w", err)
	}
	signalExtractor := NewMediaSignalExtractor(nil, MediaSignalConfig{})
	signalExtractor.obs = obs
	normalizer := NewVideoIndexNormalizer(signalExtractor)
	normalizer.obs = obs
	activities := NewVideoIndexerActivities(store, blobSvc, viClient, normalizer, planner, nil)
	registry, err := NewVideoIndexerTaskRegistry(activities)
	if err != nil {
		return fmt.Errorf("creating Durable Task Scheduler registry: %w", err)
	}
	runtime, err := NewDTSRuntime(ctx, cfg, registry)
	if err != nil {
		return fmt.Errorf("starting Durable Task Scheduler runtime: %w", err)
	}
	cancellationProjection := NewDurableJobService(store, nil, blobSvc, nil, nil, cfg.DTSCancellationGrace)
	runtime.SetCancellationReconciler(cancellationProjection.ReconcileCancellation)
	activities.SetCancellationTerminator(runtime.ForceTerminateAndReconcile)
	defer runtime.Close(context.Background())
	logger.Info("azure-video-indexer durable worker started", "task_hub", cfg.DTSTaskHub, "telemetry_mode", obs.mode)
	<-ctx.Done()
	return nil
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
