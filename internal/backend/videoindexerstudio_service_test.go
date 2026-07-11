package backend

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zecloud/ai-video-studio/internal/editing"
	"github.com/zecloud/ai-video-studio/internal/library"
	"github.com/zecloud/ai-video-studio/internal/onedrive"
	"github.com/zecloud/ai-video-studio/internal/settings"
	"github.com/zecloud/ai-video-studio/internal/videoindexerstudio"
)

type fakeVideoIndexerClient struct {
	mu           sync.Mutex
	createReqs   []videoindexerstudio.CreateJobRequest
	createResp   *videoindexerstudio.JobResponse
	createScript []scriptedCreateOutcome
	createErr    error
	getResp      map[string][]videoindexerstudio.JobResponse
	getScript    map[string][]scriptedGetOutcome
	getErr       error
	cancelResp   *videoindexerstudio.JobResponse
	cancelErr    error
	cancelCalls  []string
}

type scriptedGetOutcome struct {
	resp *videoindexerstudio.JobResponse
	err  error
}

type scriptedCreateOutcome struct {
	resp *videoindexerstudio.JobResponse
	err  error
}

func (f *fakeVideoIndexerClient) CreateJob(_ context.Context, req videoindexerstudio.CreateJobRequest) (*videoindexerstudio.JobResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createReqs = append(f.createReqs, req)
	if len(f.createScript) > 0 {
		outcome := f.createScript[0]
		if len(f.createScript) == 1 {
			f.createScript = nil
		} else {
			f.createScript = f.createScript[1:]
		}
		if outcome.err != nil {
			return nil, outcome.err
		}
		if outcome.resp != nil {
			resp := *outcome.resp
			return &resp, nil
		}
	}
	if f.createErr != nil {
		return nil, f.createErr
	}
	if f.createResp != nil {
		resp := *f.createResp
		return &resp, nil
	}
	resp := videoindexerstudio.JobResponse{Job: videoindexerstudio.Job{ID: "remote-job-1", Status: videoindexerstudio.JobStatusQueued}}
	return &resp, nil
}

func (f *fakeVideoIndexerClient) GetJob(_ context.Context, id string) (*videoindexerstudio.JobResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if script := f.getScript[id]; len(script) > 0 {
		outcome := script[0]
		if len(script) == 1 {
			delete(f.getScript, id)
		} else {
			f.getScript[id] = script[1:]
		}
		if outcome.err != nil {
			return nil, outcome.err
		}
		if outcome.resp != nil {
			resp := *outcome.resp
			return &resp, nil
		}
	}
	if f.getErr != nil {
		return nil, f.getErr
	}
	if seq := f.getResp[id]; len(seq) > 0 {
		resp := seq[0]
		if len(seq) == 1 {
			delete(f.getResp, id)
		} else {
			f.getResp[id] = seq[1:]
		}
		return &resp, nil
	}
	return &videoindexerstudio.JobResponse{Job: videoindexerstudio.Job{ID: id, Status: videoindexerstudio.JobStatusSucceeded}}, nil
}

func (f *fakeVideoIndexerClient) CancelJob(_ context.Context, id string) (*videoindexerstudio.JobResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancelCalls = append(f.cancelCalls, id)
	if f.cancelErr != nil {
		return nil, f.cancelErr
	}
	if f.cancelResp != nil {
		resp := *f.cancelResp
		return &resp, nil
	}
	resp := videoindexerstudio.JobResponse{Job: videoindexerstudio.Job{ID: id, Status: videoindexerstudio.JobStatusCanceled}}
	return &resp, nil
}

type fakeOneDriveSource struct {
	client *onedrive.Client
}

func (f fakeOneDriveSource) DriveClient() *onedrive.Client {
	return f.client
}

type fakeLibraryStore struct {
	assets           []library.ProjectAsset
	saveJobsCalled   int
	saveAssetsCalled int
	jobs             []library.AnalysisJob
}

func (f *fakeLibraryStore) LoadAssets(context.Context) ([]library.ProjectAsset, error) {
	out := make([]library.ProjectAsset, len(f.assets))
	copy(out, f.assets)
	return out, nil
}

func (f *fakeLibraryStore) SaveAssets(_ context.Context, assets []library.ProjectAsset) error {
	f.saveAssetsCalled++
	f.assets = append([]library.ProjectAsset(nil), assets...)
	return nil
}

func (f *fakeLibraryStore) AddAsset(_ context.Context, asset library.ProjectAsset) error {
	f.assets = append(f.assets, asset)
	return nil
}

func (f *fakeLibraryStore) SaveJob(_ context.Context, job library.AnalysisJob) error {
	f.saveJobsCalled++
	for i := range f.jobs {
		if f.jobs[i].ID == job.ID {
			f.jobs[i] = job
			return nil
		}
	}
	f.jobs = append(f.jobs, job)
	return nil
}

func (f *fakeLibraryStore) LoadJobs(context.Context) ([]library.AnalysisJob, error) {
	out := make([]library.AnalysisJob, len(f.jobs))
	copy(out, f.jobs)
	return out, nil
}

func (f *fakeLibraryStore) Path() string { return "" }

type fakeEditingSaver struct {
	saved []editing.EditProject
}

func (f *fakeEditingSaver) SaveProject(_ context.Context, project editing.EditProject) (editing.EditProject, error) {
	f.saved = append(f.saved, project)
	return project, nil
}

func newBackendTestDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp(".", "backend-video-indexer-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func TestVideoIndexerSubmitForIndexingAssetNotFound(t *testing.T) {
	svc := NewVideoIndexerStudioService(&fakeLibraryStore{}, nil, &fakeEditingSaver{}, &fakeVideoIndexerClient{})
	_, err := svc.SubmitForIndexing(context.Background(), "missing")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected asset not found error, got %v", err)
	}
}

func TestVideoIndexerSubmitForIndexingAuthMissing(t *testing.T) {
	client := &fakeVideoIndexerClient{}
	lib := &fakeLibraryStore{assets: []library.ProjectAsset{{ID: "asset-1", Name: "clip.mp4", CloudAssetID: "drive-item-1"}}}
	svc := NewVideoIndexerStudioService(lib, nil, &fakeEditingSaver{}, client)
	_, err := svc.SubmitForIndexing(context.Background(), "asset-1")
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "sign in") {
		t.Fatalf("expected sign-in error, got %v", err)
	}
	if len(client.createReqs) != 0 {
		t.Fatal("video indexer client should not be called when OneDrive auth is missing")
	}
}

func TestVideoIndexerSubmitForIndexingPersistsSeparately(t *testing.T) {
	dir := newBackendTestDir(t)
	store := &fileVideoIndexerJobStore{path: filepath.Join(dir, videoIndexerJobsFileName)}
	lib := &fakeLibraryStore{assets: []library.ProjectAsset{{ID: "asset-1", Name: "clip.mp4", CloudAssetID: "drive-item-1"}}}
	edit := &fakeEditingSaver{}
	client := &fakeVideoIndexerClient{createResp: &videoindexerstudio.JobResponse{Job: videoindexerstudio.Job{ID: "remote-job-1", Status: videoindexerstudio.JobStatusQueued}}}
	drive := &onedrive.Client{
		TokenProvider: staticTokenProvider{token: "delegated-token"},
		Scopes:        []string{onedrive.GraphScopeFilesReadWriteAppFolder},
	}

	svc := NewVideoIndexerStudioService(lib, fakeOneDriveSource{client: drive}, edit, client, store)
	svc.now = func() time.Time { return time.Date(2026, 7, 10, 15, 4, 5, 0, time.UTC) }

	job, err := svc.SubmitForIndexing(context.Background(), "asset-1")
	if err != nil {
		t.Fatalf("SubmitForIndexing: %v", err)
	}
	if job.Status != videoIndexerJobStatusSubmitted {
		t.Fatalf("job status = %q, want submitted", job.Status)
	}
	if len(client.createReqs) != 1 {
		t.Fatalf("expected 1 create request, got %d", len(client.createReqs))
	}
	if got := client.createReqs[0].OneDriveItemID; got != "drive-item-1" {
		t.Fatalf("create request item id = %q", got)
	}
	if got := client.createReqs[0].OneDriveAccessToken; got != "delegated-token" {
		t.Fatalf("create request access token = %q", got)
	}
	if got := client.createReqs[0].SourceName; got != "clip.mp4" {
		t.Fatalf("create request source name = %q", got)
	}
	if lib.saveJobsCalled != 0 {
		t.Fatalf("CU analysis job store was mutated: saveJobsCalled=%d", lib.saveJobsCalled)
	}
	data, err := os.ReadFile(filepath.Join(dir, videoIndexerJobsFileName))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if bytes.Contains(data, []byte("delegated-token")) {
		t.Fatal("job store leaked the access token")
	}
	if _, err := os.Stat(filepath.Join(dir, "analysis-jobs.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("analysis-jobs.json should not be written, got err=%v", err)
	}
}

func TestVideoIndexerSubmitForIndexingUsesPerSubmissionCorrelationID(t *testing.T) {
	dir := newBackendTestDir(t)
	store := &fileVideoIndexerJobStore{path: filepath.Join(dir, videoIndexerJobsFileName)}
	lib := &fakeLibraryStore{assets: []library.ProjectAsset{{ID: "asset-1", Name: "clip.mp4", CloudAssetID: "drive-item-1"}}}
	edit := &fakeEditingSaver{}
	client := &fakeVideoIndexerClient{
		createScript: []scriptedCreateOutcome{
			{resp: &videoindexerstudio.JobResponse{Job: videoindexerstudio.Job{ID: "remote-job-1", Status: videoindexerstudio.JobStatusQueued}}},
			{resp: &videoindexerstudio.JobResponse{Job: videoindexerstudio.Job{ID: "remote-job-2", Status: videoindexerstudio.JobStatusQueued}}},
		},
	}
	drive := &onedrive.Client{
		TokenProvider: staticTokenProvider{token: "delegated-token"},
		Scopes:        []string{onedrive.GraphScopeFilesReadWriteAppFolder},
	}

	svc := NewVideoIndexerStudioService(lib, fakeOneDriveSource{client: drive}, edit, client, store)
	svc.now = func() time.Time { return time.Date(2026, 7, 10, 15, 4, 5, 0, time.UTC) }

	first, err := svc.SubmitForIndexing(context.Background(), "asset-1")
	if err != nil {
		t.Fatalf("first SubmitForIndexing: %v", err)
	}
	second, err := svc.SubmitForIndexing(context.Background(), "asset-1")
	if err != nil {
		t.Fatalf("second SubmitForIndexing: %v", err)
	}

	if first.ID == "" || second.ID == "" {
		t.Fatalf("expected non-empty job ids: %#v %#v", first, second)
	}
	if first.ID == second.ID {
		t.Fatalf("expected distinct local job ids: %q", first.ID)
	}
	if len(client.createReqs) != 2 {
		t.Fatalf("expected 2 create requests, got %d", len(client.createReqs))
	}
	if got := client.createReqs[0].CorrelationID; got != first.ID {
		t.Fatalf("first correlation id = %q, want %q", got, first.ID)
	}
	if got := client.createReqs[1].CorrelationID; got != second.ID {
		t.Fatalf("second correlation id = %q, want %q", got, second.ID)
	}
	if client.createReqs[0].CorrelationID == client.createReqs[1].CorrelationID {
		t.Fatalf("expected distinct correlation ids, got %q", client.createReqs[0].CorrelationID)
	}
	if first.RemoteJobID != "remote-job-1" || second.RemoteJobID != "remote-job-2" {
		t.Fatalf("unexpected remote job linkage: %#v %#v", first, second)
	}
	stored, err := store.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(stored) != 2 {
		t.Fatalf("expected 2 stored jobs, got %d", len(stored))
	}
	seen := map[string]string{}
	for _, job := range stored {
		seen[job.ID] = job.RemoteJobID
	}
	if seen[first.ID] != "remote-job-1" || seen[second.ID] != "remote-job-2" {
		t.Fatalf("unexpected stored linkage: %#v", seen)
	}
}

func TestVideoIndexerJobStoreLoadsLegacySlice(t *testing.T) {
	dir := newBackendTestDir(t)
	store := &fileVideoIndexerJobStore{path: filepath.Join(dir, videoIndexerJobsFileName)}
	legacy := []byte(`[{"id":"legacy-job","assetId":"asset-1","status":"submitted","createdAt":"2026-07-10T15:04:05Z","updatedAt":"2026-07-10T15:04:05Z"}]`)
	if err := os.WriteFile(store.path, legacy, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	jobs, err := store.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(jobs) != 1 || jobs[0].ID != "legacy-job" || jobs[0].Status != videoIndexerJobStatusSubmitted {
		t.Fatalf("unexpected jobs: %#v", jobs)
	}
}

func TestVideoIndexerPollingTerminalStates(t *testing.T) {
	lib := &fakeLibraryStore{assets: []library.ProjectAsset{{ID: "asset-1", Name: "clip.mp4", CloudAssetID: "drive-item-1"}}}
	edit := &fakeEditingSaver{}
	jobStore := &memoryVideoIndexerJobStore{}
	client := &fakeVideoIndexerClient{
		createResp: &videoindexerstudio.JobResponse{Job: videoindexerstudio.Job{ID: "remote-job-1", Status: videoindexerstudio.JobStatusQueued}},
		getResp: map[string][]videoindexerstudio.JobResponse{
			"remote-job-1": {
				{Job: videoindexerstudio.Job{ID: "remote-job-1", Status: videoindexerstudio.JobStatusRunning}},
				{Job: videoindexerstudio.Job{ID: "remote-job-1", Status: videoindexerstudio.JobStatusSucceeded}},
			},
		},
	}
	drive := &onedrive.Client{
		TokenProvider: staticTokenProvider{token: "delegated-token"},
		Scopes:        []string{onedrive.GraphScopeFilesReadWriteAppFolder},
	}
	svc := NewVideoIndexerStudioService(lib, fakeOneDriveSource{client: drive}, edit, client, jobStore)
	svc.now = func() time.Time { return time.Date(2026, 7, 10, 15, 4, 5, 0, time.UTC) }

	submitted, err := svc.SubmitForIndexing(context.Background(), "asset-1")
	if err != nil {
		t.Fatalf("SubmitForIndexing: %v", err)
	}
	if submitted.Status != videoIndexerJobStatusSubmitted {
		t.Fatalf("submit status = %q, want submitted", submitted.Status)
	}
	first, err := svc.IndexingJob(context.Background(), submitted.ID)
	if err != nil {
		t.Fatalf("IndexingJob first poll: %v", err)
	}
	if first.Status != videoIndexerJobStatusPolling {
		t.Fatalf("first poll status = %q, want polling", first.Status)
	}
	second, err := svc.IndexingJob(context.Background(), submitted.ID)
	if err != nil {
		t.Fatalf("IndexingJob second poll: %v", err)
	}
	if second.Status != videoIndexerJobStatusSucceeded {
		t.Fatalf("second poll status = %q, want succeeded", second.Status)
	}
}

func TestVideoIndexerPollingRetryableErrorsKeepPolling(t *testing.T) {
	lib := &fakeLibraryStore{assets: []library.ProjectAsset{{ID: "asset-1", Name: "clip.mp4", CloudAssetID: "drive-item-1"}}}
	edit := &fakeEditingSaver{}
	jobStore := &memoryVideoIndexerJobStore{}
	client := &fakeVideoIndexerClient{
		createResp: &videoindexerstudio.JobResponse{Job: videoindexerstudio.Job{ID: "remote-job-1", Status: videoindexerstudio.JobStatusQueued}},
		getScript: map[string][]scriptedGetOutcome{
			"remote-job-1": {
				{resp: &videoindexerstudio.JobResponse{Job: videoindexerstudio.Job{ID: "remote-job-1", Status: videoindexerstudio.JobStatusRunning}}},
				{err: &videoindexerstudio.ResponseError{StatusCode: http.StatusServiceUnavailable, APIErrorResponse: videoindexerstudio.APIErrorResponse{Code: "service_unavailable", Message: "temporary outage", Retryable: true}}},
				{resp: &videoindexerstudio.JobResponse{Job: videoindexerstudio.Job{ID: "remote-job-1", Status: videoindexerstudio.JobStatusSucceeded, VideoIndexerVideoID: "video-123", VideoIndexResult: &videoindexerstudio.VideoIndexResult{VideoID: "video-123", State: "Processed"}}}},
			},
		},
	}
	drive := &onedrive.Client{
		TokenProvider: staticTokenProvider{token: "delegated-token"},
		Scopes:        []string{onedrive.GraphScopeFilesReadWriteAppFolder},
	}
	svc := NewVideoIndexerStudioService(lib, fakeOneDriveSource{client: drive}, edit, client, jobStore)
	svc.now = func() time.Time { return time.Date(2026, 7, 10, 15, 4, 5, 0, time.UTC) }

	submitted, err := svc.SubmitForIndexing(context.Background(), "asset-1")
	if err != nil {
		t.Fatalf("SubmitForIndexing: %v", err)
	}
	if submitted.Status != videoIndexerJobStatusSubmitted {
		t.Fatalf("submit status = %q, want submitted", submitted.Status)
	}

	first, err := svc.IndexingJobs(context.Background())
	if err != nil {
		t.Fatalf("IndexingJobs first pass: %v", err)
	}
	if len(first) != 1 || first[0].Status != videoIndexerJobStatusPolling {
		t.Fatalf("first pass jobs: %#v", first)
	}

	second, err := svc.IndexingJobs(context.Background())
	if err != nil {
		t.Fatalf("IndexingJobs second pass: %v", err)
	}
	if len(second) != 1 || second[0].Status != videoIndexerJobStatusPolling {
		t.Fatalf("second pass jobs: %#v", second)
	}
	if second[0].ErrorMessage != "temporary outage" || !second[0].Retryable {
		t.Fatalf("retryable error was not preserved: %#v", second[0])
	}

	third, err := svc.IndexingJobs(context.Background())
	if err != nil {
		t.Fatalf("IndexingJobs third pass: %v", err)
	}
	if len(third) != 1 || third[0].Status != videoIndexerJobStatusSucceeded {
		t.Fatalf("third pass jobs: %#v", third)
	}
	if third[0].ErrorMessage != "" || third[0].Retryable {
		t.Fatalf("terminal success should clear retry diagnostics: %#v", third[0])
	}
}

func TestVideoIndexerPollingNonRetryableErrorsFailAndStopPolling(t *testing.T) {
	lib := &fakeLibraryStore{assets: []library.ProjectAsset{{ID: "asset-1", Name: "clip.mp4", CloudAssetID: "drive-item-1"}}}
	edit := &fakeEditingSaver{}
	jobStore := &memoryVideoIndexerJobStore{}
	client := &fakeVideoIndexerClient{
		createResp: &videoindexerstudio.JobResponse{Job: videoindexerstudio.Job{ID: "remote-job-1", Status: videoindexerstudio.JobStatusQueued}},
		getScript: map[string][]scriptedGetOutcome{
			"remote-job-1": {
				{resp: &videoindexerstudio.JobResponse{Job: videoindexerstudio.Job{ID: "remote-job-1", Status: videoindexerstudio.JobStatusRunning}}},
				{err: &videoindexerstudio.ResponseError{StatusCode: http.StatusBadRequest, APIErrorResponse: videoindexerstudio.APIErrorResponse{Code: "bad_request", Message: "bad input", Retryable: false}}},
				{resp: &videoindexerstudio.JobResponse{Job: videoindexerstudio.Job{ID: "remote-job-1", Status: videoindexerstudio.JobStatusSucceeded}}},
			},
		},
	}
	drive := &onedrive.Client{
		TokenProvider: staticTokenProvider{token: "delegated-token"},
		Scopes:        []string{onedrive.GraphScopeFilesReadWriteAppFolder},
	}
	svc := NewVideoIndexerStudioService(lib, fakeOneDriveSource{client: drive}, edit, client, jobStore)
	svc.now = func() time.Time { return time.Date(2026, 7, 10, 15, 4, 5, 0, time.UTC) }

	submitted, err := svc.SubmitForIndexing(context.Background(), "asset-1")
	if err != nil {
		t.Fatalf("SubmitForIndexing: %v", err)
	}
	if submitted.Status != videoIndexerJobStatusSubmitted {
		t.Fatalf("submit status = %q, want submitted", submitted.Status)
	}

	first, err := svc.IndexingJob(context.Background(), submitted.ID)
	if err != nil {
		t.Fatalf("IndexingJob first pass: %v", err)
	}
	if first.Status != videoIndexerJobStatusPolling {
		t.Fatalf("first pass status = %q, want polling", first.Status)
	}

	second, err := svc.IndexingJob(context.Background(), submitted.ID)
	if err != nil {
		t.Fatalf("IndexingJob second pass: %v", err)
	}
	if second.Status != videoIndexerJobStatusFailed {
		t.Fatalf("second pass status = %q, want failed", second.Status)
	}
	if second.ErrorMessage != "bad input" || second.Retryable {
		t.Fatalf("non-retryable error was not persisted: %#v", second)
	}

	third, err := svc.IndexingJob(context.Background(), submitted.ID)
	if err != nil {
		t.Fatalf("IndexingJob third pass: %v", err)
	}
	if third.Status != videoIndexerJobStatusFailed {
		t.Fatalf("third pass status = %q, want failed", third.Status)
	}
	if third.ErrorMessage != "bad input" || third.Retryable {
		t.Fatalf("terminal failure should keep diagnostics: %#v", third)
	}

	client.mu.Lock()
	remaining := len(client.getScript["remote-job-1"])
	client.mu.Unlock()
	if remaining != 1 {
		t.Fatalf("future polling should stop after failure, remaining scripted calls=%d", remaining)
	}
}

func TestVideoIndexerCancelIndexing(t *testing.T) {
	lib := &fakeLibraryStore{assets: []library.ProjectAsset{{ID: "asset-1", Name: "clip.mp4", CloudAssetID: "drive-item-1"}}}
	edit := &fakeEditingSaver{}
	jobStore := &memoryVideoIndexerJobStore{}
	draft := videoindexerstudio.TimelineDraft{
		SchemaVersion: videoindexerstudio.TimelineDraftSchemaVersion,
		OriginJobID:   "remote-job-1",
		SuggestionID:  "suggestion-1",
		PromptVersion: "v1",
		PrimaryVideoTrack: videoindexerstudio.TimelineTrack{
			ID:   "primary-video",
			Kind: videoindexerstudio.TimelineTrackKindVideo,
			Clips: []videoindexerstudio.TimelineClip{
				{
					ID:              "clip-1",
					SourceAssetID:   "asset-1",
					InMS:            0,
					OutMS:           1000,
					TimelineStartMS: 0,
					DurationMS:      1000,
					Transition:      videoindexerstudio.TimelineTransition{Kind: videoindexerstudio.TimelineTransitionKindCut},
				},
			},
		},
	}
	client := &fakeVideoIndexerClient{
		createResp: &videoindexerstudio.JobResponse{Job: videoindexerstudio.Job{
			ID:                  "remote-job-1",
			Status:              videoindexerstudio.JobStatusQueued,
			VideoIndexerVideoID: "video-123",
			VideoIndexResult:    &videoindexerstudio.VideoIndexResult{VideoID: "video-123", State: "Processed"},
			EditPlan:            &videoindexerstudio.EditPlan{SchemaVersion: 1, VideoID: "video-123", AssetID: "asset-1", Title: "Title", Summary: "Summary"},
			TimelineDrafts:      []videoindexerstudio.TimelineDraft{draft},
		}},
		cancelResp: &videoindexerstudio.JobResponse{Job: videoindexerstudio.Job{
			ID:                  "remote-job-1",
			Status:              videoindexerstudio.JobStatusSucceeded,
			VideoIndexerVideoID: "video-123",
			VideoIndexResult:    &videoindexerstudio.VideoIndexResult{VideoID: "video-123", State: "Processed"},
			EditPlan:            &videoindexerstudio.EditPlan{SchemaVersion: 1, VideoID: "video-123", AssetID: "asset-1", Title: "Title", Summary: "Summary"},
			TimelineDrafts:      []videoindexerstudio.TimelineDraft{draft},
		}},
	}
	drive := &onedrive.Client{
		TokenProvider: staticTokenProvider{token: "delegated-token"},
		Scopes:        []string{onedrive.GraphScopeFilesReadWriteAppFolder},
	}
	svc := NewVideoIndexerStudioService(lib, fakeOneDriveSource{client: drive}, edit, client, jobStore)

	job, err := svc.SubmitForIndexing(context.Background(), "asset-1")
	if err != nil {
		t.Fatalf("SubmitForIndexing: %v", err)
	}
	cancelled, err := svc.CancelIndexing(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("CancelIndexing: %v", err)
	}
	if cancelled.Status != videoIndexerJobStatusSucceeded {
		t.Fatalf("cancel status = %q, want succeeded", cancelled.Status)
	}
	if cancelled.VideoIndexerVideoID != "video-123" || cancelled.VideoIndexResult == nil || cancelled.VideoIndexResult.VideoID != "video-123" {
		t.Fatalf("cancel response was not hydrated: %#v", cancelled)
	}
	if len(cancelled.TimelineDrafts) != 1 || cancelled.TimelineDrafts[0].SuggestionID != "suggestion-1" {
		t.Fatalf("timeline drafts were not hydrated: %#v", cancelled.TimelineDrafts)
	}
	if len(client.cancelCalls) != 1 || client.cancelCalls[0] != "remote-job-1" {
		t.Fatalf("unexpected cancel calls: %#v", client.cancelCalls)
	}
}

func TestVideoIndexerSubmitForIndexingEnablesTimelineSelection(t *testing.T) {
	lib := &fakeLibraryStore{assets: []library.ProjectAsset{{ID: "asset-1", Name: "clip.mp4", CloudAssetID: "drive-item-1"}}}
	editStore := &memoryEditingProjectStore{}
	edit := NewEditingService(nil, nil, nil, editStore)
	jobStore := &memoryVideoIndexerJobStore{}
	draft := videoindexerstudio.TimelineDraft{
		SchemaVersion: videoindexerstudio.TimelineDraftSchemaVersion,
		OriginJobID:   "remote-job-1",
		SuggestionID:  "suggestion-1",
		PromptVersion: "v1",
		PrimaryVideoTrack: videoindexerstudio.TimelineTrack{
			ID:   "primary-video",
			Kind: videoindexerstudio.TimelineTrackKindVideo,
			Clips: []videoindexerstudio.TimelineClip{
				{
					ID:              "clip-1",
					SourceAssetID:   "asset-1",
					InMS:            0,
					OutMS:           1000,
					TimelineStartMS: 0,
					DurationMS:      1000,
					Transition:      videoindexerstudio.TimelineTransition{Kind: videoindexerstudio.TimelineTransitionKindCut},
				},
			},
		},
	}
	client := &fakeVideoIndexerClient{
		createResp: &videoindexerstudio.JobResponse{Job: videoindexerstudio.Job{
			ID:                  "remote-job-1",
			Status:              videoindexerstudio.JobStatusSucceeded,
			VideoIndexerVideoID: "video-123",
			VideoIndexResult:    &videoindexerstudio.VideoIndexResult{VideoID: "video-123", State: "Processed"},
			EditPlan:            &videoindexerstudio.EditPlan{SchemaVersion: 1, VideoID: "video-123", AssetID: "asset-1", Title: "Title", Summary: "Summary"},
			TimelineDrafts:      []videoindexerstudio.TimelineDraft{draft},
		}},
	}
	drive := &onedrive.Client{
		TokenProvider: staticTokenProvider{token: "delegated-token"},
		Scopes:        []string{onedrive.GraphScopeFilesReadWriteAppFolder},
	}
	svc := NewVideoIndexerStudioService(lib, fakeOneDriveSource{client: drive}, edit, client, jobStore)

	job, err := svc.SubmitForIndexing(context.Background(), "asset-1")
	if err != nil {
		t.Fatalf("SubmitForIndexing: %v", err)
	}
	if job.Status != videoIndexerJobStatusSucceeded || len(job.TimelineDrafts) != 1 {
		t.Fatalf("submitted job was not hydrated: %#v", job)
	}

	project, err := svc.CreateEditProject(context.Background(), job.ID, "suggestion-1")
	if err != nil {
		t.Fatalf("CreateEditProject: %v", err)
	}
	if project.SuggestionID != "suggestion-1" || len(editStore.projects) != 1 {
		t.Fatalf("timeline selection did not reach the editor: %#v %#v", project, editStore.projects)
	}
}

func TestVideoIndexerCreateEditProject(t *testing.T) {
	lib := &fakeLibraryStore{}
	editStore := &memoryEditingProjectStore{}
	edit := NewEditingService(nil, nil, nil, editStore)
	jobStore := &memoryVideoIndexerJobStore{
		jobs: []VideoIndexerStudioJob{
			{
				ID:        "video-indexer-123",
				AssetID:   "asset-1",
				AssetName: "clip.mp4",
				Status:    videoIndexerJobStatusSucceeded,
				TimelineDrafts: []videoindexerstudio.TimelineDraft{
					{
						SchemaVersion: videoindexerstudio.TimelineDraftSchemaVersion,
						OriginJobID:   "video-indexer-123",
						SuggestionID:  "suggestion-1",
						PromptVersion: "v1",
						PrimaryVideoTrack: videoindexerstudio.TimelineTrack{
							ID:   "primary-video",
							Kind: videoindexerstudio.TimelineTrackKindVideo,
							Clips: []videoindexerstudio.TimelineClip{
								{
									ID:              "clip-1",
									SourceAssetID:   "asset-1",
									InMS:            0,
									OutMS:           1000,
									TimelineStartMS: 0,
									DurationMS:      1000,
									Transition:      videoindexerstudio.TimelineTransition{Kind: videoindexerstudio.TimelineTransitionKindCut},
								},
							},
						},
					},
				},
			},
		},
	}
	svc := NewVideoIndexerStudioService(lib, nil, edit, &fakeVideoIndexerClient{}, jobStore)

	project, err := svc.CreateEditProject(context.Background(), "video-indexer-123", "suggestion-1")
	if err != nil {
		t.Fatalf("CreateEditProject: %v", err)
	}
	if project.ID != "video-indexer-video-indexer-123-suggestion-1" {
		t.Fatalf("project ID = %q, want deterministic id", project.ID)
	}
	if project.SuggestionID != "suggestion-1" || project.OriginJobID != "video-indexer-123" {
		t.Fatalf("unexpected project metadata: %#v", project)
	}
	if len(editStore.projects) != 1 {
		t.Fatalf("expected 1 saved project, got %d", len(editStore.projects))
	}
	if len(jobStore.jobs) != 1 || jobStore.jobs[0].ProjectID != project.ID {
		t.Fatalf("job store was not updated with project reference: %#v", jobStore.jobs)
	}
	if lib.saveJobsCalled != 0 {
		t.Fatalf("CU analysis job store was mutated: saveJobsCalled=%d", lib.saveJobsCalled)
	}
}

func TestVideoIndexerClientFromSettingsRejectsInvalidEndpoint(t *testing.T) {
	store := &mutableSettingsStore{settings: settings.AppSettings{
		VideoIndexerServiceEndpoint: "not-a-url",
		VideoIndexerServiceAPIKey:   "secret",
	}}
	if _, err := newVideoIndexerClientFromSettings(context.Background(), store); err == nil {
		t.Fatal("expected invalid endpoint error")
	}
}

func TestVideoIndexerServiceReloadsSettingsOnEachClientBuild(t *testing.T) {
	t.Setenv("AI_VIDEO_STUDIO_ALLOW_LOOPBACK_HTTP", "1")

	store := &mutableSettingsStore{settings: settings.AppSettings{
		VideoIndexerServiceEndpoint: "https://first.example",
		VideoIndexerServiceAPIKey:   "first-secret",
	}}

	firstServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-API-Key"); got != "first-secret" {
			t.Fatalf("first request API key = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"job":{"id":"job-1","status":"queued","oneDriveItemId":"item-1","sourceName":"clip.mp4"}}`))
	}))
	defer firstServer.Close()

	secondServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-API-Key"); got != "second-secret" {
			t.Fatalf("second request API key = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"job":{"id":"job-2","status":"queued","oneDriveItemId":"item-2","sourceName":"clip.mp4"}}`))
	}))
	defer secondServer.Close()

	store.settings.VideoIndexerServiceEndpoint = firstServer.URL
	svc := NewVideoIndexerStudioServiceFromSettings(&fakeLibraryStore{}, nil, &fakeEditingSaver{}, store)

	client1, err := svc.clientFor(context.Background())
	if err != nil {
		t.Fatalf("clientFor first: %v", err)
	}
	if _, err := client1.CreateJob(context.Background(), videoindexerstudio.CreateJobRequest{
		OneDriveItemID:      "item-1",
		OneDriveAccessToken: "token-1",
		SourceName:          "clip.mp4",
		CorrelationID:       "asset-1",
	}); err != nil {
		t.Fatalf("first CreateJob: %v", err)
	}

	store.settings.VideoIndexerServiceEndpoint = secondServer.URL
	store.settings.VideoIndexerServiceAPIKey = "second-secret"

	client2, err := svc.clientFor(context.Background())
	if err != nil {
		t.Fatalf("clientFor second: %v", err)
	}
	if _, err := client2.CreateJob(context.Background(), videoindexerstudio.CreateJobRequest{
		OneDriveItemID:      "item-2",
		OneDriveAccessToken: "token-2",
		SourceName:          "clip.mp4",
		CorrelationID:       "asset-2",
	}); err != nil {
		t.Fatalf("second CreateJob: %v", err)
	}
}

func TestEditingServicePersistsProjects(t *testing.T) {
	dir := newBackendTestDir(t)
	store := &fileEditingProjectStore{path: filepath.Join(dir, "edit-projects.json")}
	svc := NewEditingService(nil, nil, nil, store)

	project, err := svc.SaveProject(context.Background(), editing.EditProject{
		ID:   "project-1",
		Name: "Draft",
	})
	if err != nil {
		t.Fatalf("SaveProject: %v", err)
	}
	if project.ID != "project-1" {
		t.Fatalf("project ID = %q", project.ID)
	}
	loadedSvc := NewEditingService(nil, nil, nil, store)
	projects, err := loadedSvc.ListProjects(context.Background())
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(projects) != 1 || projects[0].ID != "project-1" {
		t.Fatalf("unexpected loaded projects: %#v", projects)
	}
}

type staticTokenProvider struct {
	token string
}

func (s staticTokenProvider) AccessToken(context.Context, []string) (string, error) {
	return s.token, nil
}

type memoryEditingProjectStore struct {
	projects []editing.EditProject
}

func (m *memoryEditingProjectStore) Load(context.Context) ([]editing.EditProject, error) {
	out := make([]editing.EditProject, len(m.projects))
	copy(out, m.projects)
	return out, nil
}

func (m *memoryEditingProjectStore) Save(_ context.Context, projects []editing.EditProject) error {
	m.projects = append([]editing.EditProject(nil), projects...)
	return nil
}

type memoryVideoIndexerJobStore struct {
	jobs []VideoIndexerStudioJob
}

func (m *memoryVideoIndexerJobStore) Load(context.Context) ([]VideoIndexerStudioJob, error) {
	out := make([]VideoIndexerStudioJob, len(m.jobs))
	copy(out, m.jobs)
	return out, nil
}

func (m *memoryVideoIndexerJobStore) Save(_ context.Context, jobs []VideoIndexerStudioJob) error {
	m.jobs = append([]VideoIndexerStudioJob(nil), jobs...)
	return nil
}

type mutableSettingsStore struct {
	mu       sync.Mutex
	settings settings.AppSettings
}

func (m *mutableSettingsStore) Load(context.Context) (settings.AppSettings, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.settings, nil
}

func (m *mutableSettingsStore) Save(_ context.Context, next settings.AppSettings) (settings.AppSettings, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.settings = next
	return next, nil
}

func (m *mutableSettingsStore) Path() string { return "" }
