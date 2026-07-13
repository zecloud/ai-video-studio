package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const (
	videoIndexerOrchestrationName    = "video-indexer-orchestration"
	videoIndexerOrchestrationVersion = "v1"
)

// VideoIndexerOrchestrationInput intentionally contains only durable blob
// references. Delegated OneDrive credentials and item IDs remain in the API
// request while staging and never cross the DTS boundary.
type VideoIndexerOrchestrationInput struct {
	JobID             string        `json:"jobId"`
	Container         string        `json:"container"`
	BlobName          string        `json:"blobName"`
	SourceName        string        `json:"sourceName,omitempty"`
	Correlation       string        `json:"correlationId,omitempty"`
	Version           string        `json:"version"`
	CancellationGrace time.Duration `json:"cancellationGrace,omitempty"`
}

type OrchestrationScheduler interface {
	Schedule(context.Context, VideoIndexerOrchestrationInput) error
	RequestCancellation(context.Context, string) error
}

// DurableJobService is the API-side implementation. It stages the source
// synchronously before scheduling so DTS never stores delegated credentials.
type DurableJobService struct {
	store             JobStore
	oneDrive          *OneDriveClient
	stager            BlobStager
	scheduler         OrchestrationScheduler
	clock             Clock
	cancellationGrace time.Duration
}

func NewDurableJobService(store JobStore, oneDrive *OneDriveClient, stager BlobStager, scheduler OrchestrationScheduler, clock Clock, cancellationGrace ...time.Duration) *DurableJobService {
	if clock == nil {
		clock = realClock{}
	}
	if oneDrive == nil {
		oneDrive = NewOneDriveClient(defaultGraphBaseURL, nil)
	}
	grace := dtsDefaultCancellationGrace
	if len(cancellationGrace) > 0 && cancellationGrace[0] > 0 {
		grace = cancellationGrace[0]
	}
	return &DurableJobService{store: store, oneDrive: oneDrive, stager: stager, scheduler: scheduler, clock: clock, cancellationGrace: grace}
}

func (s *DurableJobService) CreateJob(ctx context.Context, req CreateIndexJobRequest) (Job, error) {
	if s == nil || s.store == nil || s.stager == nil || s.scheduler == nil {
		return Job{}, newServiceError(http.StatusServiceUnavailable, "durable_execution_unavailable", "durable job service is not configured", true)
	}
	if err := req.Validate(); err != nil {
		return Job{}, newServiceError(http.StatusBadRequest, "validation_failed", redactURLsInText(err.Error()), false)
	}
	req = req.normalize()
	now := s.clock.Now()
	jobID := newJobID(req.CorrelationID)
	job := JobDocument{
		SchemaVersion:        schemaVersion,
		ID:                   jobID,
		Status:               JobStatusStaging,
		OneDriveItemID:       req.OneDriveItemID,
		SourceName:           req.SourceName,
		CorrelationID:        req.CorrelationID,
		OrchestrationID:      jobID,
		OrchestrationName:    videoIndexerOrchestrationName,
		OrchestrationVersion: videoIndexerOrchestrationVersion,
		StagingContainer:     strings.TrimSpace(s.stager.StagingContainer()),
		StagedBlobName:       stageBlobName(jobID, req.SourceName),
		CreatedAt:            now,
		UpdatedAt:            now,
	}
	if err := s.store.Create(ctx, job); err != nil {
		if req.CorrelationID != "" && isConflict(err) {
			stored, getErr := s.store.Get(ctx, job.ID)
			if getErr != nil {
				return Job{}, getErr
			}
			if stored.Status == JobStatusStaged {
				if scheduleErr := s.scheduleStaged(ctx, stored); scheduleErr != nil {
					return Job{}, scheduleErr
				}
			}
			return stored.ToJob(), nil
		}
		return Job{}, err
	}

	reader, metadata, err := s.oneDrive.OpenItem(ctx, req.OneDriveItemID, req.OneDriveAccessToken)
	if err != nil {
		return Job{}, s.failAndReturn(ctx, job.ID, err)
	}
	asset, stageErr := s.stager.Stage(ctx, job.ID, req.SourceName, reader, StageOptions{
		ContentLength: metadata.ContentLength,
		ContentType:   metadata.ContentType,
	})
	closeErr := reader.Close()
	if stageErr != nil {
		return Job{}, s.failAndReturn(ctx, job.ID, stageErr)
	}
	if closeErr != nil {
		_ = s.cleanup(context.Background(), asset)
		return Job{}, s.failAndReturn(ctx, job.ID, newServiceError(http.StatusBadGateway, "onedrive_stream_close_failed", closeErr.Error(), true))
	}

	stored, err := s.transition(ctx, job.ID, JobStatusStaged, func(doc *JobDocument) {
		doc.StagingContainer = asset.Container
		doc.StagedBlobName = asset.BlobName
		doc.Error = nil
	})
	if err != nil {
		_ = s.cleanup(context.Background(), asset)
		return Job{}, err
	}
	if stored.Status != JobStatusStaged {
		return stored.ToJob(), nil
	}

	input := VideoIndexerOrchestrationInput{
		JobID:       stored.ID,
		Container:   stored.StagingContainer,
		BlobName:    stored.StagedBlobName,
		SourceName:  stored.SourceName,
		Correlation: stored.CorrelationID,
		Version:     videoIndexerOrchestrationVersion,
	}
	if err := s.scheduler.Schedule(ctx, input); err != nil {
		return Job{}, newServiceError(http.StatusServiceUnavailable, "orchestration_schedule_failed", redactURLsInText(err.Error()), true)
	}
	return stored.ToJob(), nil
}

// ReconcileStaged schedules every persisted staged job. It closes the crash window
// between Blob staging and orchestration creation without replaying OneDrive input.
func (s *DurableJobService) ReconcileStaged(ctx context.Context) error {
	if s == nil || s.store == nil || s.scheduler == nil {
		return newServiceError(http.StatusServiceUnavailable, "durable_execution_unavailable", "durable job service is not configured", true)
	}
	jobs, err := s.store.List(ctx)
	if err != nil {
		return err
	}
	for _, job := range jobs {
		if job.Status != JobStatusStaged {
			continue
		}
		if err := s.scheduleStaged(ctx, job); err != nil {
			return err
		}
	}
	return nil
}

func (s *DurableJobService) scheduleStaged(ctx context.Context, stored StoredJob) error {
	if stored.Status != JobStatusStaged {
		return nil
	}
	if strings.TrimSpace(stored.StagingContainer) == "" || strings.TrimSpace(stored.StagedBlobName) == "" {
		return newServiceError(http.StatusConflict, "staged_asset_missing", "staged job has no durable blob reference", false)
	}
	input := VideoIndexerOrchestrationInput{JobID: stored.ID, Container: stored.StagingContainer, BlobName: stored.StagedBlobName, SourceName: stored.SourceName, Correlation: stored.CorrelationID, Version: videoIndexerOrchestrationVersion}
	if err := s.scheduler.Schedule(ctx, input); err != nil {
		return newServiceError(http.StatusServiceUnavailable, "orchestration_schedule_failed", redactURLsInText(err.Error()), true)
	}
	return nil
}

func (s *DurableJobService) GetJob(ctx context.Context, jobID string) (Job, error) {
	stored, err := s.store.Get(ctx, jobID)
	if err != nil {
		return Job{}, err
	}
	return stored.ToJob(), nil
}

func (s *DurableJobService) ListJobs(ctx context.Context, filter JobStatus) ([]Job, error) {
	stored, err := s.store.List(ctx)
	if err != nil {
		return nil, err
	}
	jobs := make([]Job, 0, len(stored))
	for _, job := range stored {
		if filter == "" || job.Status == filter {
			jobs = append(jobs, job.ToJob())
		}
	}
	return jobs, nil
}

func (s *DurableJobService) CancelJob(ctx context.Context, jobID string) (Job, error) {
	if s == nil || s.store == nil || s.scheduler == nil {
		return Job{}, newServiceError(http.StatusServiceUnavailable, "durable_execution_unavailable", "durable job service is not configured", true)
	}
	stored, err := s.store.Get(ctx, jobID)
	if err != nil {
		return Job{}, err
	}
	if stored.Status.Terminal() {
		return stored.ToJob(), nil
	}
	if stored.Status == JobStatusStaging {
		asset := StagedAsset{Container: stored.StagingContainer, BlobName: stored.StagedBlobName}
		if err := s.cleanup(context.Background(), asset); err != nil {
			return Job{}, newServiceError(http.StatusServiceUnavailable, "staging_cleanup_failed", redactURLsInText(err.Error()), true)
		}
		updated, transitionErr := s.transition(ctx, jobID, JobStatusCanceled, func(doc *JobDocument) {
			doc.Error = &APIErrorResponse{Code: "canceled", Message: "job canceled during staging", Retryable: false}
		})
		if transitionErr != nil {
			return Job{}, transitionErr
		}
		return updated.ToJob(), nil
	}

	now := s.clock.Now()
	updated, err := s.transition(ctx, jobID, stored.Status, func(doc *JobDocument) {
		doc.CancellationRequestedAt = &now
		doc.CancellationGrace = s.cancellationGrace
	})
	if err != nil {
		return Job{}, err
	}
	if err := s.scheduler.RequestCancellation(ctx, jobID); err != nil {
		return Job{}, newServiceError(http.StatusServiceUnavailable, "cancellation_signal_failed", redactURLsInText(err.Error()), true)
	}
	return updated.ToJob(), nil
}

// ReconcileCancellation is safe to call after forced DTS termination. It makes
// the public projection terminal and removes staging without relying on a live
// orchestration worker.
func (s *DurableJobService) ReconcileCancellation(ctx context.Context, jobID string) error {
	if s == nil || s.store == nil {
		return newServiceError(http.StatusServiceUnavailable, "durable_execution_unavailable", "durable job service is not configured", true)
	}
	stored, err := s.store.Get(ctx, jobID)
	if err != nil {
		return err
	}
	if stored.Status.Terminal() {
		return nil
	}
	if err := s.cleanup(ctx, StagedAsset{Container: stored.StagingContainer, BlobName: stored.StagedBlobName}); err != nil {
		return err
	}
	_, err = s.transition(ctx, jobID, JobStatusCanceled, func(doc *JobDocument) {
		doc.Error = &APIErrorResponse{Code: "canceled", Message: "job canceled", Retryable: false}
	})
	return err
}
func (s *DurableJobService) failAndReturn(ctx context.Context, jobID string, failure error) error {
	_, projectionErr := s.transition(ctx, jobID, JobStatusFailed, func(doc *JobDocument) {
		doc.Error = &APIErrorResponse{
			Code:      serviceErrorCode(failure),
			Message:   serviceErrorMessage(failure),
			Retryable: durableServiceRetryable(failure),
		}
	})
	if projectionErr != nil {
		return errors.Join(failure, fmt.Errorf("persisting failed job projection: %w", projectionErr))
	}
	return failure
}

func (s *DurableJobService) transition(ctx context.Context, jobID string, desired JobStatus, mutate func(*JobDocument)) (StoredJob, error) {
	for attempt := 0; attempt < 5; attempt++ {
		current, err := s.store.Get(ctx, jobID)
		if err != nil {
			return StoredJob{}, err
		}
		if current.Status.Terminal() && current.Status != desired {
			return current, nil
		}
		if current.Status != desired && jobStatusRank[current.Status] > jobStatusRank[desired] {
			return current, nil
		}
		next := current.JobDocument
		if current.Status != desired {
			next, err = current.JobDocument.next(desired, s.clock.Now())
			if err != nil {
				return StoredJob{}, err
			}
		} else {
			next.UpdatedAt = s.clock.Now()
		}
		if mutate != nil {
			mutate(&next)
		}
		if err := next.Validate(); err != nil {
			return StoredJob{}, err
		}
		if err := s.store.Update(ctx, next, current.ETag); err != nil {
			if isConflict(err) {
				continue
			}
			return StoredJob{}, err
		}
		return s.store.Get(ctx, jobID)
	}
	return StoredJob{}, newServiceError(http.StatusConflict, "etag_conflict", "job update conflict", true)
}

func (s *DurableJobService) cleanup(ctx context.Context, asset StagedAsset) error {
	if s.stager == nil || strings.TrimSpace(asset.Container) == "" || strings.TrimSpace(asset.BlobName) == "" {
		return nil
	}
	if err := s.stager.Delete(ctx, asset); err != nil && !isNotFound(err) {
		return fmt.Errorf("deleting staged blob %s/%s: %w", asset.Container, asset.BlobName, err)
	}
	return nil
}

var _ JobService = (*DurableJobService)(nil)

func durableServiceRetryable(err error) bool {
	var serviceErr *ServiceError
	if errors.As(err, &serviceErr) {
		return serviceErr.Retryable
	}
	return !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded)
}
