package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/microsoft/durabletask-go/api"
	"github.com/microsoft/durabletask-go/backend"
	"github.com/microsoft/durabletask-go/backend/durabletaskscheduler"
	"github.com/microsoft/durabletask-go/client"
	"github.com/microsoft/durabletask-go/task"
)

const cancellationEventName = "video-indexer-cancel"

const renderCancellationEventName = "ffmpeg-render-cancel"

// DTSRuntime owns the experimental DTS backend behind the API/worker boundary.
// The dependency is pinned to the immutable PR #122 commit in go.mod.
type DTSRuntime struct {
	backend               *durabletaskscheduler.Backend
	client                *client.TaskHubGrpcClient
	cancellationGrace     time.Duration
	reconcileCancellation func(context.Context, string) error
	listenerStarted       bool
	mu                    sync.Mutex
}

func NewDTSRuntime(ctx context.Context, cfg Config, registry *task.TaskRegistry) (*DTSRuntime, error) {
	taskHub := cfg.DTSTaskHub
	if cfg.Normalize().ServiceRole == "ffmpeg-worker" {
		taskHub = cfg.DTSRenderTaskHub
	}
	return NewDTSRuntimeForHub(ctx, cfg, taskHub, registry)
}

func NewDTSRuntimeForHub(ctx context.Context, cfg Config, taskHub string, registry *task.TaskRegistry) (*DTSRuntime, error) {
	cfg = cfg.Normalize()
	taskHub = strings.TrimSpace(taskHub)
	if taskHub == "" {
		return nil, fmt.Errorf("Durable Task Scheduler task hub is required")
	}
	opts := durabletaskscheduler.NewOptions(cfg.DTSEndpoint, taskHub)
	be := durabletaskscheduler.NewBackend(opts, backend.DefaultLogger())
	if err := be.Start(ctx); err != nil {
		return nil, fmt.Errorf("starting Durable Task Scheduler backend: %w", err)
	}
	conn, err := be.Connection()
	if err != nil {
		_ = be.Stop(context.Background())
		return nil, fmt.Errorf("opening Durable Task Scheduler connection: %w", err)
	}
	runtime := &DTSRuntime{
		backend:           be,
		client:            client.NewTaskHubGrpcClient(conn, backend.DefaultLogger()),
		cancellationGrace: cfg.DTSCancellationGrace,
	}
	if registry != nil {
		if err := runtime.client.StartWorkItemListener(ctx, registry); err != nil {
			_ = be.Stop(context.Background())
			return nil, fmt.Errorf("starting Durable Task Scheduler listener: %w", err)
		}
		runtime.listenerStarted = true
	}
	return runtime, nil
}

func orchestrationIDReusePolicy() *api.OrchestrationIdReusePolicy {
	return &api.OrchestrationIdReusePolicy{
		Action: api.REUSE_ID_ACTION_IGNORE,
		OperationStatus: []api.OrchestrationStatus{
			api.RUNTIME_STATUS_PENDING,
			api.RUNTIME_STATUS_RUNNING,
			api.RUNTIME_STATUS_TERMINATED,
		},
	}
}

func (r *DTSRuntime) Schedule(ctx context.Context, input VideoIndexerOrchestrationInput) error {
	if r == nil || r.client == nil {
		return fmt.Errorf("Durable Task Scheduler runtime is not configured")
	}
	reusePolicy := orchestrationIDReusePolicy()
	_, err := r.client.ScheduleNewOrchestration(ctx, videoIndexerOrchestrationName,
		api.WithInstanceID(api.InstanceID(input.JobID)),
		api.WithInput(input),
		api.WithOrchestrationIdReusePolicy(reusePolicy),
	)
	if errors.Is(err, api.ErrIgnoreInstance) {
		return nil
	}

	return err
}

func (r *DTSRuntime) ScheduleRender(ctx context.Context, input FFmpegRenderOrchestrationInput) error {
	if r == nil || r.client == nil {
		return fmt.Errorf("Durable Task Scheduler runtime is not configured")
	}
	_, err := r.client.ScheduleNewOrchestration(ctx, ffmpegRenderOrchestrationName,
		api.WithInstanceID(api.InstanceID(input.JobID)), api.WithInput(input),
		api.WithOrchestrationIdReusePolicy(orchestrationIDReusePolicy()))
	if errors.Is(err, api.ErrIgnoreInstance) {
		return nil
	}
	return err
}

// SetCancellationReconciler converges the public Blob projection when forced
// termination prevents the orchestration from running its compensation activity.
func (r *DTSRuntime) SetCancellationReconciler(reconciler func(context.Context, string) error) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reconcileCancellation = reconciler
}
func scheduleThenRaiseCancellation(schedule func() error, raise func() error, scheduleMessage, raiseMessage string) error {
	if err := schedule(); err != nil && !errors.Is(err, api.ErrIgnoreInstance) {
		return fmt.Errorf("%s: %w", scheduleMessage, err)
	}
	if err := raise(); err != nil {
		return fmt.Errorf("%s: %w", raiseMessage, err)
	}
	return nil
}

func (r *DTSRuntime) RequestCancellation(ctx context.Context, jobID string) error {
	if r == nil || r.client == nil {
		return fmt.Errorf("Durable Task Scheduler runtime is not configured")
	}
	input := VideoIndexerOrchestrationInput{JobID: jobID, Version: videoIndexerOrchestrationVersion, CancellationGrace: r.cancellationGrace}
	return scheduleThenRaiseCancellation(func() error {
		_, err := r.client.ScheduleNewOrchestration(ctx, cancellationWatchdogName,
			api.WithInstanceID(api.InstanceID(jobID+".cancel")), api.WithInput(input),
			api.WithOrchestrationIdReusePolicy(orchestrationIDReusePolicy()))
		return err
	}, func() error {
		return r.client.RaiseEvent(ctx, api.InstanceID(jobID), cancellationEventName)
	}, "scheduling durable cancellation watchdog", "raising cancellation event")
}

func (r *DTSRuntime) RequestRenderCancellation(ctx context.Context, jobID string) error {
	if r == nil || r.client == nil {
		return fmt.Errorf("Durable Task Scheduler runtime is not configured")
	}
	input := FFmpegRenderOrchestrationInput{JobID: jobID, Version: ffmpegRenderOrchestrationVersion, CancellationGrace: r.cancellationGrace}
	return scheduleThenRaiseCancellation(func() error {
		_, err := r.client.ScheduleNewOrchestration(ctx, ffmpegRenderCancellationWatchdogName,
			api.WithInstanceID(api.InstanceID(jobID+".render-cancel")), api.WithInput(input),
			api.WithOrchestrationIdReusePolicy(orchestrationIDReusePolicy()))
		return err
	}, func() error {
		return r.client.RaiseEvent(ctx, api.InstanceID(jobID), renderCancellationEventName)
	}, "scheduling render cancellation watchdog", "raising render cancellation event")
}

// ForceTerminateAndReconcile runs in a worker activity scheduled by the durable
// watchdog, so the grace period survives API restarts and scale-to-zero.
func (r *DTSRuntime) ForceTerminateAndReconcile(ctx context.Context, jobID string) error {
	r.mu.Lock()
	client := r.client
	reconciler := r.reconcileCancellation
	r.mu.Unlock()
	if client == nil {
		return fmt.Errorf("Durable Task Scheduler runtime is not configured")
	}

	metadata, err := client.FetchOrchestrationMetadata(ctx, api.InstanceID(jobID))
	if err != nil {
		return fmt.Errorf("fetching orchestration metadata: %w", err)
	}

	if !metadata.IsComplete() {
		if err := client.TerminateOrchestration(ctx, api.InstanceID(jobID)); err != nil {
			return fmt.Errorf("terminating orchestration: %w", err)
		}
	}
	if reconciler != nil {
		if err := reconciler(ctx, jobID); err != nil {
			return fmt.Errorf("reconciling cancellation: %w", err)
		}
	}
	return nil
}

func (r *DTSRuntime) ForceTerminate(ctx context.Context, jobID string) error {
	r.mu.Lock()
	client := r.client
	r.mu.Unlock()
	if client == nil {
		return fmt.Errorf("Durable Task Scheduler runtime is not configured")
	}
	metadata, err := client.FetchOrchestrationMetadata(ctx, api.InstanceID(jobID))
	if err != nil {
		return fmt.Errorf("fetching orchestration metadata: %w", err)
	}
	if !metadata.IsComplete() {
		if err := client.TerminateOrchestration(ctx, api.InstanceID(jobID)); err != nil {
			return fmt.Errorf("terminating orchestration: %w", err)
		}
	}
	return nil
}
func (r *DTSRuntime) Close(ctx context.Context) error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var listenerErr error
	if r.client != nil {
		if r.listenerStarted {
			listenerErr = r.client.StopWorkItemListener(ctx)
		}
		r.listenerStarted = false
		r.client = nil
	}
	if r.backend != nil {
		backendErr := r.backend.Stop(ctx)
		r.backend = nil
		if listenerErr != nil {
			return listenerErr
		}
		return backendErr
	}
	return listenerErr
}

var _ OrchestrationScheduler = (*DTSRuntime)(nil)
var _ RenderOrchestrationScheduler = (*DTSRuntime)(nil)
