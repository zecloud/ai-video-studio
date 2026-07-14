package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/microsoft/durabletask-go/task"
)

const (
	ffmpegRenderPrepareActivity          = "ffmpegRenderPrepare"
	ffmpegRenderExecuteActivity          = "ffmpegRenderExecute"
	ffmpegRenderCompleteActivity         = "ffmpegRenderComplete"
	ffmpegRenderFailActivity             = "ffmpegRenderFail"
	ffmpegRenderCancelActivity           = "ffmpegRenderCancel"
	ffmpegRenderForceCancelActivity      = "ffmpegRenderForceCancel"
	ffmpegRenderCancellationWatchdogName = "ffmpeg-render-cancellation-watchdog"
	defaultRenderCancellationPoll        = 500 * time.Millisecond
)

func validateRenderExecutionInput(input FFmpegRenderOrchestrationInput) error {
	if err := validateID(input.JobID, "jobId"); err != nil {
		return err
	}
	if input.Version != ffmpegRenderOrchestrationVersion {
		return fmt.Errorf("unsupported render orchestration version %q", input.Version)
	}
	if len(input.Clips) == 0 || len(input.Clips) > 64 {
		return fmt.Errorf("render orchestration requires between 1 and 64 clips")
	}
	if input.Preset != "h264-1080p" && input.Preset != "h264-720p" && input.Preset != "h265-1080p" {
		return fmt.Errorf("render orchestration preset is unsupported")
	}
	if input.Output.Container == "" || input.Output.BlobName == "" {
		return fmt.Errorf("render orchestration output is incomplete")
	}
	if len(input.Transitions) > 0 && len(input.Transitions) != len(input.Clips)-1 {
		return fmt.Errorf("render orchestration transitions are incomplete")
	}
	for _, clip := range input.Clips {
		if clip.ID == "" || clip.Container == "" || clip.BlobName == "" || clip.InMS < 0 || clip.OutMS <= clip.InMS {
			return fmt.Errorf("render orchestration clip reference is invalid")
		}
	}
	for _, transition := range input.Transitions {
		if transition.Kind != "cut" || transition.DurationMS != 0 {
			return fmt.Errorf("render orchestration supports only zero-duration cuts")
		}
	}
	return nil
}

type RenderExecutionBlobs interface {
	RenderBlobStager
	DownloadToFile(context.Context, StagedAsset, string) error
	UploadFile(context.Context, StagedAsset, string, string) (int64, error)
}

type FFmpegRenderActivities struct {
	store                    RenderJobStore
	blobs                    RenderExecutionBlobs
	ffmpegPath               string
	workspaceRoot            string
	renderTimeout            time.Duration
	cancellationPollInterval time.Duration
	clock                    Clock
	forceTerminate           func(context.Context, string) error
}

func NewFFmpegRenderActivities(store RenderJobStore, blobs RenderExecutionBlobs, cfg Config, clock Clock) *FFmpegRenderActivities {
	cfg = cfg.Normalize()
	if clock == nil {
		clock = realClock{}
	}
	return &FFmpegRenderActivities{
		store: store, blobs: blobs, ffmpegPath: cfg.FFmpegPath, workspaceRoot: cfg.RenderWorkspaceRoot,
		renderTimeout: cfg.RenderTimeout, cancellationPollInterval: defaultRenderCancellationPoll, clock: clock,
	}
}

func (a *FFmpegRenderActivities) SetCancellationTerminator(terminator func(context.Context, string) error) {
	a.forceTerminate = terminator
}

func (a *FFmpegRenderActivities) input(ctx task.ActivityContext) (FFmpegRenderOrchestrationInput, error) {
	var input FFmpegRenderOrchestrationInput
	if err := ctx.GetInput(&input); err != nil {
		return input, err
	}
	if err := validateID(input.JobID, "jobId"); err != nil {
		return input, err
	}
	return input, nil
}

func (a *FFmpegRenderActivities) service() *DurableRenderJobService {
	return &DurableRenderJobService{store: a.store, stager: a.blobs, clock: a.clock}
}

func (a *FFmpegRenderActivities) update(ctx context.Context, id string, status RenderJobStatus, mutate func(*RenderJobDocument)) (StoredRenderJob, error) {
	return a.service().transition(ctx, id, status, mutate)
}

func (a *FFmpegRenderActivities) Prepare(ctx task.ActivityContext) (any, error) {
	input, err := a.input(ctx)
	if err != nil {
		return nil, err
	}
	current, err := a.store.Get(ctx.Context(), input.JobID)
	if err != nil {
		return nil, err
	}
	if current.CancellationRequestedAt != nil || current.Status == RenderJobStatusCanceled {
		_, finishErr := a.finishByID(ctx.Context(), input.JobID, RenderJobStatusCanceled, "canceled", "render job canceled")
		return nil, errors.Join(errRenderCancellationRequested, finishErr)
	}
	updated, err := a.update(ctx.Context(), input.JobID, RenderJobStatusRendering, nil)
	if err != nil {
		return nil, err
	}
	if updated.Status != RenderJobStatusRendering || updated.CancellationRequestedAt != nil {
		return nil, errRenderCancellationRequested
	}
	return nil, nil
}

func (a *FFmpegRenderActivities) Execute(ctx task.ActivityContext) (any, error) {
	input, err := a.input(ctx)
	if err != nil {
		return nil, err
	}
	if err := validateRenderExecutionInput(input); err != nil {
		return nil, err
	}
	if a.blobs == nil {
		return nil, newServiceError(503, "render_blob_store_unavailable", "render blob store is not configured", true)
	}
	current, err := a.store.Get(ctx.Context(), input.JobID)
	if err != nil {
		return nil, err
	}
	if current.CancellationRequestedAt != nil || current.Status == RenderJobStatusCanceled {
		return nil, a.cancelExecution(input)
	}
	current, err = a.update(ctx.Context(), input.JobID, RenderJobStatusRendering, func(doc *RenderJobDocument) {
		doc.ExecutionActive = true
	})
	if err != nil {
		return nil, err
	}
	if current.CancellationRequestedAt != nil || current.Status != RenderJobStatusRendering || !current.ExecutionActive {
		return nil, a.cancelExecution(input)
	}

	renderCtx, cancelRender := context.WithTimeout(ctx.Context(), a.renderTimeout)
	defer cancelRender()
	requested := &atomic.Bool{}
	monitorErr := make(chan error, 1)
	stopMonitor := a.watchCancellation(ctx.Context(), input.JobID, requested, cancelRender, monitorErr)
	defer stopMonitor()

	workspace, err := os.MkdirTemp(a.workspaceRoot, "render-"+input.JobID+"-")
	if err != nil {
		return nil, fmt.Errorf("creating render workspace: %w", err)
	}
	defer os.RemoveAll(workspace)

	segments := make([]string, 0, len(input.Clips))
	for i, clip := range input.Clips {
		inputPath := filepath.Join(workspace, fmt.Sprintf("input-%02d.media", i))
		if err := a.blobs.DownloadToFile(renderCtx, StagedAsset{Container: clip.Container, BlobName: clip.BlobName}, inputPath); err != nil {
			return nil, a.executionError(input, requested, monitorErr, err)
		}
		segmentPath := filepath.Join(workspace, fmt.Sprintf("segment-%02d.mp4", i))
		if err := a.runFFmpeg(renderCtx, ffmpegSegmentArgs(inputPath, segmentPath, clip, input.Preset)); err != nil {
			return nil, a.executionError(input, requested, monitorErr, err)
		}
		segments = append(segments, segmentPath)
	}

	listPath := filepath.Join(workspace, "segments.txt")
	var list bytes.Buffer
	for _, segment := range segments {
		ffmpegPath := filepath.ToSlash(segment)
		fmt.Fprintf(&list, "file '%s'\n", strings.ReplaceAll(ffmpegPath, "'", "'\\''"))
	}
	if err := os.WriteFile(listPath, list.Bytes(), 0o600); err != nil {
		return nil, fmt.Errorf("writing render concat list: %w", err)
	}
	outputPath := filepath.Join(workspace, "output.mp4")
	if err := a.runFFmpeg(renderCtx, []string{"-hide_banner", "-loglevel", "error", "-y", "-f", "concat", "-safe", "0", "-i", listPath, "-c", "copy", "-movflags", "+faststart", outputPath}); err != nil {
		return nil, a.executionError(input, requested, monitorErr, err)
	}
	if requested.Load() {
		return nil, a.cancelExecution(input)
	}
	updated, err := a.update(renderCtx, input.JobID, RenderJobStatusUploading, nil)
	if err != nil {
		return nil, a.executionError(input, requested, monitorErr, err)
	}
	if updated.Status != RenderJobStatusUploading || updated.CancellationRequestedAt != nil {
		return nil, a.cancelExecution(input)
	}

	size, err := a.blobs.UploadFile(renderCtx, StagedAsset{Container: input.Output.Container, BlobName: input.Output.BlobName}, outputPath, "video/mp4")
	if err != nil {
		return nil, a.executionError(input, requested, monitorErr, err)
	}
	latest, err := a.store.Get(renderCtx, input.JobID)
	if err != nil {
		return nil, err
	}
	if requested.Load() || latest.CancellationRequestedAt != nil || latest.Status == RenderJobStatusCanceled {
		return nil, a.cancelExecution(input)
	}
	updated, err = a.update(renderCtx, input.JobID, RenderJobStatusUploading, func(doc *RenderJobDocument) {
		doc.Output = &RenderOutput{Container: input.Output.Container, BlobName: input.Output.BlobName, Size: size, MediaType: "video/mp4"}
		doc.ExecutionActive = false
	})
	if err != nil {
		return nil, err
	}
	if updated.CancellationRequestedAt != nil || updated.Status != RenderJobStatusUploading {
		return nil, a.cancelExecution(input)
	}
	return nil, nil
}

func (a *FFmpegRenderActivities) watchCancellation(parent context.Context, jobID string, requested *atomic.Bool, cancelWork context.CancelFunc, errCh chan<- error) func() {
	monitorCtx, stop := context.WithCancel(parent)
	interval := a.cancellationPollInterval
	if interval <= 0 {
		interval = defaultRenderCancellationPoll
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-monitorCtx.Done():
				return
			case <-ticker.C:
				job, err := a.store.Get(monitorCtx, jobID)
				if err != nil {
					if monitorCtx.Err() == nil {
						select {
						case errCh <- err:
						default:
						}
						cancelWork()
					}
					return
				}
				if job.CancellationRequestedAt != nil || job.Status == RenderJobStatusCanceled {
					requested.Store(true)
					cancelWork()
					return
				}
			}
		}
	}()
	return func() {
		stop()
		wg.Wait()
	}
}

func (a *FFmpegRenderActivities) executionError(input FFmpegRenderOrchestrationInput, requested *atomic.Bool, monitorErr <-chan error, operationErr error) error {
	if requested.Load() {
		return errors.Join(errRenderCancellationRequested, a.cancelExecution(input))
	}
	select {
	case err := <-monitorErr:
		return err
	default:
		return operationErr
	}
}

func (a *FFmpegRenderActivities) cancelExecution(input FFmpegRenderOrchestrationInput) error {
	ctx, cancel := context.WithTimeout(context.Background(), renderProjectionTimeout)
	defer cancel()
	_, err := a.finishByID(ctx, input.JobID, RenderJobStatusCanceled, "canceled", "render job canceled")
	return errors.Join(errRenderCancellationRequested, err)
}

func (a *FFmpegRenderActivities) Complete(ctx task.ActivityContext) (any, error) {
	return a.finish(ctx, RenderJobStatusSucceeded, "", "")
}

func (a *FFmpegRenderActivities) Fail(ctx task.ActivityContext) (any, error) {
	return a.finish(ctx, RenderJobStatusFailed, "render_failed", "FFmpeg render failed")
}

func (a *FFmpegRenderActivities) Cancel(ctx task.ActivityContext) (any, error) {
	input, err := a.input(ctx)
	if err != nil {
		return nil, err
	}
	return nil, a.cancelByID(ctx.Context(), input.JobID)
}

func (a *FFmpegRenderActivities) cancelByID(ctx context.Context, jobID string) error {
	job, err := a.store.Get(ctx, jobID)
	if err != nil {
		return err
	}
	if job.Status == RenderJobStatusSucceeded || job.Status == RenderJobStatusFailed {
		return nil
	}
	if job.ExecutionActive {
		return newServiceError(http.StatusConflict, "render_execution_active", "active FFmpeg execution is still processing its durable cancellation request", true)
	}
	_, err = a.finishByID(ctx, jobID, RenderJobStatusCanceled, "canceled", "render job canceled")
	return err
}

func (a *FFmpegRenderActivities) finish(ctx task.ActivityContext, status RenderJobStatus, code, message string) (any, error) {
	input, err := a.input(ctx)
	if err != nil {
		return nil, err
	}
	_, err = a.finishByID(ctx.Context(), input.JobID, status, code, message)
	return nil, err
}

func (a *FFmpegRenderActivities) finishByID(ctx context.Context, id string, status RenderJobStatus, code, message string) (StoredRenderJob, error) {
	job, err := a.store.Get(ctx, id)
	if err != nil {
		return StoredRenderJob{}, err
	}
	if job.CancellationRequestedAt != nil && status != RenderJobStatusCanceled {
		status, code, message = RenderJobStatusCanceled, "canceled", "render job canceled"
	}
	if status != RenderJobStatusSucceeded {
		job, err = a.service().mutate(ctx, id, func(doc *RenderJobDocument) { doc.ExecutionActive = false })
		if err != nil {
			return StoredRenderJob{}, err
		}
		if err := a.deleteRenderOutput(ctx, job); err != nil {
			return job, err
		}
	}
	stored, err := a.update(ctx, id, status, func(doc *RenderJobDocument) {
		doc.ExecutionActive = false
		if code != "" {
			doc.Error = &APIErrorResponse{Code: code, Message: message, Retryable: false}
		} else {
			doc.Error = nil
		}
		doc.InputsCleanupPending = len(doc.Clips) > 0
	})
	if err != nil {
		return StoredRenderJob{}, err
	}
	if stored.CancellationRequestedAt != nil && stored.Status != RenderJobStatusCanceled {
		if err := a.deleteRenderOutput(ctx, stored); err != nil {
			return stored, err
		}
		stored, err = a.update(ctx, id, RenderJobStatusCanceled, func(doc *RenderJobDocument) {
			doc.ExecutionActive = false
			doc.Error = &APIErrorResponse{Code: "canceled", Message: "render job canceled", Retryable: false}
			doc.InputsCleanupPending = len(doc.Clips) > 0
		})
		if err != nil {
			return StoredRenderJob{}, err
		}
	}
	cleaned, cleanupErr := a.service().cleanupAndRecord(ctx, id)
	return cleaned, cleanupErr
}

func (a *FFmpegRenderActivities) deleteRenderOutput(ctx context.Context, job StoredRenderJob) error {
	if job.Output == nil {
		return nil
	}
	if a.blobs == nil {
		return newServiceError(http.StatusServiceUnavailable, "render_blob_store_unavailable", "render blob store is not configured", true)
	}
	if err := a.blobs.Delete(ctx, StagedAsset{Container: job.Output.Container, BlobName: job.Output.BlobName}); err != nil {
		return fmt.Errorf("deleting canceled or failed render output: %w", err)
	}
	return nil
}

func (a *FFmpegRenderActivities) ForceCancel(ctx task.ActivityContext) (any, error) {
	input, err := a.input(ctx)
	if err != nil {
		return nil, err
	}
	return nil, a.forceCancelByID(ctx.Context(), input.JobID)
}

func (a *FFmpegRenderActivities) forceCancelByID(ctx context.Context, jobID string) error {
	job, err := a.store.Get(ctx, jobID)
	if err != nil {
		return err
	}
	if job.Status.Terminal() {
		return nil
	}
	if job.ExecutionActive {
		return newServiceError(http.StatusConflict, "render_execution_active", "active FFmpeg execution is still processing its durable cancellation request", true)
	}
	if a.forceTerminate == nil {
		return newServiceError(http.StatusServiceUnavailable, "cancellation_terminator_unavailable", "render cancellation terminator is not configured", true)
	}
	if err := a.forceTerminate(ctx, jobID); err != nil {
		return err
	}
	_, err = a.finishByID(ctx, jobID, RenderJobStatusCanceled, "canceled", "render job canceled")
	return err
}

func (a *FFmpegRenderActivities) runFFmpeg(ctx context.Context, args []string) error {
	var output ffmpegLogBuffer
	command := exec.CommandContext(ctx, a.ffmpegPath, args...)
	command.Stdout = &output
	command.Stderr = &output
	err := command.Run()
	if ctx.Err() != nil {
		return fmt.Errorf("FFmpeg render timed out or was canceled: %w", ctx.Err())
	}
	if err != nil {
		return fmt.Errorf("FFmpeg render failed: %w: %s", err, boundedFFmpegLog(output.Bytes()))
	}
	return nil
}

type ffmpegLogBuffer struct {
	bytes.Buffer
}

func (b *ffmpegLogBuffer) Write(p []byte) (int, error) {
	const captureLimit = 8192
	written := len(p)
	remaining := captureLimit - b.Len()
	if remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		_, _ = b.Buffer.Write(p)
	}
	return written, nil
}

func ffmpegSegmentArgs(input, output string, clip StagedRenderClip, preset string) []string {
	codec, width, height := "libx264", 1920, 1080
	if preset == "h264-720p" {
		width, height = 1280, 720
	} else if preset == "h265-1080p" {
		codec = "libx265"
	}
	filter := fmt.Sprintf("fps=30,scale=%d:%d:force_original_aspect_ratio=decrease,pad=%d:%d:(ow-iw)/2:(oh-ih)/2", width, height, width, height)
	duration := float64(clip.OutMS-clip.InMS) / 1000
	return []string{"-hide_banner", "-loglevel", "error", "-y", "-ss", strconv.FormatFloat(float64(clip.InMS)/1000, 'f', 3, 64), "-t", strconv.FormatFloat(duration, 'f', 3, 64), "-i", input, "-map", "0:v:0", "-an", "-vf", filter, "-c:v", codec, "-pix_fmt", "yuv420p", "-avoid_negative_ts", "make_zero", output}
}

func boundedFFmpegLog(output []byte) string {
	const limit = 4096
	text := strings.TrimSpace(redactURLsInText(string(output)))
	if len(text) > limit {
		return text[:limit] + "..."
	}
	return text
}

var errRenderCancellationRequested = errors.New("FFmpeg render cancellation requested")

func FFmpegRenderOrchestrator(ctx *task.OrchestrationContext) (any, error) {
	var input FFmpegRenderOrchestrationInput
	if err := ctx.GetInput(&input); err != nil {
		return nil, err
	}
	if err := validateRenderExecutionInput(input); err != nil {
		return nil, err
	}
	for _, activity := range []string{ffmpegRenderPrepareActivity, ffmpegRenderExecuteActivity, ffmpegRenderCompleteActivity} {
		if err := callFFmpegRenderActivity(ctx, activity, input); err != nil {
			if errors.Is(err, errRenderCancellationRequested) {
				return finishRenderOrchestration(ctx, ffmpegRenderCancelActivity, input, RenderJobStatusCanceled)
			}
			return finishRenderOrchestration(ctx, ffmpegRenderFailActivity, input, RenderJobStatusFailed)
		}
	}
	return map[string]string{"jobId": input.JobID, "status": string(RenderJobStatusSucceeded)}, nil
}

func callFFmpegRenderActivity(ctx *task.OrchestrationContext, activity string, input FFmpegRenderOrchestrationInput) error {
	if err := ctx.WaitForSingleEvent(renderCancellationEventName, 0).Await(nil); err == nil {
		return errRenderCancellationRequested
	} else if !errors.Is(err, task.ErrTaskCanceled) {
		return err
	}
	return ctx.CallActivity(activity, task.WithActivityInput(input), task.WithActivityRetryPolicy(durableRetryPolicy())).Await(nil)
}

func finishRenderOrchestration(ctx *task.OrchestrationContext, activity string, input FFmpegRenderOrchestrationInput, status RenderJobStatus) (any, error) {
	if err := ctx.CallActivity(activity, task.WithActivityInput(input), task.WithActivityRetryPolicy(durableRetryPolicy())).Await(nil); err != nil {
		return nil, err
	}
	return map[string]string{"jobId": input.JobID, "status": string(status)}, nil
}

func FFmpegRenderCancellationWatchdog(ctx *task.OrchestrationContext) (any, error) {
	var input FFmpegRenderOrchestrationInput
	if err := ctx.GetInput(&input); err != nil {
		return nil, err
	}
	if err := validateID(input.JobID, "jobId"); err != nil {
		return nil, err
	}
	if input.Version != ffmpegRenderOrchestrationVersion {
		return nil, fmt.Errorf("unsupported render orchestration version %q", input.Version)
	}
	grace := input.CancellationGrace
	if grace <= 0 {
		grace = dtsDefaultCancellationGrace
	}
	if err := ctx.CreateTimer(grace).Await(nil); err != nil {
		return nil, err
	}
	if err := ctx.CallActivity(ffmpegRenderForceCancelActivity, task.WithActivityInput(input), task.WithActivityRetryPolicy(durableRetryPolicy())).Await(nil); err != nil {
		return nil, err
	}
	return map[string]string{"jobId": input.JobID, "status": "cancellation_reconciled"}, nil
}

func NewFFmpegRenderTaskRegistry(activities *FFmpegRenderActivities) (*task.TaskRegistry, error) {
	if activities == nil {
		return nil, fmt.Errorf("FFmpeg render activities are required")
	}
	registry := task.NewTaskRegistry()
	if err := registry.AddOrchestratorN(ffmpegRenderOrchestrationName, FFmpegRenderOrchestrator); err != nil {
		return nil, err
	}
	if err := registry.AddOrchestratorN(ffmpegRenderCancellationWatchdogName, FFmpegRenderCancellationWatchdog); err != nil {
		return nil, err
	}
	definitions := []struct {
		name string
		fn   task.Activity
	}{{ffmpegRenderPrepareActivity, activities.Prepare}, {ffmpegRenderExecuteActivity, activities.Execute}, {ffmpegRenderCompleteActivity, activities.Complete}, {ffmpegRenderFailActivity, activities.Fail}, {ffmpegRenderCancelActivity, activities.Cancel}, {ffmpegRenderForceCancelActivity, activities.ForceCancel}}
	for _, definition := range definitions {
		if err := registry.AddActivityN(definition.name, retryableActivity(definition.fn)); err != nil {
			return nil, err
		}
	}
	return registry, nil
}
