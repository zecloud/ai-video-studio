package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"
)

const (
	ffmpegRenderOrchestrationName    = "ffmpeg-render-orchestration"
	ffmpegRenderOrchestrationVersion = "v1"
	renderProjectionTimeout          = 30 * time.Second
	renderStagingRecoveryDelay       = 5 * time.Minute
)

// FFmpegRenderOrchestrationInput holds only durable Blob references. The
// delegated OneDrive token is consumed while the API stages the source clips.
type FFmpegRenderOrchestrationInput struct {
	JobID             string                    `json:"jobId"`
	Clips             []StagedRenderClip        `json:"clips"`
	Transitions       []RenderTransitionRequest `json:"transitions,omitempty"`
	Preset            string                    `json:"preset"`
	Output            RenderOutput              `json:"output"`
	Version           string                    `json:"version"`
	CancellationGrace time.Duration             `json:"cancellationGrace,omitempty"`
}

type RenderOrchestrationScheduler interface {
	ScheduleRender(context.Context, FFmpegRenderOrchestrationInput) error
	RequestRenderCancellation(context.Context, string) error
}

type RenderJobService interface {
	CreateRenderJob(context.Context, CreateRenderJobRequest) (RenderJob, error)
	GetRenderJob(context.Context, string) (RenderJob, error)
	ListRenderJobs(context.Context, RenderJobStatus) ([]RenderJob, error)
	CancelRenderJob(context.Context, string) (RenderJob, error)
	ReconcileQueuedRenders(context.Context) error
}

type DurableRenderJobService struct {
	store     RenderJobStore
	oneDrive  *OneDriveClient
	stager    RenderBlobStager
	scheduler RenderOrchestrationScheduler
	clock     Clock
}

func NewDurableRenderJobService(store RenderJobStore, oneDrive *OneDriveClient, stager RenderBlobStager, scheduler RenderOrchestrationScheduler, clock Clock) *DurableRenderJobService {
	if clock == nil {
		clock = realClock{}
	}
	if oneDrive == nil {
		oneDrive = NewOneDriveClient(defaultGraphBaseURL, nil)
	}
	return &DurableRenderJobService{store: store, oneDrive: oneDrive, stager: stager, scheduler: scheduler, clock: clock}
}

func (s *DurableRenderJobService) CreateRenderJob(ctx context.Context, req CreateRenderJobRequest) (RenderJob, error) {
	if s == nil || s.store == nil || s.stager == nil || s.scheduler == nil {
		return RenderJob{}, newServiceError(http.StatusServiceUnavailable, "render_execution_unavailable", "render job service is not configured", true)
	}
	if err := req.Validate(); err != nil {
		return RenderJob{}, newServiceError(http.StatusBadRequest, "validation_failed", err.Error(), false)
	}
	req = req.normalize()
	fingerprint, err := renderRequestFingerprint(req)
	if err != nil {
		return RenderJob{}, newServiceError(http.StatusInternalServerError, "render_request_fingerprint_failed", err.Error(), false)
	}
	now := s.clock.Now()
	jobID := newJobID(req.CorrelationID)
	job := RenderJobDocument{
		SchemaVersion: renderSchemaVersion, ID: jobID, ProjectID: req.ProjectID, Status: RenderJobStatusStaging,
		Preset: req.Preset, OutputName: req.OutputName, CorrelationID: req.CorrelationID,
		RequestFingerprint: fingerprint, Transitions: append([]RenderTransitionRequest(nil), req.Transitions...),
		OrchestrationID: jobID, OrchestrationName: ffmpegRenderOrchestrationName,
		OrchestrationVersion: ffmpegRenderOrchestrationVersion, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.store.Create(ctx, job); err != nil {
		if !isConflict(err) || req.CorrelationID == "" {
			return RenderJob{}, err
		}
		existing, getErr := s.store.Get(ctx, jobID)
		if getErr != nil {
			return RenderJob{}, errors.Join(err, getErr)
		}
		if existing.CorrelationID != req.CorrelationID || existing.RequestFingerprint == "" || existing.RequestFingerprint != fingerprint {
			return RenderJob{}, newServiceError(http.StatusConflict, "render_correlation_conflict", "correlationId already belongs to a different render request", false)
		}
		if existing.Status == RenderJobStatusStaging && now.Sub(existing.UpdatedAt) >= renderStagingRecoveryDelay {
			if recoveryErr := s.failStaging(existing.ID, existing.Clips, newServiceError(http.StatusServiceUnavailable, "render_staging_interrupted", "render input staging was interrupted; submit a new render request", true)); recoveryErr != nil {
				return RenderJob{}, recoveryErr
			}
			refreshed, getErr := s.store.Get(ctx, existing.ID)
			if getErr != nil {
				return RenderJob{}, getErr
			}
			return refreshed.ToRenderJob(), nil
		}
		if existing.Status == RenderJobStatusQueued && existing.CancellationRequestedAt == nil {
			if scheduleErr := s.schedule(ctx, existing); scheduleErr != nil {
				return RenderJob{}, scheduleErr
			}
		}
		return existing.ToRenderJob(), nil
	}

	staged := make([]StagedRenderClip, 0, len(req.Clips))
	for _, clip := range req.Clips {
		reader, metadata, openErr := s.oneDrive.OpenItem(ctx, clip.OneDriveItemID, req.OneDriveAccessToken)
		if openErr != nil {
			return RenderJob{}, s.failStaging(jobID, staged, openErr)
		}
		asset, stageErr := s.stager.StageNamed(ctx, renderStageBlobName(jobID, clip.ID, clip.SourceName), reader, StageOptions{ContentLength: metadata.ContentLength, ContentType: metadata.ContentType})
		closeErr := reader.Close()
		if stageErr != nil {
			return RenderJob{}, s.failStaging(jobID, staged, errors.Join(stageErr, closeErr))
		}
		staged = append(staged, StagedRenderClip{ID: clip.ID, Container: asset.Container, BlobName: asset.BlobName, SourceName: clip.SourceName, InMS: clip.InMS, OutMS: clip.OutMS, Muted: clip.Muted})
		if closeErr != nil {
			return RenderJob{}, s.failStaging(jobID, staged, closeErr)
		}
		stored, updateErr := s.transition(ctx, jobID, RenderJobStatusStaging, func(doc *RenderJobDocument) {
			doc.Clips = append([]StagedRenderClip(nil), staged...)
		})
		if updateErr != nil {
			return RenderJob{}, s.failStaging(jobID, staged, updateErr)
		}
		if stored.CancellationRequestedAt != nil || stored.Status != RenderJobStatusStaging {
			canceled, cancelErr := s.finishCanceledWithClipsBounded(jobID, staged)
			if cancelErr != nil {
				return RenderJob{}, cancelErr
			}
			return canceled.ToRenderJob(), nil
		}
	}

	output := RenderOutput{Container: s.stager.StagingContainer(), BlobName: renderOutputBlobName(jobID, req.OutputName), MediaType: "video/mp4"}
	stored, err := s.transition(ctx, jobID, RenderJobStatusQueued, func(doc *RenderJobDocument) {
		doc.Clips = append([]StagedRenderClip(nil), staged...)
		doc.Output = &output
		doc.Error = nil
	})
	if err != nil {
		return RenderJob{}, s.failStaging(jobID, staged, err)
	}
	if stored.CancellationRequestedAt != nil || stored.Status != RenderJobStatusQueued {
		canceled, cancelErr := s.finishCanceledWithClipsBounded(jobID, staged)
		if cancelErr != nil {
			return RenderJob{}, cancelErr
		}
		return canceled.ToRenderJob(), nil
	}
	if err := s.schedule(ctx, stored); err != nil {
		return RenderJob{}, err
	}
	return stored.ToRenderJob(), nil
}

func (s *DurableRenderJobService) GetRenderJob(ctx context.Context, id string) (RenderJob, error) {
	stored, err := s.store.Get(ctx, id)
	if err != nil {
		return RenderJob{}, err
	}
	return stored.ToRenderJob(), nil
}

func (s *DurableRenderJobService) ListRenderJobs(ctx context.Context, filter RenderJobStatus) ([]RenderJob, error) {
	stored, err := s.store.List(ctx)
	if err != nil {
		return nil, err
	}
	jobs := make([]RenderJob, 0, len(stored))
	for _, job := range stored {
		if filter == "" || job.Status == filter {
			jobs = append(jobs, job.ToRenderJob())
		}
	}
	return jobs, nil
}

func (s *DurableRenderJobService) ReconcileQueuedRenders(ctx context.Context) error {
	jobs, err := s.store.List(ctx)
	if err != nil {
		return err
	}
	var reconcileErr error
	for _, job := range jobs {
		switch {
		case job.Status.Terminal() && job.InputsCleanupPending:
			_, cleanupErr := s.cleanupAndRecord(ctx, job.ID)
			reconcileErr = errors.Join(reconcileErr, cleanupErr)
		case job.CancellationRequestedAt != nil && job.Status == RenderJobStatusStaging:
			_, cancelErr := s.finishCanceled(ctx, job.ID)
			reconcileErr = errors.Join(reconcileErr, cancelErr)
		case job.Status == RenderJobStatusStaging && s.clock.Now().Sub(job.UpdatedAt) >= renderStagingRecoveryDelay:
			if failErr := s.failStaging(job.ID, job.Clips, newServiceError(http.StatusServiceUnavailable, "render_staging_interrupted", "render input staging was interrupted; submit a new render request", true)); failErr != nil {
				if refreshed, getErr := s.store.Get(ctx, job.ID); getErr != nil || refreshed.Status != RenderJobStatusFailed {
					reconcileErr = errors.Join(reconcileErr, failErr)
				}
			}
		case job.CancellationRequestedAt != nil && !job.Status.Terminal():
			if signalErr := s.scheduler.RequestRenderCancellation(ctx, job.ID); signalErr != nil {
				reconcileErr = errors.Join(reconcileErr, fmt.Errorf("re-signaling cancellation for render %s: %w", job.ID, signalErr))
			}
		case job.Status == RenderJobStatusQueued:
			reconcileErr = errors.Join(reconcileErr, s.schedule(ctx, job))
		}
	}
	return reconcileErr
}

func (s *DurableRenderJobService) schedule(ctx context.Context, job StoredRenderJob) error {
	if job.Status != RenderJobStatusQueued || job.Output == nil || job.CancellationRequestedAt != nil {
		return nil
	}
	input := FFmpegRenderOrchestrationInput{JobID: job.ID, Clips: append([]StagedRenderClip(nil), job.Clips...), Transitions: append([]RenderTransitionRequest(nil), job.Transitions...), Preset: job.Preset, Output: *job.Output, Version: ffmpegRenderOrchestrationVersion}
	if err := s.scheduler.ScheduleRender(ctx, input); err != nil {
		return newServiceError(http.StatusServiceUnavailable, "render_orchestration_schedule_failed", redactURLsInText(err.Error()), true)
	}
	return nil
}

func (s *DurableRenderJobService) CancelRenderJob(ctx context.Context, id string) (RenderJob, error) {
	stored, err := s.requestCancellation(ctx, id)
	if err != nil {
		return RenderJob{}, err
	}
	if stored.Status.Terminal() {
		return stored.ToRenderJob(), nil
	}
	if stored.Status == RenderJobStatusStaging {
		canceled, cancelErr := s.finishCanceledBounded(id)
		if cancelErr != nil {
			return RenderJob{}, cancelErr
		}
		return canceled.ToRenderJob(), nil
	}
	if err := s.scheduler.RequestRenderCancellation(ctx, id); err != nil {
		return RenderJob{}, newServiceError(http.StatusServiceUnavailable, "render_cancellation_signal_failed", redactURLsInText(err.Error()), true)
	}
	latest, err := s.store.Get(ctx, id)
	if err != nil {
		return RenderJob{}, err
	}
	return latest.ToRenderJob(), nil
}

func (s *DurableRenderJobService) requestCancellation(ctx context.Context, id string) (StoredRenderJob, error) {
	return s.mutate(ctx, id, func(doc *RenderJobDocument) {
		if doc.Status.Terminal() || doc.CancellationRequestedAt != nil {
			return
		}
		requested := s.clock.Now()
		doc.CancellationRequestedAt = &requested
	})
}

func (s *DurableRenderJobService) failStaging(id string, clips []StagedRenderClip, failure error) error {
	ctx, cancel := context.WithTimeout(context.Background(), renderProjectionTimeout)
	defer cancel()
	current, getErr := s.store.Get(ctx, id)
	if getErr != nil {
		return errors.Join(failure, fmt.Errorf("reading render failure projection: %w", getErr), s.cleanupWithContext(ctx, clips))
	}
	status := RenderJobStatusFailed
	code, message, retryable := serviceErrorCode(failure), serviceErrorMessage(failure), durableServiceRetryable(failure)
	if current.CancellationRequestedAt != nil || current.Status == RenderJobStatusCanceled {
		status, code, message, retryable = RenderJobStatusCanceled, "canceled", "render job canceled", false
	}
	stored, updateErr := s.transition(ctx, id, status, func(doc *RenderJobDocument) {
		doc.Clips = append([]StagedRenderClip(nil), clips...)
		doc.Error = &APIErrorResponse{Code: code, Message: message, Retryable: retryable}
		doc.InputsCleanupPending = len(clips) > 0
	})
	if updateErr != nil {
		return errors.Join(failure, fmt.Errorf("persisting render failure: %w", updateErr), s.cleanupWithContext(ctx, clips))
	}
	_, cleanupErr := s.cleanupAndRecord(ctx, stored.ID)
	return errors.Join(failure, cleanupErr)
}

func (s *DurableRenderJobService) finishCanceledBounded(id string) (StoredRenderJob, error) {
	return s.finishCanceledWithClipsBounded(id, nil)
}

func (s *DurableRenderJobService) finishCanceledWithClipsBounded(id string, clips []StagedRenderClip) (StoredRenderJob, error) {
	ctx, cancel := context.WithTimeout(context.Background(), renderProjectionTimeout)
	defer cancel()
	return s.finishCanceledWithClips(ctx, id, clips)
}

func (s *DurableRenderJobService) finishCanceled(ctx context.Context, id string) (StoredRenderJob, error) {
	return s.finishCanceledWithClips(ctx, id, nil)
}

func (s *DurableRenderJobService) finishCanceledWithClips(ctx context.Context, id string, clips []StagedRenderClip) (StoredRenderJob, error) {
	stored, err := s.transition(ctx, id, RenderJobStatusCanceled, func(doc *RenderJobDocument) {
		if clips != nil {
			doc.Clips = append([]StagedRenderClip(nil), clips...)
		}
		doc.Error = &APIErrorResponse{Code: "canceled", Message: "render job canceled", Retryable: false}
		doc.InputsCleanupPending = len(doc.Clips) > 0
	})
	if err != nil {
		return StoredRenderJob{}, err
	}
	return s.cleanupAndRecord(ctx, stored.ID)
}

func (s *DurableRenderJobService) cleanupBounded(clips []StagedRenderClip) error {
	ctx, cancel := context.WithTimeout(context.Background(), renderProjectionTimeout)
	defer cancel()
	return s.cleanupWithContext(ctx, clips)
}

func (s *DurableRenderJobService) cleanupWithContext(ctx context.Context, clips []StagedRenderClip) error {
	var cleanupErr error
	for _, clip := range clips {
		if err := s.stager.Delete(ctx, StagedAsset{Container: clip.Container, BlobName: clip.BlobName}); err != nil {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("deleting render input %s: %w", clip.BlobName, err))
		}
	}
	return cleanupErr
}

func (s *DurableRenderJobService) cleanupAndRecord(ctx context.Context, id string) (StoredRenderJob, error) {
	current, err := s.store.Get(ctx, id)
	if err != nil {
		return StoredRenderJob{}, err
	}
	if !current.Status.Terminal() {
		return current, nil
	}
	cleanupErr := s.cleanupWithContext(ctx, current.Clips)
	updated, updateErr := s.mutate(ctx, id, func(doc *RenderJobDocument) {
		if cleanupErr == nil {
			doc.InputsCleanupPending = false
			doc.CleanupError = nil
			return
		}
		doc.InputsCleanupPending = true
		doc.CleanupError = &APIErrorResponse{Code: "render_input_cleanup_failed", Message: redactURLsInText(cleanupErr.Error()), Retryable: true}
	})
	if updateErr != nil {
		return StoredRenderJob{}, errors.Join(cleanupErr, fmt.Errorf("persisting render cleanup state: %w", updateErr))
	}
	return updated, cleanupErr
}

func (s *DurableRenderJobService) mutate(ctx context.Context, id string, mutate func(*RenderJobDocument)) (StoredRenderJob, error) {
	for attempt := 0; attempt < 5; attempt++ {
		current, err := s.store.Get(ctx, id)
		if err != nil {
			return StoredRenderJob{}, err
		}
		next := current.RenderJobDocument
		if mutate != nil {
			mutate(&next)
		}
		next.UpdatedAt = s.clock.Now()
		if err := s.store.Update(ctx, next, current.ETag); err == nil {
			return s.store.Get(ctx, id)
		} else if !isConflict(err) {
			return StoredRenderJob{}, err
		}
	}
	return StoredRenderJob{}, newServiceError(http.StatusConflict, "etag_conflict", "render job update conflict", true)
}

func (s *DurableRenderJobService) transition(ctx context.Context, id string, status RenderJobStatus, mutate func(*RenderJobDocument)) (StoredRenderJob, error) {
	return s.mutate(ctx, id, func(next *RenderJobDocument) {
		if next.Status.Terminal() && next.Status != status {
			return
		}
		if next.CancellationRequestedAt != nil && status != RenderJobStatusCanceled {
			return
		}
		if renderStatusRank(status) < renderStatusRank(next.Status) && status != RenderJobStatusFailed && status != RenderJobStatusCanceled {
			return
		}
		next.Status = status
		if next.StartedAt == nil && (status == RenderJobStatusRendering || status == RenderJobStatusUploading || status == RenderJobStatusSucceeded || status == RenderJobStatusFailed) {
			started := s.clock.Now()
			next.StartedAt = &started
		}
		if status.Terminal() && next.CompletedAt == nil {
			completed := s.clock.Now()
			next.CompletedAt = &completed
		}
		if mutate != nil {
			mutate(next)
		}
	})
}

func renderStatusRank(status RenderJobStatus) int {
	switch status {
	case RenderJobStatusStaging:
		return 0
	case RenderJobStatusQueued:
		return 1
	case RenderJobStatusRendering:
		return 2
	case RenderJobStatusUploading:
		return 3
	default:
		return 4
	}
}

var _ RenderJobService = (*DurableRenderJobService)(nil)
