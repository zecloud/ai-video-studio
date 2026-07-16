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

	var runErr error
	switch cfg.ServiceRole {
	case "worker":
		store := NewAzureJobStore(blobSvc.Client(), cfg.JobContainer)
		viClient, err := NewManagedIdentityVideoIndexerClient(videoIndexerConfigFromConfig(cfg), cfg.ManagedIdentityClientID, nil)
		if err != nil {
			logger.Error("video indexer client error", "error", redactURLsInText(err.Error()))
			os.Exit(1)
		}
		viClient.obs = obs
		runErr = runWorker(ctx, logger, cfg, store, blobSvc, viClient, obs)
	case "ffmpeg-worker":
		renderStore := NewAzureRenderJobStore(blobSvc.Client(), cfg.JobContainer)
		runErr = runFFmpegWorker(ctx, logger, cfg, renderStore, blobSvc, obs)
	default:
		store := NewAzureJobStore(blobSvc.Client(), cfg.JobContainer)
		runErr = runAPI(ctx, logger, cfg, store, blobSvc, obs)
	}
	if runErr != nil {
		logger.Error("service exited with an error", "error", redactURLsInText(runErr.Error()))
		os.Exit(1)
	}
}

func runAPI(ctx context.Context, logger *slog.Logger, cfg Config, store JobStore, blobSvc *AzureBlobService, obs *Observability) error {
	indexRuntime, err := NewDTSRuntimeForHub(ctx, cfg, cfg.DTSTaskHub, nil)
	if err != nil {
		return fmt.Errorf("starting indexing Durable Task Scheduler runtime: %w", err)
	}
	defer indexRuntime.Close(context.Background())
	renderRuntime, err := NewDTSRuntimeForHub(ctx, cfg, cfg.DTSRenderTaskHub, nil)
	if err != nil {
		return fmt.Errorf("starting render Durable Task Scheduler runtime: %w", err)
	}
	defer renderRuntime.Close(context.Background())

	oneDrive := NewOneDriveClient(cfg.GraphBaseURL, nil)
	oneDrive.obs = obs
	jobs := NewDurableJobService(store, oneDrive, blobSvc, indexRuntime, nil, cfg.DTSCancellationGrace)
	renderStore := NewAzureRenderJobStore(blobSvc.Client(), cfg.JobContainer)
	renderJobs := NewDurableRenderJobService(renderStore, oneDrive, blobSvc, renderRuntime, nil)
	indexRuntime.SetCancellationReconciler(jobs.ReconcileCancellation)
	startStagedJobReconciler(ctx, logger, jobs)
	startQueuedRenderReconciler(ctx, logger, renderJobs)
	server := NewServer(cfg, jobs)
	if planner, plannerErr := NewEditPlannerFromEnv(obs); plannerErr == nil {
		server.SetNarrativeRanker(narrativeRanker{planner: planner, max: cfg.NarrativeRankingMaxCandidates, maxSources: cfg.NarrativeRankingMaxSources, timeout: cfg.NarrativeRankingTimeout, obs: obs})
	} else {
		logger.Warn("narrative ranking unavailable", "error", redactURLsInText(plannerErr.Error()))
	}
	if classifier, classifierErr := newNarrativeIntentClassifier(editPlannerConfig{
		FoundryEndpoint: getRequiredEditPlannerEnv("FOUNDRY_PROJECT_ENDPOINT", "FOUNDRY_ENDPOINT", "AZURE_FOUNDRY_ENDPOINT"),
		ModelDeployment: getOptionalEditPlannerEnv("FOUNDRY_DEPLOYMENT_NAME", "AZURE_FOUNDRY_DEPLOYMENT_NAME"),
	}, cfg.NarrativeIntentClassifierTimeout, obs); classifierErr == nil {
		server.SetNarrativeIntentClassifier(classifier)
	} else {
		logger.Warn("narrative intent classifier unavailable", "error", redactURLsInText(classifierErr.Error()))
	}
	server.SetRenderJobs(renderJobs)
	server.obs = obs
	server.blobSvc = blobSvc
	server.SetRenderOutputStreamer(blobSvc)
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
func startQueuedRenderReconciler(ctx context.Context, logger *slog.Logger, jobs *DurableRenderJobService) {
	reconcile := func() {
		if err := jobs.ReconcileQueuedRenders(ctx); err != nil && ctx.Err() == nil {
			logger.Error("reconciling queued render jobs", "error", redactURLsInText(err.Error()))
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

func runFFmpegWorker(ctx context.Context, logger *slog.Logger, cfg Config, store RenderJobStore, blobSvc *AzureBlobService, obs *Observability) error {
	activities := NewFFmpegRenderActivities(store, blobSvc, cfg, nil)
	registry, err := NewFFmpegRenderTaskRegistry(activities)
	if err != nil {
		return fmt.Errorf("creating FFmpeg Durable Task Scheduler registry: %w", err)
	}
	runtime, err := NewDTSRuntime(ctx, cfg, registry)
	if err != nil {
		return fmt.Errorf("starting Durable Task Scheduler runtime: %w", err)
	}
	activities.SetCancellationTerminator(runtime.ForceTerminate)
	defer runtime.Close(context.Background())
	logger.Info("FFmpeg durable worker started", "task_hub", cfg.DTSRenderTaskHub, "telemetry_mode", obs.mode)
	<-ctx.Done()
	return nil
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
