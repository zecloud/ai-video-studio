package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
)

type Clock interface {
	Now() time.Time
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now().UTC() }

type JobManagerConfig struct {
	QueueSize         int
	WorkerConcurrency int
}

type JobManager struct {
	store    JobStore
	oneDrive *OneDriveClient
	stager   BlobStager
	pipeline Pipeline
	clock    Clock
	workerID string
	obs      *Observability

	queue chan string

	mu      sync.Mutex
	started bool
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	pending map[string]CreateIndexJobRequest
	active  map[string]*activeJob
}

type activeJob struct {
	cancel context.CancelFunc
	done   chan struct{}
}

func NewJobManager(cfg JobManagerConfig, store JobStore, oneDrive *OneDriveClient, stager BlobStager, pipeline Pipeline, clock Clock) *JobManager {
	if cfg.QueueSize <= 0 {
		cfg.QueueSize = 16
	}
	if cfg.WorkerConcurrency <= 0 {
		cfg.WorkerConcurrency = 1
	}
	if clock == nil {
		clock = realClock{}
	}
	if pipeline == nil {
		pipeline = NoopPipeline{}
	}
	if oneDrive == nil {
		oneDrive = NewOneDriveClient(defaultGraphBaseURL, nil)
	}
	return &JobManager{
		store:    store,
		oneDrive: oneDrive,
		stager:   stager,
		pipeline: pipeline,
		clock:    clock,
		workerID: newWorkerID(),
		queue:    make(chan string, cfg.QueueSize),
		pending:  make(map[string]CreateIndexJobRequest),
		active:   make(map[string]*activeJob),
	}
}

func (m *JobManager) Start(ctx context.Context, workerCount int) error {
	m.mu.Lock()
	if m.started {
		m.mu.Unlock()
		return nil
	}
	if workerCount <= 0 {
		workerCount = 1
	}
	m.ctx, m.cancel = context.WithCancel(ctx)
	m.started = true
	m.mu.Unlock()

	if err := m.recover(ctx); err != nil {
		m.cancel()
		return err
	}
	for i := 0; i < workerCount; i++ {
		m.wg.Add(1)
		go func() {
			defer m.wg.Done()
			m.worker()
		}()
	}
	return nil
}

func (m *JobManager) Close() {
	m.mu.Lock()
	cancel := m.cancel
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	m.wg.Wait()
}

func (m *JobManager) CreateJob(ctx context.Context, req CreateIndexJobRequest) (Job, error) {
	if err := req.Validate(); err != nil {
		return Job{}, &ServiceError{Status: http.StatusBadRequest, Code: "validation_failed", Message: redactURLsInText(err.Error()), Retryable: false}
	}
	job := JobDocument{
		SchemaVersion:  schemaVersion,
		ID:             newJobID(req.CorrelationID),
		Status:         JobStatusQueued,
		OneDriveItemID: req.OneDriveItemID,
		SourceName:     req.SourceName,
		CorrelationID:  req.CorrelationID,
		CreatedAt:      m.clock.Now(),
		UpdatedAt:      m.clock.Now(),
	}
	if err := m.store.Create(ctx, job); err != nil {
		if req.CorrelationID != "" && isConflict(err) {
			if existing, getErr := m.store.Get(ctx, job.ID); getErr == nil {
				return existing.JobDocument.ToJob(), nil
			}
		}
		return Job{}, err
	}

	m.mu.Lock()
	m.pending[job.ID] = req
	started := m.started
	m.mu.Unlock()

	if !started {
		return job.ToJob(), nil
	}
	if err := m.enqueue(job.ID); err != nil {
		_ = m.failJob(ctx, job.ID, "queue_full", "job queue is full", true)
		m.mu.Lock()
		delete(m.pending, job.ID)
		m.mu.Unlock()
		return Job{}, err
	}
	if m.obs != nil {
		m.obs.logger.InfoContext(ctx, "job created", "job_id", job.ID, "status", job.Status)
	}
	return job.ToJob(), nil
}

func (m *JobManager) GetJob(ctx context.Context, jobID string) (Job, error) {
	stored, err := m.store.Get(ctx, jobID)
	if err != nil {
		return Job{}, err
	}
	return stored.JobDocument.ToJob(), nil
}

func (m *JobManager) ListJobs(ctx context.Context, filter JobStatus) ([]Job, error) {
	stored, err := m.store.List(ctx)
	if err != nil {
		return nil, err
	}
	jobs := make([]Job, 0, len(stored))
	for _, item := range stored {
		if filter != "" && item.Status != filter {
			continue
		}
		jobs = append(jobs, item.JobDocument.ToJob())
	}
	return jobs, nil
}

func (m *JobManager) CancelJob(ctx context.Context, jobID string) (Job, error) {
	m.mu.Lock()
	active := m.active[jobID]
	if active != nil && active.cancel != nil {
		active.cancel()
	}
	delete(m.pending, jobID)
	m.mu.Unlock()
	if active != nil {
		select {
		case <-active.done:
		case <-ctx.Done():
			return Job{}, ctx.Err()
		}
		stored, err := m.store.Get(context.Background(), jobID)
		if err != nil {
			return Job{}, err
		}
		if stored.Status.Terminal() {
			return stored.JobDocument.ToJob(), nil
		}
		updated, err := m.transition(context.Background(), jobID, JobStatusCanceled, func(doc *JobDocument) {
			doc.Error = &APIErrorResponse{
				Code:      "canceled",
				Message:   "job canceled by request",
				Retryable: false,
			}
		})
		if err != nil {
			return Job{}, err
		}
		_ = m.cleanupAsset(context.Background(), m.stagedAssetForJob(updated))
		return updated.JobDocument.ToJob(), nil
	}
	updated, err := m.transition(context.Background(), jobID, JobStatusCanceled, func(doc *JobDocument) {
		doc.Error = &APIErrorResponse{
			Code:      "canceled",
			Message:   "job canceled by request",
			Retryable: false,
		}
	})
	if err != nil {
		return Job{}, err
	}
	_ = m.cleanupAsset(context.Background(), m.stagedAssetForJob(updated))
	return updated.JobDocument.ToJob(), nil
}

func (m *JobManager) recover(ctx context.Context) error {
	jobs, err := m.store.List(ctx)
	if err != nil {
		return err
	}
	for _, stored := range jobs {
		switch stored.Status {
		case JobStatusQueued:
			m.mu.Lock()
			_, hasPending := m.pending[stored.ID]
			m.mu.Unlock()
			if hasPending {
				if err := m.enqueue(stored.ID); err != nil {
					return err
				}
				continue
			}
			if err := m.failRecoveredJob(ctx, stored); err != nil {
				return err
			}
		case JobStatusStaging:
			m.mu.Lock()
			_, hasPending := m.pending[stored.ID]
			m.mu.Unlock()
			if hasPending {
				if err := m.enqueue(stored.ID); err != nil {
					return err
				}
				continue
			}
			if err := m.failRecoveredStagingJob(ctx, stored); err != nil {
				return err
			}
		case JobStatusStaged, JobStatusProcessing, JobStatusSubmitting, JobStatusIndexing, JobStatusNormalizing, JobStatusGenerating, JobStatusBuildingTimeline:
			if err := m.enqueue(stored.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

func (m *JobManager) failRecoveredJob(ctx context.Context, stored StoredJob) error {
	_, err := m.transition(ctx, stored.ID, JobStatusFailed, func(doc *JobDocument) {
		doc.Error = &APIErrorResponse{
			Code:      "transient_token_required",
			Message:   "job restarted before OneDrive staging completed; recreate the job with a fresh delegated token",
			Retryable: true,
		}
	})
	return err
}

func (m *JobManager) failRecoveredStagingJob(ctx context.Context, stored StoredJob) error {
	asset := m.stagedAssetForJob(stored)
	cleanupErr := m.cleanupAsset(ctx, asset)
	code := "transient_token_required"
	message := "job restarted before OneDrive staging completed; recreate the job with a fresh delegated token"
	if cleanupErr != nil {
		code = "staging_cleanup_failed"
		message = fmt.Sprintf("job restarted before OneDrive staging completed; delete staged blob %s/%s and recreate the job with a fresh delegated token", asset.Container, asset.BlobName)
		if m.obs != nil {
			m.obs.logger.WarnContext(ctx, "staged blob cleanup failed", "job_id", stored.ID, "container", asset.Container, "blob_name", asset.BlobName, "error", redactURLsInText(cleanupErr.Error()))
		}
	}
	_, err := m.transition(ctx, stored.ID, JobStatusFailed, func(doc *JobDocument) {
		if asset.Container != "" {
			doc.StagingContainer = asset.Container
		}
		if asset.BlobName != "" {
			doc.StagedBlobName = asset.BlobName
		}
		doc.Error = &APIErrorResponse{
			Code:      code,
			Message:   message,
			Retryable: true,
		}
	})
	return err
}

func (m *JobManager) failJob(ctx context.Context, jobID, code, message string, retryable bool) error {
	_, err := m.transition(ctx, jobID, JobStatusFailed, func(doc *JobDocument) {
		doc.Error = &APIErrorResponse{
			Code:      code,
			Message:   redactURLsInText(message),
			Retryable: retryable,
		}
	})
	return err
}

func (m *JobManager) enqueue(jobID string) error {
	select {
	case m.queue <- jobID:
		return nil
	default:
		return newServiceError(http.StatusServiceUnavailable, "queue_full", "job queue is full", true)
	}
}

func (m *JobManager) worker() {
	for {
		select {
		case <-m.ctx.Done():
			return
		case jobID := <-m.queue:
			if jobID == "" {
				continue
			}
			_ = m.process(jobID)
		}
	}
}

func (m *JobManager) process(jobID string) error {
	jobCtx, cancel := context.WithCancel(m.ctx)
	active := &activeJob{cancel: cancel, done: make(chan struct{})}
	m.mu.Lock()
	m.active[jobID] = active
	req, hasPending := m.pending[jobID]
	delete(m.pending, jobID)
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.active, jobID)
		m.mu.Unlock()
		close(active.done)
		cancel()
	}()

	stored, err := m.store.Get(jobCtx, jobID)
	if err != nil {
		return err
	}
	if stored.Status.Terminal() {
		return nil
	}
	if !hasPending && (stored.Status == JobStatusQueued || stored.Status == JobStatusStaging) {
		return m.failJob(jobCtx, jobID, "transient_token_required", "job restarted before OneDrive staging completed; recreate the job with a fresh delegated token", true)
	}

	asset := StagedAsset{}
	if hasPending && stored.Status == JobStatusQueued {
		stored, err = m.transition(jobCtx, jobID, JobStatusStaging, func(doc *JobDocument) {
			doc.ClaimedBy = m.workerID
		})
		if err != nil {
			return err
		}
		if stored.Status != JobStatusStaging {
			return nil
		}
		downloadCtx := jobCtx
		if m.obs != nil {
			downloadCtx = m.obs.ContextWithAttrs(jobCtx, attribute.String("job_id", jobID), attribute.String("stage", "onedrive.download"))
		}
		reader, metadata, err := m.oneDrive.OpenItem(downloadCtx, req.OneDriveItemID, req.OneDriveAccessToken)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return m.finishCanceled(jobCtx, jobID, stored, asset)
			}
			return m.failJob(jobCtx, jobID, serviceErrorCode(err), serviceErrorMessage(err), serviceRetryable(err))
		}
		stageCtx := jobCtx
		if m.obs != nil {
			stageCtx = m.obs.ContextWithAttrs(jobCtx, attribute.String("job_id", jobID), attribute.String("stage", "blob.stage"))
		}
		asset, err = m.stager.Stage(stageCtx, jobID, req.SourceName, reader, StageOptions{
			ContentLength: metadata.ContentLength,
			ContentType:   metadata.ContentType,
		})
		_ = reader.Close()
		req.OneDriveAccessToken = ""
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return m.finishCanceled(jobCtx, jobID, stored, asset)
			}
			return m.failJob(jobCtx, jobID, serviceErrorCode(err), serviceErrorMessage(err), serviceRetryable(err))
		}
		stored, err = m.transition(jobCtx, jobID, JobStatusStaged, func(doc *JobDocument) {
			doc.StagingContainer = asset.Container
			doc.StagedBlobName = asset.BlobName
			doc.ClaimedBy = m.workerID
			doc.Error = nil
		})
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return m.finishCanceled(jobCtx, jobID, stored, asset)
			}
			_ = m.cleanupAsset(jobCtx, asset)
			return err
		}
		if stored.Status != JobStatusStaged {
			_ = m.cleanupAsset(jobCtx, asset)
			return nil
		}
	} else {
		if stored.StagedBlobName == "" {
			return m.failJob(jobCtx, jobID, "staged_blob_missing", "job has no staged blob to resume from", false)
		}
		asset = StagedAsset{Container: stored.StagingContainer, BlobName: stored.StagedBlobName}
	}

	readURL, err := m.stager.ReadURL(jobCtx, asset)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return m.finishCanceled(jobCtx, jobID, stored, asset)
		}
		_ = m.cleanupAsset(jobCtx, asset)
		return m.failJob(jobCtx, jobID, "staged_url_failed", err.Error(), true)
	}

	stored, err = m.transition(jobCtx, jobID, JobStatusProcessing, func(doc *JobDocument) {
		doc.ClaimedBy = m.workerID
		doc.Error = nil
	})
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return m.finishCanceled(jobCtx, jobID, stored, asset)
		}
		_ = m.cleanupAsset(jobCtx, asset)
		return err
	}
	if stored.Status != JobStatusProcessing {
		_ = m.cleanupAsset(jobCtx, asset)
		return nil
	}

	outcome, err := m.pipeline.Process(jobCtx, stored.JobDocument, asset, readURL, m)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return m.finishCanceled(jobCtx, jobID, stored, asset)
		}
		if _, ok := err.(*ServiceError); !ok {
			err = &ServiceError{Status: http.StatusInternalServerError, Code: "pipeline_failed", Message: redactURLsInText(err.Error()), Retryable: false}
		}
		_, _ = m.transition(jobCtx, jobID, JobStatusFailed, func(doc *JobDocument) {
			doc.Error = &APIErrorResponse{Code: serviceErrorCode(err), Message: serviceErrorMessage(err), Retryable: serviceRetryable(err)}
		})
		if m.obs != nil {
			m.obs.RecordJobResult(jobCtx, jobID, string(JobStatusFailed), m.clock.Now().Sub(stored.CreatedAt), attribute.String("worker_id", m.workerID))
		}
		_ = m.cleanupAsset(jobCtx, asset)
		return err
	}

	if outcome.Kind == PipelineOutcomePendingNormalization || outcome.Kind == PipelineOutcomePendingEditing {
		_ = m.cleanupAsset(jobCtx, asset)
		return nil
	}

	if _, err := m.transition(jobCtx, jobID, JobStatusSucceeded, func(doc *JobDocument) {
		doc.Error = nil
	}); err != nil {
		_ = m.cleanupAsset(jobCtx, asset)
		return err
	}
	if stored, err := m.store.Get(jobCtx, jobID); err == nil && stored.Status != JobStatusSucceeded {
		_ = m.cleanupAsset(jobCtx, asset)
		return nil
	}
	if m.obs != nil {
		m.obs.RecordJobResult(jobCtx, jobID, string(JobStatusSucceeded), m.clock.Now().Sub(stored.CreatedAt), attribute.String("worker_id", m.workerID))
	}
	_ = m.cleanupAsset(jobCtx, asset)
	return nil
}

func (m *JobManager) finishCanceled(ctx context.Context, jobID string, stored StoredJob, asset StagedAsset) error {
	_, err := m.transition(context.Background(), jobID, JobStatusCanceled, func(doc *JobDocument) {
		doc.Error = &APIErrorResponse{Code: "canceled", Message: "job canceled", Retryable: false}
	})
	if m.obs != nil {
		m.obs.RecordJobResult(ctx, jobID, string(JobStatusCanceled), m.clock.Now().Sub(stored.CreatedAt), attribute.String("worker_id", m.workerID))
	}
	cleanupErr := m.cleanupAsset(context.Background(), asset)
	if err != nil {
		return err
	}
	return cleanupErr
}

func (m *JobManager) transition(ctx context.Context, jobID string, desired JobStatus, mutate func(*JobDocument)) (StoredJob, error) {
	for attempts := 0; attempts < 5; attempts++ {
		current, err := m.store.Get(ctx, jobID)
		if err != nil {
			return StoredJob{}, err
		}

		if current.Status == desired && desired.Terminal() {
			return current, nil
		}
		if current.Status.Terminal() && current.Status != desired {
			return current, nil
		}
		if jobStatusRank[current.Status] > jobStatusRank[desired] {
			return current, nil
		}
		next, err := current.JobDocument.next(desired, m.clock.Now())
		if err != nil {
			return StoredJob{}, err
		}
		if mutate != nil {
			mutate(&next)
		}
		if err := next.Validate(); err != nil {
			return StoredJob{}, err
		}
		if err := m.store.Update(ctx, next, current.ETag); err != nil {
			if isConflict(err) {
				continue
			}
			return StoredJob{}, err
		}
		updated, err := m.store.Get(ctx, jobID)
		if err != nil {
			return StoredJob{}, err
		}
		return updated, nil
	}
	return StoredJob{}, newServiceError(http.StatusConflict, "etag_conflict", "job update conflict", true)
}

func (m *JobManager) UpdateProgress(ctx context.Context, jobID string, desired JobStatus, mutate func(*JobDocument)) (StoredJob, error) {
	return m.transition(ctx, jobID, desired, mutate)
}

func (m *JobManager) cleanupAsset(ctx context.Context, asset StagedAsset) error {
	if m.stager == nil || asset.BlobName == "" {
		return nil
	}
	if asset.Container == "" {
		return nil
	}
	if err := m.stager.Delete(ctx, asset); err != nil && !isNotFound(err) {
		return fmt.Errorf("deleting staged blob %s/%s: %w", asset.Container, asset.BlobName, err)
	}
	return nil
}

func (m *JobManager) stagedAssetForJob(job StoredJob) StagedAsset {
	asset := StagedAsset{
		Container: strings.TrimSpace(job.StagingContainer),
		BlobName:  strings.TrimSpace(job.StagedBlobName),
	}
	if asset.Container == "" && m.stager != nil {
		asset.Container = strings.TrimSpace(m.stager.StagingContainer())
	}
	if asset.BlobName == "" && job.Status == JobStatusStaging {
		asset.BlobName = stageBlobName(job.ID, job.SourceName)
	}
	return asset
}

func serviceErrorCode(err error) string {
	var se *ServiceError
	if errors.As(err, &se) && se.Code != "" {
		return se.Code
	}
	return "job_failed"
}

func serviceErrorMessage(err error) string {
	var se *ServiceError
	if errors.As(err, &se) && se.Message != "" {
		return redactURLsInText(se.Message)
	}
	return redactURLsInText(err.Error())
}

func serviceRetryable(err error) bool {
	var se *ServiceError
	if errors.As(err, &se) {
		return se.Retryable
	}
	return false
}

func newJobID(correlationID string) string {
	if correlationID = strings.TrimSpace(correlationID); correlationID != "" {
		sum := sha256.Sum256([]byte(correlationID))
		return "job-" + hex.EncodeToString(sum[:8])
	}
	var buf [8]byte
	_, _ = rand.Read(buf[:])
	return "job-" + hex.EncodeToString(buf[:])
}

func newWorkerID() string {
	var buf [8]byte
	_, _ = rand.Read(buf[:])
	return "worker-" + hex.EncodeToString(buf[:])
}
