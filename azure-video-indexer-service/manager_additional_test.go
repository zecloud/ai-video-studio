package main

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func TestJobManagerCreateJobIsIdempotentByCorrelationID(t *testing.T) {
	store := newMemoryJobStore()
	manager := NewJobManager(JobManagerConfig{QueueSize: 8, WorkerConcurrency: 1}, store, nil, nil, nil, fixedClock{now: time.Date(2026, 7, 10, 13, 46, 9, 0, time.UTC)})

	req := CreateIndexJobRequest{
		OneDriveItemID:      "item-001",
		OneDriveAccessToken: "token-abc",
		SourceName:          "action-4-clip.mp4",
		CorrelationID:       "batch-001",
	}

	first, err := manager.CreateJob(context.Background(), req)
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	second, err := manager.CreateJob(context.Background(), req)
	if err != nil {
		t.Fatalf("second create: %v", err)
	}

	if first.ID != second.ID {
		t.Fatalf("expected idempotent job ids, got %q and %q", first.ID, second.ID)
	}
	if first.Status != JobStatusQueued || second.Status != JobStatusQueued {
		t.Fatalf("unexpected statuses: %#v %#v", first, second)
	}

	jobs, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("expected one stored job, got %d", len(jobs))
	}
	if jobs[0].CorrelationID != req.CorrelationID {
		t.Fatalf("correlation id was not persisted: %#v", jobs[0].JobDocument)
	}
}

func TestManagerDefersCleanupUntilPipelineReturns(t *testing.T) {
	store := newMemoryJobStore()
	stager := &fakeStager{baseURL: "https://staged.example.com"}
	oneDrive := newTestOneDriveServer(t, "download-video-bytes")
	pipeline := &fakePipeline{
		started: make(chan struct{}),
		release: make(chan struct{}),
		outcome: PipelineOutcome{Kind: PipelineOutcomeCompleted},
	}

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

	stager.mu.Lock()
	if len(stager.deleteCalls) != 0 {
		stager.mu.Unlock()
		t.Fatal("cleanup happened before the pipeline returned")
	}
	stager.mu.Unlock()

	close(pipeline.release)
	waitFor(t, time.Second, func() bool {
		stored, err := store.Get(context.Background(), job.ID)
		return err == nil && stored.Status == JobStatusSucceeded
	})

	stager.mu.Lock()
	defer stager.mu.Unlock()
	if len(stager.deleteCalls) == 0 {
		t.Fatal("expected cleanup after pipeline returned")
	}
}

func TestManagerTerminalVideoIndexFailureCleansUpWithoutNormalizing(t *testing.T) {
	store := newMemoryJobStore()
	stager := &fakeStager{baseURL: "https://staged.example.com"}
	oneDrive := newTestOneDriveServer(t, "download-video-bytes")
	normalizer := &fakeVideoNormalizer{}
	planner := &fakeEditPlanner{plan: testEditPlan("video-123", "item-001")}
	client := &failingVideoIndexerClient{uploadVideoID: "video-123"}
	pipeline := NewAzureVideoIndexerPipeline(client, normalizer, planner, fixedClock{now: time.Date(2026, 7, 10, 13, 46, 9, 0, time.UTC)})
	manager := NewJobManager(JobManagerConfig{QueueSize: 8, WorkerConcurrency: 1}, store, oneDrive, stager, pipeline, fixedClock{now: time.Date(2026, 7, 10, 13, 46, 9, 0, time.UTC)})

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := manager.Start(ctx, 1); err != nil {
		t.Fatalf("start manager: %v", err)
	}
	t.Cleanup(manager.Close)

	job, err := manager.CreateJob(context.Background(), CreateIndexJobRequest{
		OneDriveItemID:      "item-003",
		OneDriveAccessToken: "token-fail",
		SourceName:          "fail.mp4",
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	waitFor(t, time.Second, func() bool {
		stored, err := store.Get(context.Background(), job.ID)
		return err == nil && stored.Status == JobStatusFailed
	})

	stored, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if stored.Error == nil || stored.Error.Code != "video_index_failed" {
		t.Fatalf("unexpected error: %#v", stored.Error)
	}
	if normalizer.calls != 0 {
		t.Fatalf("normalizer should not have been called, got %d", normalizer.calls)
	}
	if planner.calls != 0 {
		t.Fatalf("planner should not have been called, got %d", planner.calls)
	}
	stager.mu.Lock()
	defer stager.mu.Unlock()
	if len(stager.deleteCalls) == 0 {
		t.Fatal("expected cleanup after terminal video index failure")
	}
}

type failingVideoIndexerClient struct {
	uploadVideoID string
	uploadCalls   int
	pollCalls     int
}

func (c *failingVideoIndexerClient) UploadVideoURL(ctx context.Context, readURL, sourceName, externalID string) (string, error) {
	c.uploadCalls++
	return c.uploadVideoID, nil
}

func (c *failingVideoIndexerClient) PollVideoIndex(ctx context.Context, videoID string, timeout time.Duration) (VideoIndexData, error) {
	c.pollCalls++
	return nil, &ServiceError{Status: http.StatusUnprocessableEntity, Code: "video_index_failed", Message: "video indexer reported terminal state failed", Retryable: false}
}

func (c *failingVideoIndexerClient) PollTimeout() time.Duration {
	return time.Minute
}
