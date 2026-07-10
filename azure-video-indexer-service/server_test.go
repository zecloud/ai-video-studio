package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

type fixedClock struct {
	now time.Time
}

func (c fixedClock) Now() time.Time { return c.now }

type memoryJobStore struct {
	mu       sync.Mutex
	jobs     map[string]StoredJob
	history  map[string][]JobDocument
	payloads map[string][][]byte
	seq      int
}

func newMemoryJobStore() *memoryJobStore {
	return &memoryJobStore{
		jobs:     make(map[string]StoredJob),
		history:  make(map[string][]JobDocument),
		payloads: make(map[string][][]byte),
	}
}

func (s *memoryJobStore) Create(ctx context.Context, job JobDocument) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.jobs[job.ID]; exists {
		return newServiceError(http.StatusConflict, "job_exists", "job already exists", false)
	}
	s.seq++
	stored := StoredJob{JobDocument: job, ETag: fmt.Sprintf("etag-%d", s.seq)}
	s.jobs[job.ID] = stored
	s.recordLocked(job)
	return nil
}

func (s *memoryJobStore) Get(ctx context.Context, jobID string) (StoredJob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	stored, ok := s.jobs[jobID]
	if !ok {
		return StoredJob{}, newServiceError(http.StatusNotFound, "job_not_found", "job not found", false)
	}
	return stored, nil
}

func (s *memoryJobStore) List(ctx context.Context) ([]StoredJob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]StoredJob, 0, len(s.jobs))
	for _, job := range s.jobs {
		out = append(out, job)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out, nil
}

func (s *memoryJobStore) Update(ctx context.Context, job JobDocument, etag string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.jobs[job.ID]
	if !ok {
		return newServiceError(http.StatusNotFound, "job_not_found", "job not found", false)
	}
	if current.ETag != etag {
		return newServiceError(http.StatusConflict, "etag_conflict", "job update conflict", true)
	}
	s.seq++
	s.jobs[job.ID] = StoredJob{JobDocument: job, ETag: fmt.Sprintf("etag-%d", s.seq)}
	s.recordLocked(job)
	return nil
}

func (s *memoryJobStore) recordLocked(job JobDocument) {
	data, _ := json.Marshal(job)
	s.history[job.ID] = append(s.history[job.ID], job)
	s.payloads[job.ID] = append(s.payloads[job.ID], data)
}

func (s *memoryJobStore) statuses(jobID string) []JobStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	history := s.history[jobID]
	out := make([]JobStatus, 0, len(history))
	for _, job := range history {
		out = append(out, job.Status)
	}
	return out
}

func (s *memoryJobStore) payloadStrings(jobID string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries := s.payloads[jobID]
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		out = append(out, string(entry))
	}
	return out
}

type fakeStager struct {
	mu               sync.Mutex
	stageCalls       []fakeStageCall
	deleteCalls      []StagedAsset
	baseURL          string
	stagingContainer string
	deleteErr        error
}

type fakeStageCall struct {
	JobID      string
	SourceName string
	BlobName   string
	Size       int64
}

func (s *fakeStager) Stage(ctx context.Context, jobID, sourceName string, body io.Reader) (StagedAsset, error) {
	size, _ := io.Copy(io.Discard, body)
	asset := StagedAsset{Container: s.StagingContainer(), BlobName: stageBlobName(jobID, sourceName)}
	s.mu.Lock()
	s.stageCalls = append(s.stageCalls, fakeStageCall{JobID: jobID, SourceName: sourceName, BlobName: asset.BlobName, Size: size})
	s.mu.Unlock()
	return asset, nil
}

func (s *fakeStager) ReadURL(ctx context.Context, asset StagedAsset) (string, error) {
	base := s.baseURL
	if base == "" {
		base = "https://staged.example.com"
	}
	return fmt.Sprintf("%s/%s?sig=ephemeral", strings.TrimRight(base, "/"), asset.BlobName), nil
}

func (s *fakeStager) Delete(ctx context.Context, asset StagedAsset) error {
	s.mu.Lock()
	s.deleteCalls = append(s.deleteCalls, asset)
	s.mu.Unlock()
	return s.deleteErr
}

func (s *fakeStager) StagingContainer() string {
	if strings.TrimSpace(s.stagingContainer) != "" {
		return strings.TrimSpace(s.stagingContainer)
	}
	return "video-indexer-staging"
}

type fakePipeline struct {
	mu      sync.Mutex
	calls   []pipelineCall
	started chan struct{}
	release chan struct{}
	outcome PipelineOutcome
	err     error
	once    sync.Once
}

type pipelineCall struct {
	JobID   string
	Blob    string
	ReadURL string
	Status  JobStatus
}

func (p *fakePipeline) Process(ctx context.Context, job JobDocument, asset StagedAsset, readURL string, progress JobProgressRecorder) (PipelineOutcome, error) {
	p.mu.Lock()
	p.calls = append(p.calls, pipelineCall{JobID: job.ID, Blob: asset.BlobName, ReadURL: readURL, Status: job.Status})
	p.mu.Unlock()
	if p.started != nil {
		p.once.Do(func() { close(p.started) })
	}
	if p.release != nil {
		select {
		case <-p.release:
		case <-ctx.Done():
			return PipelineOutcome{}, ctx.Err()
		}
	}
	if p.err != nil {
		return PipelineOutcome{}, p.err
	}
	if p.outcome.Kind == "" {
		p.outcome.Kind = PipelineOutcomePendingNormalization
	}
	return p.outcome, nil
}

type cancelIgnoringPipeline struct {
	mu             sync.Mutex
	started        chan struct{}
	cancelObserved chan struct{}
	release        chan struct{}
	onceStarted    sync.Once
	onceCanceled   sync.Once
}

func (p *cancelIgnoringPipeline) Process(ctx context.Context, job JobDocument, asset StagedAsset, readURL string, progress JobProgressRecorder) (PipelineOutcome, error) {
	if p.started != nil {
		p.onceStarted.Do(func() { close(p.started) })
	}
	select {
	case <-ctx.Done():
		if p.cancelObserved != nil {
			p.onceCanceled.Do(func() { close(p.cancelObserved) })
		}
		if p.release != nil {
			<-p.release
		}
	case <-p.release:
	}
	return PipelineOutcome{Kind: PipelineOutcomeCompleted}, nil
}

func TestOneDriveClientStreamsAndBoundsErrors(t *testing.T) {
	downloadServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "" {
			t.Fatalf("download host received authorization header: %q", got)
		}
		w.Header().Set("Content-Type", "video/mp4")
		w.Header().Set("Content-Disposition", `attachment; filename="clip.mp4"`)
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "streamed-video-bytes")
	}))
	defer downloadServer.Close()

	graphServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer token-abc" {
			t.Fatalf("unexpected authorization header: %q", got)
		}
		http.Redirect(w, r, downloadServer.URL, http.StatusFound)
	}))
	defer graphServer.Close()

	client := NewOneDriveClient(graphServer.URL, graphServer.Client())
	body, meta, err := client.OpenItem(context.Background(), "item-123", "token-abc")
	if err != nil {
		t.Fatalf("open item: %v", err)
	}
	defer body.Close()

	data, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if string(data) != "streamed-video-bytes" {
		t.Fatalf("unexpected body: %q", string(data))
	}
	if meta.FileName != "clip.mp4" || meta.ContentType != "video/mp4" {
		t.Fatalf("unexpected metadata: %#v", meta)
	}

	errorServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, strings.Repeat("x", 5000))
	}))
	defer errorServer.Close()

	client = NewOneDriveClient(errorServer.URL, errorServer.Client())
	_, _, err = client.OpenItem(context.Background(), "item-123", "token-abc")
	if err == nil {
		t.Fatal("expected error")
	}
	var svcErr *ServiceError
	if !errors.As(err, &svcErr) || svcErr.Status != http.StatusTooManyRequests || !svcErr.Retryable {
		t.Fatalf("unexpected error: %#v", err)
	}
	if strings.Contains(err.Error(), strings.Repeat("x", 4500)) {
		t.Fatalf("error body was not bounded: %v", err)
	}
}

func TestManagerProcessesJobAndDoesNotPersistSecrets(t *testing.T) {
	store := newMemoryJobStore()
	stager := &fakeStager{baseURL: "https://staged.example.com"}
	pipeline := &fakePipeline{started: make(chan struct{})}
	oneDrive := newTestOneDriveServer(t, "download-video-bytes")

	manager := NewJobManager(JobManagerConfig{QueueSize: 8, WorkerConcurrency: 1}, store, oneDrive, stager, pipeline, fixedClock{now: time.Date(2026, 7, 10, 13, 46, 9, 0, time.UTC)})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := manager.Start(ctx, 1); err != nil {
		t.Fatalf("start manager: %v", err)
	}
	t.Cleanup(manager.Close)

	job, err := manager.CreateJob(context.Background(), CreateIndexJobRequest{
		OneDriveItemID:      "item-001",
		OneDriveAccessToken: "token-abc",
		SourceName:          "action-4-clip.mp4",
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	if job.Status != JobStatusQueued {
		t.Fatalf("unexpected initial status: %s", job.Status)
	}

	waitFor(t, time.Second, func() bool {
		statuses := store.statuses(job.ID)
		return len(statuses) > 0 && statuses[len(statuses)-1] == JobStatusProcessing
	})

	stored, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if stored.Status != JobStatusProcessing {
		t.Fatalf("unexpected final status: %s", stored.Status)
	}

	wantStatuses := []JobStatus{JobStatusQueued, JobStatusStaging, JobStatusStaged, JobStatusProcessing}
	gotStatuses := store.statuses(job.ID)
	if !containsSequence(gotStatuses, wantStatuses) {
		t.Fatalf("unexpected transition history: %v", gotStatuses)
	}

	for _, payload := range store.payloadStrings(job.ID) {
		if strings.Contains(payload, "token-abc") || strings.Contains(payload, "sig=ephemeral") {
			t.Fatalf("secret leaked into persistent payload: %s", payload)
		}
	}

	stager.mu.Lock()
	defer stager.mu.Unlock()
	if len(stager.deleteCalls) == 0 {
		t.Fatal("expected staged blob cleanup")
	}
	if len(pipeline.calls) != 1 {
		t.Fatalf("expected one pipeline call, got %d", len(pipeline.calls))
	}
	if !strings.Contains(pipeline.calls[0].ReadURL, "sig=ephemeral") {
		t.Fatalf("expected ephemeral read URL, got %q", pipeline.calls[0].ReadURL)
	}
}

func TestManagerCancelAndRecovery(t *testing.T) {
	t.Run("cancel running job", func(t *testing.T) {
		store := newMemoryJobStore()
		stager := &fakeStager{baseURL: "https://staged.example.com"}
		pipeline := &fakePipeline{started: make(chan struct{}), release: make(chan struct{})}
		oneDrive := newTestOneDriveServer(t, "download-video-bytes")

		manager := NewJobManager(JobManagerConfig{QueueSize: 8, WorkerConcurrency: 1}, store, oneDrive, stager, pipeline, fixedClock{now: time.Date(2026, 7, 10, 13, 46, 9, 0, time.UTC)})
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		if err := manager.Start(ctx, 1); err != nil {
			t.Fatalf("start manager: %v", err)
		}
		t.Cleanup(manager.Close)

		job, err := manager.CreateJob(context.Background(), CreateIndexJobRequest{
			OneDriveItemID:      "item-002",
			OneDriveAccessToken: "token-xyz",
			SourceName:          "ride.mp4",
		})
		if err != nil {
			t.Fatalf("create job: %v", err)
		}

		waitFor(t, time.Second, func() bool {
			select {
			case <-pipeline.started:
				return true
			default:
				return false
			}
		})

		canceled, err := manager.CancelJob(context.Background(), job.ID)
		if err != nil {
			t.Fatalf("cancel job: %v", err)
		}
		if canceled.Status != JobStatusCanceled {
			t.Fatalf("unexpected canceled status: %s", canceled.Status)
		}

		close(pipeline.release)
		waitFor(t, time.Second, func() bool {
			stored, err := store.Get(context.Background(), job.ID)
			return err == nil && stored.Status == JobStatusCanceled
		})

		if len(stager.deleteCalls) == 0 {
			t.Fatal("expected cleanup after cancellation")
		}
	})

	t.Run("cancel returns succeeded when the worker finishes first", func(t *testing.T) {
		store := newMemoryJobStore()
		stager := &fakeStager{baseURL: "https://staged.example.com"}
		pipeline := &cancelIgnoringPipeline{
			started:        make(chan struct{}),
			cancelObserved: make(chan struct{}),
			release:        make(chan struct{}),
		}
		oneDrive := newTestOneDriveServer(t, "download-video-bytes")

		manager := NewJobManager(JobManagerConfig{QueueSize: 8, WorkerConcurrency: 1}, store, oneDrive, stager, pipeline, fixedClock{now: time.Date(2026, 7, 10, 13, 46, 9, 0, time.UTC)})
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		if err := manager.Start(ctx, 1); err != nil {
			t.Fatalf("start manager: %v", err)
		}
		t.Cleanup(manager.Close)

		job, err := manager.CreateJob(context.Background(), CreateIndexJobRequest{
			OneDriveItemID:      "item-003",
			OneDriveAccessToken: "token-abc",
			SourceName:          "race.mp4",
		})
		if err != nil {
			t.Fatalf("create job: %v", err)
		}

		waitFor(t, time.Second, func() bool {
			select {
			case <-pipeline.started:
				return true
			default:
				return false
			}
		})

		cancelDone := make(chan struct{})
		var canceled Job
		var cancelErr error
		go func() {
			canceled, cancelErr = manager.CancelJob(context.Background(), job.ID)
			close(cancelDone)
		}()

		waitFor(t, time.Second, func() bool {
			select {
			case <-pipeline.cancelObserved:
				return true
			default:
				return false
			}
		})
		close(pipeline.release)

		waitFor(t, time.Second, func() bool {
			select {
			case <-cancelDone:
				return true
			default:
				return false
			}
		})

		if cancelErr != nil {
			t.Fatalf("cancel job: %v", cancelErr)
		}
		if canceled.Status != JobStatusSucceeded {
			t.Fatalf("unexpected cancel status: %s", canceled.Status)
		}
		stored, err := store.Get(context.Background(), job.ID)
		if err != nil {
			t.Fatalf("get job: %v", err)
		}
		if stored.Status != JobStatusSucceeded {
			t.Fatalf("unexpected durable status: %s", stored.Status)
		}
		if len(stager.deleteCalls) == 0 {
			t.Fatal("expected cleanup after completion")
		}
	})

	t.Run("recovery resumes staged jobs and fails queued jobs without token", func(t *testing.T) {
		store := newMemoryJobStore()
		clock := fixedClock{now: time.Date(2026, 7, 10, 13, 46, 9, 0, time.UTC)}
		queued := JobDocument{
			SchemaVersion:  schemaVersion,
			ID:             "job-queued",
			Status:         JobStatusQueued,
			OneDriveItemID: "item-queued",
			SourceName:     "queued.mp4",
			CreatedAt:      clock.Now(),
			UpdatedAt:      clock.Now(),
		}
		staged := JobDocument{
			SchemaVersion:    schemaVersion,
			ID:               "job-staged",
			Status:           JobStatusStaged,
			OneDriveItemID:   "item-staged",
			SourceName:       "staged.mp4",
			StagingContainer: "video-indexer-staging",
			StagedBlobName:   stageBlobName("job-staged", "staged.mp4"),
			CreatedAt:        clock.Now(),
			UpdatedAt:        clock.Now(),
			StartedAt:        ptr(clock.Now()),
		}
		if err := store.Create(context.Background(), queued); err != nil {
			t.Fatalf("create queued job: %v", err)
		}
		if err := store.Create(context.Background(), staged); err != nil {
			t.Fatalf("create staged job: %v", err)
		}

		stager := &fakeStager{baseURL: "https://staged.example.com"}
		pipeline := &fakePipeline{started: make(chan struct{}), release: make(chan struct{})}
		oneDrive := newTestOneDriveServer(t, "unexpected onedrive request")

		manager := NewJobManager(JobManagerConfig{QueueSize: 8, WorkerConcurrency: 1}, store, oneDrive, stager, pipeline, clock)
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		if err := manager.Start(ctx, 1); err != nil {
			t.Fatalf("start manager: %v", err)
		}
		t.Cleanup(manager.Close)

		waitFor(t, time.Second, func() bool {
			select {
			case <-pipeline.started:
				return true
			default:
				return false
			}
		})

		stager.mu.Lock()
		if len(stager.deleteCalls) != 0 {
			stager.mu.Unlock()
			t.Fatal("cleanup happened before downstream processing finished")
		}
		stager.mu.Unlock()

		close(pipeline.release)

		waitFor(t, time.Second, func() bool {
			storedQueued, err := store.Get(context.Background(), queued.ID)
			return err == nil && storedQueued.Status == JobStatusFailed
		})
		waitFor(t, time.Second, func() bool {
			storedStaged, err := store.Get(context.Background(), staged.ID)
			return err == nil && storedStaged.Status == JobStatusProcessing
		})
		waitFor(t, time.Second, func() bool {
			stager.mu.Lock()
			defer stager.mu.Unlock()
			return len(stager.deleteCalls) > 0
		})

		storedQueued, _ := store.Get(context.Background(), queued.ID)
		if storedQueued.Error == nil || !storedQueued.Error.Retryable || storedQueued.Error.Code != "transient_token_required" {
			t.Fatalf("unexpected recovery error: %#v", storedQueued.Error)
		}
		storedStaged, _ := store.Get(context.Background(), staged.ID)
		if storedStaged.Status != JobStatusProcessing {
			t.Fatalf("expected staged job to remain processing, got %s", storedStaged.Status)
		}
	})

	t.Run("recovering a staging crash window deletes the stranded blob", func(t *testing.T) {
		store := newMemoryJobStore()
		clock := fixedClock{now: time.Date(2026, 7, 10, 13, 46, 9, 0, time.UTC)}
		job := JobDocument{
			SchemaVersion:  schemaVersion,
			ID:             "job-staging",
			Status:         JobStatusStaging,
			OneDriveItemID: "item-staging",
			SourceName:     "camera clip?.mp4",
			CreatedAt:      clock.Now(),
			UpdatedAt:      clock.Now(),
			StartedAt:      ptr(clock.Now()),
		}
		if err := store.Create(context.Background(), job); err != nil {
			t.Fatalf("create job: %v", err)
		}

		stager := &fakeStager{baseURL: "https://staged.example.com", stagingContainer: "video-indexer-staging"}
		manager := NewJobManager(JobManagerConfig{QueueSize: 8, WorkerConcurrency: 1}, store, nil, stager, nil, clock)
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		if err := manager.Start(ctx, 1); err != nil {
			t.Fatalf("start manager: %v", err)
		}
		t.Cleanup(manager.Close)

		waitFor(t, time.Second, func() bool {
			stored, err := store.Get(context.Background(), job.ID)
			return err == nil && stored.Status == JobStatusFailed
		})

		stored, err := store.Get(context.Background(), job.ID)
		if err != nil {
			t.Fatalf("get job: %v", err)
		}
		if stored.Error == nil || stored.Error.Code != "transient_token_required" || !stored.Error.Retryable {
			t.Fatalf("unexpected recovery error: %#v", stored.Error)
		}
		if stored.StagingContainer != "video-indexer-staging" {
			t.Fatalf("expected staging container to persist, got %q", stored.StagingContainer)
		}
		wantBlob := stageBlobName(job.ID, job.SourceName)
		if stored.StagedBlobName != wantBlob {
			t.Fatalf("expected derived staged blob %q, got %q", wantBlob, stored.StagedBlobName)
		}
		stager.mu.Lock()
		defer stager.mu.Unlock()
		if len(stager.deleteCalls) != 1 {
			t.Fatalf("expected one cleanup attempt, got %d", len(stager.deleteCalls))
		}
		if got := stager.deleteCalls[0]; got.Container != "video-indexer-staging" || got.BlobName != wantBlob {
			t.Fatalf("unexpected delete target: %#v", got)
		}
	})

	t.Run("recovery tolerates a missing staging blob", func(t *testing.T) {
		store := newMemoryJobStore()
		clock := fixedClock{now: time.Date(2026, 7, 10, 13, 46, 9, 0, time.UTC)}
		job := JobDocument{
			SchemaVersion:  schemaVersion,
			ID:             "job-missing",
			Status:         JobStatusStaging,
			OneDriveItemID: "item-missing",
			SourceName:     "missing.mp4",
			CreatedAt:      clock.Now(),
			UpdatedAt:      clock.Now(),
			StartedAt:      ptr(clock.Now()),
		}
		if err := store.Create(context.Background(), job); err != nil {
			t.Fatalf("create job: %v", err)
		}

		stager := &fakeStager{
			baseURL:          "https://staged.example.com",
			stagingContainer: "video-indexer-staging",
			deleteErr:        newServiceError(http.StatusNotFound, "blob_not_found", "blob already deleted", false),
		}
		manager := NewJobManager(JobManagerConfig{QueueSize: 8, WorkerConcurrency: 1}, store, nil, stager, nil, clock)
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		if err := manager.Start(ctx, 1); err != nil {
			t.Fatalf("start manager: %v", err)
		}
		t.Cleanup(manager.Close)

		waitFor(t, time.Second, func() bool {
			stored, err := store.Get(context.Background(), job.ID)
			return err == nil && stored.Status == JobStatusFailed
		})

		stored, err := store.Get(context.Background(), job.ID)
		if err != nil {
			t.Fatalf("get job: %v", err)
		}
		if stored.Error == nil || stored.Error.Code != "transient_token_required" || !stored.Error.Retryable {
			t.Fatalf("unexpected recovery error: %#v", stored.Error)
		}
		if stored.StagedBlobName != stageBlobName(job.ID, job.SourceName) {
			t.Fatalf("expected derived staged blob to persist, got %#v", stored.StagedBlobName)
		}
		stager.mu.Lock()
		defer stager.mu.Unlock()
		if len(stager.deleteCalls) != 1 {
			t.Fatalf("expected one cleanup attempt, got %d", len(stager.deleteCalls))
		}
	})

	t.Run("recovery surfaces cleanup failure as retryable", func(t *testing.T) {
		store := newMemoryJobStore()
		clock := fixedClock{now: time.Date(2026, 7, 10, 13, 46, 9, 0, time.UTC)}
		job := JobDocument{
			SchemaVersion:  schemaVersion,
			ID:             "job-cleanup-failure",
			Status:         JobStatusStaging,
			OneDriveItemID: "item-failure",
			SourceName:     "failure.mp4",
			CreatedAt:      clock.Now(),
			UpdatedAt:      clock.Now(),
			StartedAt:      ptr(clock.Now()),
		}
		if err := store.Create(context.Background(), job); err != nil {
			t.Fatalf("create job: %v", err)
		}

		stager := &fakeStager{
			baseURL:          "https://staged.example.com",
			stagingContainer: "video-indexer-staging",
			deleteErr:        newServiceError(http.StatusInternalServerError, "storage_error", "storage unavailable", true),
		}
		manager := NewJobManager(JobManagerConfig{QueueSize: 8, WorkerConcurrency: 1}, store, nil, stager, nil, clock)
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		if err := manager.Start(ctx, 1); err != nil {
			t.Fatalf("start manager: %v", err)
		}
		t.Cleanup(manager.Close)

		waitFor(t, time.Second, func() bool {
			stored, err := store.Get(context.Background(), job.ID)
			return err == nil && stored.Status == JobStatusFailed
		})

		stored, err := store.Get(context.Background(), job.ID)
		if err != nil {
			t.Fatalf("get job: %v", err)
		}
		if stored.Error == nil || stored.Error.Code != "staging_cleanup_failed" || !stored.Error.Retryable {
			t.Fatalf("unexpected cleanup failure error: %#v", stored.Error)
		}
		if stored.StagedBlobName != stageBlobName(job.ID, job.SourceName) {
			t.Fatalf("expected cleanup metadata to persist, got %#v", stored.StagedBlobName)
		}
		if stored.StagingContainer != "video-indexer-staging" {
			t.Fatalf("expected staging container to persist, got %q", stored.StagingContainer)
		}
		stager.mu.Lock()
		defer stager.mu.Unlock()
		if len(stager.deleteCalls) != 1 {
			t.Fatalf("expected one cleanup attempt, got %d", len(stager.deleteCalls))
		}
	})
}

func TestMemoryStoreETagConflict(t *testing.T) {
	store := newMemoryJobStore()
	job := JobDocument{
		SchemaVersion:  schemaVersion,
		ID:             "job-claim",
		Status:         JobStatusQueued,
		OneDriveItemID: "item-claim",
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
	}
	if err := store.Create(context.Background(), job); err != nil {
		t.Fatalf("create job: %v", err)
	}
	stored, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	next, _ := stored.JobDocument.next(JobStatusStaging, time.Now().UTC())
	if err := store.Update(context.Background(), next, stored.ETag); err != nil {
		t.Fatalf("first update should succeed: %v", err)
	}
	if err := store.Update(context.Background(), next, stored.ETag); err == nil {
		t.Fatal("expected stale etag conflict")
	}
}

func newTestOneDriveServer(t *testing.T, body string) *OneDriveClient {
	t.Helper()
	downloadServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "" {
			t.Fatalf("download server received authorization header: %q", got)
		}
		w.Header().Set("Content-Type", "video/mp4")
		w.Header().Set("Content-Disposition", `attachment; filename="input.mp4"`)
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(downloadServer.Close)

	graphServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got == "" {
			t.Fatal("expected authorization header")
		}
		if !strings.Contains(r.URL.Path, "/content") {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		http.Redirect(w, r, downloadServer.URL, http.StatusFound)
	}))
	t.Cleanup(graphServer.Close)
	return NewOneDriveClient(graphServer.URL, graphServer.Client())
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met before timeout")
}

func containsSequence(haystack, needle []JobStatus) bool {
	if len(needle) == 0 {
		return true
	}
	j := 0
	for _, status := range haystack {
		if status == needle[j] {
			j++
			if j == len(needle) {
				return true
			}
		}
	}
	return false
}
