package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type recordingScheduler struct {
	inputs               []VideoIndexerOrchestrationInput
	cancellationRequests []string
	err                  error
}

func (s *recordingScheduler) Schedule(_ context.Context, input VideoIndexerOrchestrationInput) error {
	s.inputs = append(s.inputs, input)
	return s.err
}

func (s *recordingScheduler) RequestCancellation(_ context.Context, jobID string) error {
	s.cancellationRequests = append(s.cancellationRequests, jobID)
	return s.err
}

func TestDurableJobServiceStagesBeforeSchedulingWithoutDelegatedCredentials(t *testing.T) {
	store := newMemoryJobStore()
	stager := &fakeStager{}
	scheduler := &recordingScheduler{}
	graph := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer delegated-token" {
			t.Fatalf("unexpected authorization header: %q", got)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("video-bytes"))
	}))
	defer graph.Close()

	service := NewDurableJobService(store, NewOneDriveClient(graph.URL, graph.Client()), stager, scheduler, fixedClock{now: time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)})
	job, err := service.CreateJob(context.Background(), CreateIndexJobRequest{
		OneDriveItemID:      "item-123",
		OneDriveAccessToken: "delegated-token",
		SourceName:          "clip.mp4",
		CorrelationID:       "correlation-123",
	})
	if err != nil {
		t.Fatalf("CreateJob() error = %v", err)
	}
	if job.Status != JobStatusStaged {
		t.Fatalf("job status = %q, want %q", job.Status, JobStatusStaged)
	}
	if len(stager.stageCalls) != 1 || len(scheduler.inputs) != 1 {
		t.Fatalf("staging calls = %d, schedules = %d; want one of each", len(stager.stageCalls), len(scheduler.inputs))
	}
	input := scheduler.inputs[0]
	if input.JobID != job.ID || input.BlobName == "" || input.Container == "" {
		t.Fatalf("unexpected durable input: %#v", input)
	}
	serialized := strings.Join([]string{input.JobID, input.Container, input.BlobName, input.SourceName, input.Correlation, input.Version}, "|")
	if strings.Contains(serialized, "delegated-token") || strings.Contains(serialized, "item-123") {
		t.Fatalf("delegated data leaked into durable input: %q", serialized)
	}
}

type inspectingStager struct {
	store     *memoryJobStore
	container string
	sawAsset  bool
}

func (s *inspectingStager) Stage(ctx context.Context, jobID, sourceName string, _ io.Reader) (StagedAsset, error) {
	stored, err := s.store.Get(ctx, jobID)
	if err != nil {
		return StagedAsset{}, err
	}
	asset := StagedAsset{Container: s.container, BlobName: stageBlobName(jobID, sourceName)}
	s.sawAsset = stored.Status == JobStatusStaging && stored.StagingContainer == asset.Container && stored.StagedBlobName == asset.BlobName
	return asset, nil
}

func (s *inspectingStager) ReadURL(context.Context, StagedAsset) (string, error) { return "", nil }
func (s *inspectingStager) Delete(context.Context, StagedAsset) error            { return nil }
func (s *inspectingStager) StagingContainer() string                             { return s.container }

func TestDurableJobServicePersistsStagedAssetReferenceBeforeUpload(t *testing.T) {
	store := newMemoryJobStore()
	stager := &inspectingStager{store: store, container: "video-indexer-staging"}
	graph := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("video-bytes"))
	}))
	defer graph.Close()

	service := NewDurableJobService(store, NewOneDriveClient(graph.URL, graph.Client()), stager, &recordingScheduler{}, fixedClock{now: time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)})
	_, err := service.CreateJob(context.Background(), CreateIndexJobRequest{
		OneDriveItemID:      "item-123",
		OneDriveAccessToken: "delegated-token",
		SourceName:          "clip.mp4",
		CorrelationID:       "persist-before-upload",
	})
	if err != nil {
		t.Fatalf("CreateJob() error = %v", err)
	}
	if !stager.sawAsset {
		t.Fatal("staging JobDocument did not contain the deterministic asset reference before upload")
	}
}

func TestDurableJobServiceReturnsOriginalStagingFailure(t *testing.T) {
	store := newMemoryJobStore()
	graph := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "upstream unavailable", http.StatusServiceUnavailable)
	}))
	defer graph.Close()

	service := NewDurableJobService(store, NewOneDriveClient(graph.URL, graph.Client()), &fakeStager{}, &recordingScheduler{}, fixedClock{now: time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)})
	correlationID := "failed-staging"
	_, err := service.CreateJob(context.Background(), CreateIndexJobRequest{
		OneDriveItemID:      "item-123",
		OneDriveAccessToken: "delegated-token",
		SourceName:          "clip.mp4",
		CorrelationID:       correlationID,
	})
	if err == nil || serviceErrorCode(err) == "" {
		t.Fatalf("CreateJob() error = %v, want original staging failure", err)
	}
	stored, getErr := store.Get(context.Background(), newJobID(correlationID))
	if getErr != nil {
		t.Fatalf("Get() error = %v", getErr)
	}
	if stored.Status != JobStatusFailed || stored.Error == nil || stored.Error.Code != serviceErrorCode(err) {
		t.Fatalf("unexpected failed projection: %#v", stored.JobDocument)
	}
}
func TestDurableJobServiceReconcileCancellationDeletesBeforeTerminalProjection(t *testing.T) {
	store := newMemoryJobStore()
	clock := fixedClock{now: time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)}
	job := JobDocument{
		SchemaVersion:    schemaVersion,
		ID:               "job-123",
		Status:           JobStatusIndexing,
		OneDriveItemID:   "item-123",
		StagingContainer: "video-indexer-staging",
		StagedBlobName:   "jobs/job-123/clip.mp4",
		CreatedAt:        clock.Now(),
		UpdatedAt:        clock.Now(),
	}

	if err := store.Create(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	stager := &fakeStager{}
	service := NewDurableJobService(store, nil, stager, &recordingScheduler{}, clock)
	if err := service.ReconcileCancellation(context.Background(), job.ID); err != nil {
		t.Fatalf("ReconcileCancellation() error = %v", err)
	}
	stored, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != JobStatusCanceled || stored.Error == nil || stored.Error.Code != "canceled" {
		t.Fatalf("unexpected terminal projection: %#v", stored.JobDocument)
	}
	if len(stager.deleteCalls) != 1 || stager.deleteCalls[0].BlobName != job.StagedBlobName {
		t.Fatalf("unexpected cleanup calls: %#v", stager.deleteCalls)
	}
}

func TestDurableJobServicePersistsCancellationGraceBeforeSchedulingWatchdog(t *testing.T) {
	store := newMemoryJobStore()
	clock := fixedClock{now: time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)}
	job := JobDocument{
		SchemaVersion:  schemaVersion,
		ID:             "job-123",
		Status:         JobStatusIndexing,
		OneDriveItemID: "item-123",
		CreatedAt:      clock.Now(),
		UpdatedAt:      clock.Now(),
	}
	if err := store.Create(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	scheduler := &recordingScheduler{}
	grace := 45 * time.Second
	service := NewDurableJobService(store, nil, &fakeStager{}, scheduler, clock, grace)

	if _, err := service.CancelJob(context.Background(), job.ID); err != nil {
		t.Fatalf("CancelJob() error = %v", err)
	}
	stored, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.CancellationRequestedAt == nil || stored.CancellationGrace != grace {
		t.Fatalf("cancellation projection = %#v, want requested timestamp and %s grace", stored.JobDocument, grace)
	}
	if got := scheduler.cancellationRequests; len(got) != 1 || got[0] != job.ID {
		t.Fatalf("cancellation requests = %#v, want [%q]", got, job.ID)
	}
}

func TestDurableJobServiceSchedulingFailureKeepsStagedJobForRecovery(t *testing.T) {
	store := newMemoryJobStore()
	stager := &fakeStager{}
	scheduler := &recordingScheduler{err: errors.New("DTS unavailable")}
	graph := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("video-bytes"))
	}))
	defer graph.Close()

	service := NewDurableJobService(store, NewOneDriveClient(graph.URL, graph.Client()), stager, scheduler, fixedClock{now: time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)})
	_, err := service.CreateJob(context.Background(), CreateIndexJobRequest{
		OneDriveItemID: "item-123", OneDriveAccessToken: "delegated-token", SourceName: "clip.mp4", CorrelationID: "schedule-failure",
	})
	if err == nil || serviceErrorCode(err) != "orchestration_schedule_failed" {
		t.Fatalf("CreateJob() error = %v, want orchestration_schedule_failed", err)
	}
	if len(stager.deleteCalls) != 0 {
		t.Fatalf("staged asset cleanup calls = %d, want 0", len(stager.deleteCalls))
	}
	stored, err := store.Get(context.Background(), scheduler.inputs[0].JobID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != JobStatusStaged {
		t.Fatalf("projection status = %q, want %q", stored.Status, JobStatusStaged)
	}

	scheduler.err = nil
	if err := service.ReconcileStaged(context.Background()); err != nil {
		t.Fatalf("ReconcileStaged() error = %v", err)
	}
	if len(scheduler.inputs) != 2 {
		t.Fatalf("schedule calls = %d, want 2", len(scheduler.inputs))
	}
}

func TestDurableJobServiceCorrelationRetryReschedulesStagedJob(t *testing.T) {
	store := newMemoryJobStore()
	clock := fixedClock{now: time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)}
	job := JobDocument{SchemaVersion: schemaVersion, ID: newJobID("correlation-123"), Status: JobStatusStaged, OneDriveItemID: "item-123", SourceName: "clip.mp4", CorrelationID: "correlation-123", StagingContainer: "video-indexer-staging", StagedBlobName: "jobs/job-123/clip.mp4", CreatedAt: clock.Now(), UpdatedAt: clock.Now()}
	if err := store.Create(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	scheduler := &recordingScheduler{}
	service := NewDurableJobService(store, nil, &fakeStager{}, scheduler, clock)
	result, err := service.CreateJob(context.Background(), CreateIndexJobRequest{OneDriveItemID: "item-123", OneDriveAccessToken: "delegated-token", SourceName: "clip.mp4", CorrelationID: "correlation-123"})
	if err != nil {
		t.Fatalf("CreateJob() retry error = %v", err)
	}
	if result.ID != job.ID || len(scheduler.inputs) != 1 || scheduler.inputs[0].JobID != job.ID {
		t.Fatalf("retry result = %#v, schedules = %#v", result, scheduler.inputs)
	}
}
func TestDurableJobServiceCancelDuringStagingCleansBeforeProjection(t *testing.T) {
	store := newMemoryJobStore()
	clock := fixedClock{now: time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)}
	job := JobDocument{SchemaVersion: schemaVersion, ID: "job-123", Status: JobStatusStaging, OneDriveItemID: "item-123", StagingContainer: "video-indexer-staging", StagedBlobName: "jobs/job-123/clip.mp4", CreatedAt: clock.Now(), UpdatedAt: clock.Now()}
	if err := store.Create(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	stager := &fakeStager{}
	service := NewDurableJobService(store, nil, stager, &recordingScheduler{}, clock)
	canceled, err := service.CancelJob(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("CancelJob() error = %v", err)
	}
	if canceled.Status != JobStatusCanceled || len(stager.deleteCalls) != 1 {
		t.Fatalf("cancel result = %#v; cleanup calls = %#v", canceled, stager.deleteCalls)
	}
}

func TestDurableJobServiceCancelDuringStagingDoesNotProjectCanceledWhenCleanupFails(t *testing.T) {
	store := newMemoryJobStore()
	clock := fixedClock{now: time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)}
	job := JobDocument{SchemaVersion: schemaVersion, ID: "job-123", Status: JobStatusStaging, OneDriveItemID: "item-123", StagingContainer: "video-indexer-staging", StagedBlobName: "jobs/job-123/clip.mp4", CreatedAt: clock.Now(), UpdatedAt: clock.Now()}
	if err := store.Create(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	service := NewDurableJobService(store, nil, &fakeStager{deleteErr: errors.New("storage unavailable")}, &recordingScheduler{}, clock)
	_, err := service.CancelJob(context.Background(), job.ID)
	if err == nil || serviceErrorCode(err) != "staging_cleanup_failed" {
		t.Fatalf("CancelJob() error = %v, want staging_cleanup_failed", err)
	}
	stored, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != JobStatusStaging {
		t.Fatalf("status = %q, want %q after failed cleanup", stored.Status, JobStatusStaging)
	}
}
