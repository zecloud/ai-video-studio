package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type memoryRenderJobStore struct {
	mu       sync.Mutex
	jobs     map[string]StoredRenderJob
	payloads []string
	version  int
}

func newMemoryRenderJobStore() *memoryRenderJobStore {
	return &memoryRenderJobStore{jobs: make(map[string]StoredRenderJob)}
}

func (s *memoryRenderJobStore) Create(_ context.Context, job RenderJobDocument) error {
	if err := job.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.jobs[job.ID]; exists {
		return newServiceError(http.StatusConflict, "render_job_exists", "render job already exists", false)
	}
	s.version++
	s.record(job, fmt.Sprintf("etag-%d", s.version))
	return nil
}

func (s *memoryRenderJobStore) Get(_ context.Context, id string) (StoredRenderJob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[id]
	if !ok {
		return StoredRenderJob{}, newServiceError(http.StatusNotFound, "render_job_not_found", "render job not found", false)
	}
	return job, nil
}

func (s *memoryRenderJobStore) List(context.Context) ([]StoredRenderJob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	jobs := make([]StoredRenderJob, 0, len(s.jobs))
	for _, job := range s.jobs {
		jobs = append(jobs, job)
	}
	return jobs, nil
}

func (s *memoryRenderJobStore) Update(_ context.Context, job RenderJobDocument, etag string) error {
	if err := job.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.jobs[job.ID]
	if !ok {
		return newServiceError(http.StatusNotFound, "render_job_not_found", "render job not found", false)
	}
	if current.ETag != etag {
		return newServiceError(http.StatusConflict, "etag_conflict", "render job update conflict", true)
	}
	s.version++
	s.record(job, fmt.Sprintf("etag-%d", s.version))
	return nil
}

func (s *memoryRenderJobStore) record(job RenderJobDocument, etag string) {
	data, _ := json.Marshal(job)
	s.payloads = append(s.payloads, string(data))
	s.jobs[job.ID] = StoredRenderJob{RenderJobDocument: job, ETag: etag}
}

type renderStageCall struct {
	name    string
	body    string
	options StageOptions
}

type fakeRenderStager struct {
	container string
	stages    []renderStageCall
	deletes   []StagedAsset
}

func (s *fakeRenderStager) StageNamed(_ context.Context, name string, body io.Reader, options StageOptions) (StagedAsset, error) {
	data, err := io.ReadAll(body)
	if err != nil {
		return StagedAsset{}, err
	}
	s.stages = append(s.stages, renderStageCall{name: name, body: string(data), options: options})
	return StagedAsset{Container: s.StagingContainer(), BlobName: name, Size: int64(len(data)), MediaType: options.ContentType}, nil
}

func (s *fakeRenderStager) Delete(_ context.Context, asset StagedAsset) error {
	s.deletes = append(s.deletes, asset)
	return nil
}

func (s *fakeRenderStager) StagingContainer() string {
	if s.container != "" {
		return s.container
	}
	return "render-staging"
}

type fakeRenderExecutionBlobs struct {
	*fakeRenderStager
}

func (*fakeRenderExecutionBlobs) DownloadToFile(context.Context, StagedAsset, string) error {
	return errors.New("unexpected download")
}

func (*fakeRenderExecutionBlobs) UploadFile(context.Context, StagedAsset, string, string) (int64, error) {
	return 0, errors.New("unexpected upload")
}

type failingRenderExecutionBlobs struct {
	*fakeRenderExecutionBlobs
	err error
}

func (b *failingRenderExecutionBlobs) Delete(_ context.Context, asset StagedAsset) error {
	b.fakeRenderStager.deletes = append(b.fakeRenderStager.deletes, asset)
	return b.err
}

type recordingRenderScheduler struct {
	inputs        []FFmpegRenderOrchestrationInput
	cancellations []string
}

func (s *recordingRenderScheduler) ScheduleRender(_ context.Context, input FFmpegRenderOrchestrationInput) error {
	s.inputs = append(s.inputs, input)
	return nil
}

func (s *recordingRenderScheduler) RequestRenderCancellation(_ context.Context, id string) error {
	s.cancellations = append(s.cancellations, id)
	return nil
}

func validRenderRequest() CreateRenderJobRequest {
	return CreateRenderJobRequest{
		ProjectID:           "project-1",
		OneDriveAccessToken: "delegated-token-secret",
		Clips: []RenderClipRequest{
			{ID: "clip-a", OneDriveItemID: "drive-item-a", SourceName: "camera-a.mp4", InMS: 1000, OutMS: 4000},
			{ID: "clip-b", OneDriveItemID: "drive-item-b", SourceName: "camera-b.mp4", InMS: 0, OutMS: 2000, Muted: true},
		},
		Transitions:   []RenderTransitionRequest{{Kind: "cut"}},
		Preset:        "mpeg4-1080p",
		OutputName:    "finished.mp4",
		CorrelationID: "render-correlation",
	}
}

func TestDurableRenderJobServiceStagesNamespacedInputsWithoutPersistingToken(t *testing.T) {
	graph := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer delegated-token-secret" {
			t.Errorf("Authorization = %q", got)
		}
		w.Header().Set("Content-Type", "video/mp4")
		_, _ = w.Write([]byte("video-bytes"))
	}))
	defer graph.Close()

	store := newMemoryRenderJobStore()
	stager := &fakeRenderStager{}
	scheduler := &recordingRenderScheduler{}
	service := NewDurableRenderJobService(store, NewOneDriveClient(graph.URL, graph.Client()), stager, scheduler, fixedClock{now: time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)})
	job, err := service.CreateRenderJob(context.Background(), validRenderRequest())
	if err != nil {
		t.Fatalf("CreateRenderJob() error = %v", err)
	}
	if job.Status != RenderJobStatusQueued {
		t.Fatalf("status = %q, want %q", job.Status, RenderJobStatusQueued)
	}
	if len(stager.stages) != 2 || len(scheduler.inputs) != 1 {
		t.Fatalf("stage calls = %d, schedules = %d", len(stager.stages), len(scheduler.inputs))
	}
	wantPrefix := "render-inputs/" + job.ID + "/"
	for _, call := range stager.stages {
		if !strings.HasPrefix(call.name, wantPrefix) {
			t.Fatalf("staged blob %q is outside %q", call.name, wantPrefix)
		}
		if call.body != "video-bytes" || call.options.ContentType != "video/mp4" {
			t.Fatalf("unexpected staged call: %#v", call)
		}
	}
	inputJSON, err := json.Marshal(scheduler.inputs[0])
	if err != nil {
		t.Fatal(err)
	}
	persisted := strings.Join(append(append([]string{}, store.payloads...), string(inputJSON)), "\n")
	for _, forbidden := range []string{"delegated-token-secret", "drive-item-a", "drive-item-b", "oneDriveAccessToken"} {
		if strings.Contains(persisted, forbidden) {
			t.Fatalf("sensitive delegated input %q was persisted: %s", forbidden, persisted)
		}
	}
	if got := scheduler.inputs[0].Output.BlobName; got != "render-outputs/"+job.ID+"/finished.mp4" {
		t.Fatalf("output blob = %q", got)
	}
}

func TestCreateRenderJobRequestValidation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*CreateRenderJobRequest)
	}{
		{"unsupported preset", func(r *CreateRenderJobRequest) { r.Preset = "copy" }},
		{"non-cut transition", func(r *CreateRenderJobRequest) { r.Transitions[0].Kind = "fade" }},
		{"cut duration", func(r *CreateRenderJobRequest) { r.Transitions[0].DurationMS = 250 }},
		{"duplicate clip id", func(r *CreateRenderJobRequest) { r.Clips[1].ID = r.Clips[0].ID }},
		{"invalid trim", func(r *CreateRenderJobRequest) { r.Clips[0].OutMS = r.Clips[0].InMS }},
		{"non-mp4 output", func(r *CreateRenderJobRequest) { r.OutputName = "output.mov" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := validRenderRequest()
			test.mutate(&req)
			if err := req.Validate(); err == nil {
				t.Fatal("Validate() error = nil")
			}
		})
	}
}

func TestFFmpegSegmentArgsProduceUniformVideoOnlySegments(t *testing.T) {
	clip := StagedRenderClip{InMS: 1250, OutMS: 2750, Muted: false}
	args := ffmpegSegmentArgs("input.media", "output.mp4", clip, "mpeg4-1080p")
	joined := strings.Join(args, " ")
	for _, want := range []string{"-ss 1.250", "-t 1.500", "-an", "mpeg4", "fps=30,scale=1920:1080", "pad=1920:1080"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("args %q do not contain %q", joined, want)
		}
	}
	if strings.Contains(joined, " -to ") {
		t.Fatalf("args use ambiguous absolute -to: %q", joined)
	}
}

func TestFFmpegLogBufferBoundsCapturedProcessOutput(t *testing.T) {
	var output ffmpegLogBuffer
	payload := []byte(strings.Repeat("x", 12000))
	written, err := output.Write(payload)
	if err != nil {
		t.Fatal(err)
	}
	if written != len(payload) {
		t.Fatalf("Write() = %d, want %d", written, len(payload))
	}
	if output.Len() != 8192 {
		t.Fatalf("captured log length = %d, want 8192", output.Len())
	}
}

func TestBoundedFFmpegLogRedactsURLsAndBoundsOutput(t *testing.T) {
	log := []byte("https://storage.example.test/input.mp4?sig=secret-token " + strings.Repeat("x", 5000))
	got := boundedFFmpegLog(log)
	if strings.Contains(got, "secret-token") || strings.Contains(got, "?sig=") {
		t.Fatalf("log was not redacted: %q", got)
	}
	if len(got) > 4099 {
		t.Fatalf("bounded log length = %d", len(got))
	}
}

func TestFFmpegWorkerConfigDoesNotRequireVideoIndexerSettings(t *testing.T) {
	cfg := Config{
		ServiceRole:         "ffmpeg-worker",
		ListenAddr:          ":8080",
		StorageURL:          "https://storage.example.test",
		StagingContainer:    "staging",
		JobContainer:        "jobs",
		GraphBaseURL:        defaultGraphBaseURL,
		DTSEndpoint:         "https://dts.example.test",
		DTSRenderTaskHub:    "render-hub",
		FFmpegPath:          "ffmpeg",
		RenderWorkspaceRoot: "render-work",
		RenderTimeout:       time.Hour,
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	cfg.FFmpegPath = ""
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "FFMPEG_PATH") {
		t.Fatalf("Validate() error = %v, want FFMPEG_PATH", err)
	}
}

type staticRenderJobService struct {
	created CreateRenderJobRequest
}

func (s *staticRenderJobService) CreateRenderJob(_ context.Context, req CreateRenderJobRequest) (RenderJob, error) {
	s.created = req
	return RenderJob{ID: "job-render", Status: RenderJobStatusQueued}, nil
}
func (*staticRenderJobService) GetRenderJob(context.Context, string) (RenderJob, error) {
	return RenderJob{}, nil
}
func (*staticRenderJobService) ListRenderJobs(context.Context, RenderJobStatus) ([]RenderJob, error) {
	return nil, nil
}
func (*staticRenderJobService) CancelRenderJob(context.Context, string) (RenderJob, error) {
	return RenderJob{}, nil
}
func (*staticRenderJobService) ReconcileQueuedRenders(context.Context) error { return nil }

func TestRenderJobAPIAcceptsAsynchronousCreate(t *testing.T) {
	service := &staticRenderJobService{}
	server := NewServer(Config{APIKey: "api-key"}, nil)
	server.SetRenderJobs(service)
	body, _ := json.Marshal(validRenderRequest())
	req := httptest.NewRequest(http.MethodPost, "/api/v1/render-jobs", bytes.NewReader(body))
	req.Header.Set("X-API-Key", "api-key")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, req)
	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Location"); got != "/api/v1/render-jobs/job-render" {
		t.Fatalf("Location = %q", got)
	}
	if strings.Contains(response.Body.String(), service.created.OneDriveAccessToken) {
		t.Fatalf("response leaked delegated token: %s", response.Body.String())
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

type closeErrorBody struct{ io.Reader }

func (closeErrorBody) Close() error { return errors.New("close failed") }

type blockingSuccessfulRenderStager struct {
	fakeRenderStager
	staged  chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *blockingSuccessfulRenderStager) StageNamed(ctx context.Context, name string, body io.Reader, options StageOptions) (StagedAsset, error) {
	asset, err := s.fakeRenderStager.StageNamed(ctx, name, body, options)
	s.once.Do(func() { close(s.staged) })
	<-s.release
	return asset, err
}

type cancelingRenderStager struct {
	fakeRenderStager
	cancel context.CancelFunc
}

func (s *cancelingRenderStager) StageNamed(ctx context.Context, name string, body io.Reader, options StageOptions) (StagedAsset, error) {
	s.cancel()
	return StagedAsset{}, ctx.Err()
}

type failingDeleteRenderStager struct {
	fakeRenderStager
	err error
}

func (s *failingDeleteRenderStager) Delete(_ context.Context, asset StagedAsset) error {
	s.deletes = append(s.deletes, asset)
	return s.err
}

type cancellationObservingScheduler struct {
	store *memoryRenderJobStore
	saw   bool
}

func (*cancellationObservingScheduler) ScheduleRender(context.Context, FFmpegRenderOrchestrationInput) error {
	return nil
}

func (s *cancellationObservingScheduler) RequestRenderCancellation(ctx context.Context, id string) error {
	job, err := s.store.Get(ctx, id)
	if err != nil {
		return err
	}
	s.saw = job.CancellationRequestedAt != nil
	return nil
}

func storedRenderDocument(id string, status RenderJobStatus) RenderJobDocument {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	return RenderJobDocument{
		SchemaVersion: renderSchemaVersion, ID: id, ProjectID: "project-1", Status: status,
		Preset: "mpeg4-1080p", OutputName: "output.mp4", RequestFingerprint: "fingerprint",
		Clips:           []StagedRenderClip{{ID: "clip-1", Container: "render-staging", BlobName: "render-inputs/" + id + "/clip.mp4", SourceName: "clip.mp4", InMS: 0, OutMS: 1000}},
		OrchestrationID: id, OrchestrationName: ffmpegRenderOrchestrationName, OrchestrationVersion: ffmpegRenderOrchestrationVersion,
		Output:    &RenderOutput{Container: "render-staging", BlobName: renderOutputBlobName(id, "output.mp4"), Size: 5, MediaType: "video/mp4"},
		CreatedAt: now, UpdatedAt: now,
	}
}

func TestCancellationDuringSuccessfulStageCleansUnprojectedAsset(t *testing.T) {
	graph := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		_, _ = w.Write([]byte("video"))
	}))
	defer graph.Close()
	store := newMemoryRenderJobStore()
	stager := &blockingSuccessfulRenderStager{staged: make(chan struct{}), release: make(chan struct{})}
	service := NewDurableRenderJobService(store, NewOneDriveClient(graph.URL, graph.Client()), stager, &recordingRenderScheduler{}, fixedClock{now: time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)})
	result := make(chan error, 1)
	go func() {
		_, err := service.CreateRenderJob(context.Background(), validRenderRequest())
		result <- err
	}()
	select {
	case <-stager.staged:
	case <-time.After(time.Second):
		t.Fatal("staging did not start")
	}
	jobID := newJobID(validRenderRequest().CorrelationID)
	if _, err := service.CancelRenderJob(context.Background(), jobID); err != nil {
		t.Fatalf("CancelRenderJob() error = %v", err)
	}
	close(stager.release)
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("CreateRenderJob() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("staging request did not stop")
	}
	if len(stager.deletes) != 1 || stager.deletes[0].BlobName == "" {
		t.Fatalf("unprojected staged asset was not cleaned: %#v", stager.deletes)
	}
	stored, err := store.Get(context.Background(), jobID)
	if err != nil || stored.Status != RenderJobStatusCanceled || stored.InputsCleanupPending {
		t.Fatalf("canceled job = %#v, %v", stored.RenderJobDocument, err)
	}
}

func TestRenderCancellationIsPersistedBeforeSignalAndDoesNotDeleteActiveInputs(t *testing.T) {
	store := newMemoryRenderJobStore()
	doc := storedRenderDocument("render-active", RenderJobStatusRendering)
	if err := store.Create(context.Background(), doc); err != nil {
		t.Fatal(err)
	}
	stager := &fakeRenderStager{}
	scheduler := &cancellationObservingScheduler{store: store}
	service := NewDurableRenderJobService(store, nil, stager, scheduler, fixedClock{now: doc.CreatedAt.Add(time.Minute)})

	job, err := service.CancelRenderJob(context.Background(), doc.ID)
	if err != nil {
		t.Fatalf("CancelRenderJob() error = %v", err)
	}
	if !scheduler.saw || job.CancellationRequestedAt == nil {
		t.Fatalf("cancellation was not durable before signaling: %#v", job)
	}
	if job.Status != RenderJobStatusRendering {
		t.Fatalf("active status = %q, want rendering until execution stops", job.Status)
	}
	if len(stager.deletes) != 0 {
		t.Fatalf("active inputs were deleted: %#v", stager.deletes)
	}
}

func TestActiveCancellationMonitorCancelsWorkAndStops(t *testing.T) {
	store := newMemoryRenderJobStore()
	doc := storedRenderDocument("render-monitor", RenderJobStatusRendering)
	if err := store.Create(context.Background(), doc); err != nil {
		t.Fatal(err)
	}
	activities := NewFFmpegRenderActivities(store, nil, Config{FFmpegPath: "ffmpeg", RenderWorkspaceRoot: ".", RenderTimeout: time.Minute}, fixedClock{now: doc.CreatedAt})
	activities.cancellationPollInterval = 5 * time.Millisecond
	workCtx, cancelWork := context.WithCancel(context.Background())
	defer cancelWork()
	requested := &atomic.Bool{}
	errCh := make(chan error, 1)
	stop := activities.watchCancellation(context.Background(), doc.ID, requested, cancelWork, errCh)

	service := NewDurableRenderJobService(store, nil, &fakeRenderStager{}, &recordingRenderScheduler{}, fixedClock{now: doc.CreatedAt.Add(time.Minute)})
	if _, err := service.requestCancellation(context.Background(), doc.ID); err != nil {
		stop()
		t.Fatal(err)
	}
	select {
	case <-workCtx.Done():
	case <-time.After(time.Second):
		stop()
		t.Fatal("cancellation monitor did not cancel active work")
	}
	stop()
	if !requested.Load() {
		t.Fatal("cancellation monitor did not observe durable request")
	}
}

func TestCancellationOutputDeleteFailureKeepsJobRetryableAndNonterminal(t *testing.T) {
	store := newMemoryRenderJobStore()
	doc := storedRenderDocument("render-cancel-delete-retry", RenderJobStatusUploading)
	doc.ExecutionActive = false
	requested := doc.CreatedAt.Add(time.Minute)
	doc.CancellationRequestedAt = &requested
	if err := store.Create(context.Background(), doc); err != nil {
		t.Fatal(err)
	}
	stager := &fakeRenderStager{}
	blobs := &failingRenderExecutionBlobs{fakeRenderExecutionBlobs: &fakeRenderExecutionBlobs{fakeRenderStager: stager}, err: newServiceError(http.StatusServiceUnavailable, "blob_delete_failed", "temporary delete failure", true)}
	activities := NewFFmpegRenderActivities(store, blobs, Config{FFmpegPath: "ffmpeg", RenderWorkspaceRoot: ".", RenderTimeout: time.Minute}, fixedClock{now: requested})

	_, err := activities.finishByID(context.Background(), doc.ID, RenderJobStatusSucceeded, "", "")
	var serviceErr *ServiceError
	if !errors.As(err, &serviceErr) || !serviceErr.Retryable {
		t.Fatalf("finishByID() error = %v, want retryable delete failure", err)
	}
	stored, err := store.Get(context.Background(), doc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status.Terminal() || stored.ExecutionActive {
		t.Fatalf("delete failure prematurely finalized cancellation: %#v", stored.RenderJobDocument)
	}

	blobs.err = nil
	stored, err = activities.finishByID(context.Background(), doc.ID, RenderJobStatusSucceeded, "", "")
	if err != nil || stored.Status != RenderJobStatusCanceled {
		t.Fatalf("cancellation retry = %#v, %v", stored.RenderJobDocument, err)
	}
}

func TestCancelActivityDeletesOutputWhenCancellationWinsRace(t *testing.T) {
	store := newMemoryRenderJobStore()
	doc := storedRenderDocument("render-cancel-output", RenderJobStatusUploading)
	doc.ExecutionActive = false
	requested := doc.CreatedAt.Add(time.Minute)
	doc.CancellationRequestedAt = &requested
	if err := store.Create(context.Background(), doc); err != nil {
		t.Fatal(err)
	}
	stager := &fakeRenderStager{}
	blobs := &fakeRenderExecutionBlobs{fakeRenderStager: stager}
	activities := NewFFmpegRenderActivities(store, blobs, Config{FFmpegPath: "ffmpeg", RenderWorkspaceRoot: ".", RenderTimeout: time.Minute}, fixedClock{now: requested})

	if err := activities.cancelByID(context.Background(), doc.ID); err != nil {
		t.Fatalf("cancelByID() error = %v", err)
	}
	if len(stager.deletes) == 0 || stager.deletes[0].BlobName != doc.Output.BlobName {
		t.Fatalf("canceled output was not deleted first: %#v", stager.deletes)
	}
	stored, err := store.Get(context.Background(), doc.ID)
	if err != nil || stored.Status != RenderJobStatusCanceled || stored.InputsCleanupPending {
		t.Fatalf("canceled job = %#v, %v", stored.RenderJobDocument, err)
	}
}

func TestForceCancelUsesStoredOutputIdentityOnlyAfterExecutionStops(t *testing.T) {
	store := newMemoryRenderJobStore()
	doc := storedRenderDocument("render-force", RenderJobStatusQueued)
	if err := store.Create(context.Background(), doc); err != nil {
		t.Fatal(err)
	}
	stager := &fakeRenderStager{}
	blobs := &fakeRenderExecutionBlobs{fakeRenderStager: stager}
	activities := NewFFmpegRenderActivities(store, blobs, Config{FFmpegPath: "ffmpeg", RenderWorkspaceRoot: ".", RenderTimeout: time.Minute}, fixedClock{now: doc.CreatedAt.Add(time.Minute)})
	terminated := false
	activities.SetCancellationTerminator(func(_ context.Context, id string) error {
		terminated = id == doc.ID
		return nil
	})

	if err := activities.forceCancelByID(context.Background(), doc.ID); err != nil {
		t.Fatalf("forceCancelByID() error = %v", err)
	}
	if !terminated {
		t.Fatal("render orchestration was not terminated")
	}
	if len(stager.deletes) == 0 || stager.deletes[0].Container != doc.Output.Container || stager.deletes[0].BlobName != doc.Output.BlobName {
		t.Fatalf("stored output identity was not deleted first: %#v", stager.deletes)
	}
	stored, err := store.Get(context.Background(), doc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != RenderJobStatusCanceled || stored.ExecutionActive {
		t.Fatalf("forced cancellation was not projected: %#v", stored.RenderJobDocument)
	}
}

func TestForceCancelNeverTerminatesOrDeletesWhileExecutionIsActive(t *testing.T) {
	store := newMemoryRenderJobStore()
	doc := storedRenderDocument("render-force-active", RenderJobStatusRendering)
	doc.ExecutionActive = true
	requested := doc.CreatedAt.Add(time.Minute)
	doc.CancellationRequestedAt = &requested
	if err := store.Create(context.Background(), doc); err != nil {
		t.Fatal(err)
	}
	stager := &fakeRenderStager{}
	blobs := &fakeRenderExecutionBlobs{fakeRenderStager: stager}
	activities := NewFFmpegRenderActivities(store, blobs, Config{FFmpegPath: "ffmpeg", RenderWorkspaceRoot: ".", RenderTimeout: time.Minute}, fixedClock{now: requested})
	terminated := false
	activities.SetCancellationTerminator(func(context.Context, string) error {
		terminated = true
		return nil
	})

	err := activities.forceCancelByID(context.Background(), doc.ID)
	var serviceErr *ServiceError
	if !errors.As(err, &serviceErr) || !serviceErr.Retryable {
		t.Fatalf("forceCancelByID() error = %v, want retryable active-execution error", err)
	}
	if terminated || len(stager.deletes) != 0 {
		t.Fatalf("active execution was terminated or its blobs deleted: terminated=%v deletes=%#v", terminated, stager.deletes)
	}
}

func TestCancellationPreventsLaterSucceededTransition(t *testing.T) {
	store := newMemoryRenderJobStore()
	doc := storedRenderDocument("render-sticky-cancel", RenderJobStatusUploading)
	if err := store.Create(context.Background(), doc); err != nil {
		t.Fatal(err)
	}
	service := NewDurableRenderJobService(store, nil, &fakeRenderStager{}, &recordingRenderScheduler{}, fixedClock{now: doc.CreatedAt.Add(time.Minute)})
	if _, err := service.requestCancellation(context.Background(), doc.ID); err != nil {
		t.Fatal(err)
	}
	got, err := service.transition(context.Background(), doc.ID, RenderJobStatusSucceeded, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status == RenderJobStatusSucceeded {
		t.Fatal("render succeeded after cancellation was requested")
	}
}

func TestStagingFailureProjectsWithBackgroundContext(t *testing.T) {
	graph := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		_, _ = w.Write([]byte("video"))
	}))
	defer graph.Close()
	ctx, cancel := context.WithCancel(context.Background())
	stager := &cancelingRenderStager{cancel: cancel}
	store := newMemoryRenderJobStore()
	service := NewDurableRenderJobService(store, NewOneDriveClient(graph.URL, graph.Client()), stager, &recordingRenderScheduler{}, fixedClock{now: time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)})

	_, err := service.CreateRenderJob(ctx, validRenderRequest())
	if err == nil {
		t.Fatal("CreateRenderJob() error = nil")
	}
	stored, getErr := store.Get(context.Background(), newJobID(validRenderRequest().CorrelationID))
	if getErr != nil {
		t.Fatal(getErr)
	}
	if stored.Status != RenderJobStatusFailed {
		t.Fatalf("status = %q, want failed", stored.Status)
	}
}

func TestReaderCloseFailureCleansJustStagedAsset(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"video/mp4"}}, ContentLength: 5, Body: closeErrorBody{Reader: strings.NewReader("video")}, Request: req}, nil
	})}
	store := newMemoryRenderJobStore()
	stager := &fakeRenderStager{}
	service := NewDurableRenderJobService(store, NewOneDriveClient("https://graph.example", client), stager, &recordingRenderScheduler{}, fixedClock{now: time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)})
	req := validRenderRequest()
	req.Clips = req.Clips[:1]
	req.Transitions = nil

	_, err := service.CreateRenderJob(context.Background(), req)
	if err == nil || !strings.Contains(err.Error(), "close failed") {
		t.Fatalf("CreateRenderJob() error = %v", err)
	}
	if len(stager.deletes) != 1 || stager.deletes[0].BlobName == "" {
		t.Fatalf("just-staged asset was not deleted: %#v", stager.deletes)
	}
}

func TestCleanupFailureCannotChangeSuccessfulOutcome(t *testing.T) {
	store := newMemoryRenderJobStore()
	doc := storedRenderDocument("render-cleanup", RenderJobStatusSucceeded)
	doc.InputsCleanupPending = true
	if err := store.Create(context.Background(), doc); err != nil {
		t.Fatal(err)
	}
	stager := &failingDeleteRenderStager{err: newServiceError(http.StatusServiceUnavailable, "blob_delete_failed", "temporary delete failure", true)}
	service := NewDurableRenderJobService(store, nil, stager, &recordingRenderScheduler{}, fixedClock{now: doc.CreatedAt.Add(time.Minute)})

	got, err := service.cleanupAndRecord(context.Background(), doc.ID)
	if err == nil {
		t.Fatal("cleanupAndRecord() error = nil")
	}
	if got.Status != RenderJobStatusSucceeded || !got.InputsCleanupPending || got.CleanupError == nil || !got.CleanupError.Retryable {
		t.Fatalf("cleanup corrupted successful outcome: %#v", got.RenderJobDocument)
	}
	if transitioned, transitionErr := service.transition(context.Background(), doc.ID, RenderJobStatusFailed, nil); transitionErr != nil || transitioned.Status != RenderJobStatusSucceeded {
		t.Fatalf("failed transition changed successful outcome: %#v, %v", transitioned, transitionErr)
	}
	blobs := &failingRenderExecutionBlobs{fakeRenderExecutionBlobs: &fakeRenderExecutionBlobs{fakeRenderStager: &stager.fakeRenderStager}, err: errors.New("output must be preserved")}
	activities := NewFFmpegRenderActivities(store, blobs, Config{FFmpegPath: "ffmpeg", RenderWorkspaceRoot: ".", RenderTimeout: time.Minute}, fixedClock{now: doc.CreatedAt.Add(2 * time.Minute)})
	if _, finishErr := activities.finishByID(context.Background(), doc.ID, RenderJobStatusFailed, "render_failed", "FFmpeg render failed"); finishErr != nil {
		t.Fatalf("failed compensation changed successful output: %v", finishErr)
	}
	if len(stager.deletes) != 1 {
		t.Fatalf("successful output was deleted during compensation: %#v", stager.deletes)
	}
}

func TestCorrelationRetryReturnsAndReschedulesCompatibleJob(t *testing.T) {
	graph := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		_, _ = w.Write([]byte("video-bytes"))
	}))
	defer graph.Close()
	store := newMemoryRenderJobStore()
	stager := &fakeRenderStager{}
	scheduler := &recordingRenderScheduler{}
	service := NewDurableRenderJobService(store, NewOneDriveClient(graph.URL, graph.Client()), stager, scheduler, fixedClock{now: time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)})

	first, err := service.CreateRenderJob(context.Background(), validRenderRequest())
	if err != nil {
		t.Fatal(err)
	}

	retry := validRenderRequest()
	retry.OneDriveAccessToken = "fresh-token"
	second, err := service.CreateRenderJob(context.Background(), retry)
	if err != nil {
		t.Fatalf("compatible retry error = %v", err)
	}

	if second.ID != first.ID || len(stager.stages) != len(firstProjectClips(validRenderRequest())) || len(scheduler.inputs) != 2 {
		t.Fatalf("retry was not idempotent/rescheduled: first=%#v second=%#v stages=%d schedules=%d", first, second, len(stager.stages), len(scheduler.inputs))
	}

	conflict := validRenderRequest()
	conflict.OutputName = "other.mp4"
	if _, err := service.CreateRenderJob(context.Background(), conflict); serviceErrorCode(err) != "render_correlation_conflict" {
		t.Fatalf("different shape error = %v", err)
	}
}

func firstProjectClips(req CreateRenderJobRequest) []RenderClipRequest { return req.Clips }

func TestReconcileQueuedRendersFailsStaleStagingAndCleansInputs(t *testing.T) {
	store := newMemoryRenderJobStore()
	doc := storedRenderDocument("render-stale-staging", RenderJobStatusStaging)
	doc.UpdatedAt = doc.CreatedAt
	if err := store.Create(context.Background(), doc); err != nil {
		t.Fatal(err)
	}
	stager := &fakeRenderStager{}
	service := NewDurableRenderJobService(store, nil, stager, &recordingRenderScheduler{}, fixedClock{now: doc.CreatedAt.Add(renderStagingRecoveryDelay)})
	if err := service.ReconcileQueuedRenders(context.Background()); err != nil {
		t.Fatalf("ReconcileQueuedRenders() error = %v", err)
	}
	stored, err := store.Get(context.Background(), doc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != RenderJobStatusFailed || stored.Error == nil || stored.Error.Code != "render_staging_interrupted" || stored.InputsCleanupPending {
		t.Fatalf("stale staging job was not failed and cleaned: %#v", stored.RenderJobDocument)
	}
	if len(stager.deletes) != len(doc.Clips) {
		t.Fatalf("stale staging inputs were not cleaned: %#v", stager.deletes)
	}
}

type staticRenderOutputService struct{ job RenderJob }

func (*staticRenderOutputService) CreateRenderJob(context.Context, CreateRenderJobRequest) (RenderJob, error) {
	return RenderJob{}, nil
}
func (s *staticRenderOutputService) GetRenderJob(context.Context, string) (RenderJob, error) {
	return s.job, nil
}
func (*staticRenderOutputService) ListRenderJobs(context.Context, RenderJobStatus) ([]RenderJob, error) {
	return nil, nil
}
func (*staticRenderOutputService) CancelRenderJob(context.Context, string) (RenderJob, error) {
	return RenderJob{}, nil
}
func (*staticRenderOutputService) ReconcileQueuedRenders(context.Context) error { return nil }

type staticRenderOutputStreamer struct {
	asset StagedAsset
	data  string
}

func (s *staticRenderOutputStreamer) OpenDownload(_ context.Context, asset StagedAsset) (BlobDownload, error) {
	s.asset = asset
	return BlobDownload{Body: io.NopCloser(strings.NewReader(s.data)), ContentLength: int64(len(s.data)), ContentType: "video/mp4"}, nil
}

func TestRenderOutputEndpointStreamsSucceededBlob(t *testing.T) {
	job := storedRenderDocument("render-output", RenderJobStatusSucceeded).ToRenderJob()
	streamer := &staticRenderOutputStreamer{data: "video"}
	server := NewServer(Config{APIKey: "api-key", StagingContainer: "render-staging"}, nil)
	server.SetRenderJobs(&staticRenderOutputService{job: job})
	server.SetRenderOutputStreamer(streamer)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/render-jobs/"+job.ID+"/output", nil)
	req.Header.Set("X-API-Key", "api-key")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, req)
	if response.Code != http.StatusOK || response.Body.String() != "video" {
		t.Fatalf("status = %d, body = %q", response.Code, response.Body.String())
	}
	if streamer.asset.Container != "render-staging" || streamer.asset.BlobName != job.Output.BlobName {
		t.Fatalf("unexpected streamed asset: %#v", streamer.asset)
	}
	if strings.Contains(response.Header().Get("Content-Disposition"), "sig=") {
		t.Fatal("output response exposed a SAS token")
	}
}

func TestRenderOutputEndpointRejectsNonSucceededJob(t *testing.T) {
	job := storedRenderDocument("render-not-ready", RenderJobStatusUploading).ToRenderJob()
	server := NewServer(Config{APIKey: "api-key", StagingContainer: "render-staging"}, nil)
	server.SetRenderJobs(&staticRenderOutputService{job: job})
	server.SetRenderOutputStreamer(&staticRenderOutputStreamer{data: "video"})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/render-jobs/"+job.ID+"/output", nil)
	req.Header.Set("X-API-Key", "api-key")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, req)
	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestRenderOutputEndpointRejectsUnexpectedBlobIdentity(t *testing.T) {
	job := storedRenderDocument("render-invalid-output", RenderJobStatusSucceeded).ToRenderJob()
	job.Output.BlobName = "render-outputs/another-job/output.mp4"
	streamer := &staticRenderOutputStreamer{data: "video"}
	server := NewServer(Config{APIKey: "api-key", StagingContainer: "render-staging"}, nil)
	server.SetRenderJobs(&staticRenderOutputService{job: job})
	server.SetRenderOutputStreamer(streamer)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/render-jobs/"+job.ID+"/output", nil)
	req.Header.Set("X-API-Key", "api-key")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, req)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if streamer.asset.BlobName != "" {
		t.Fatalf("unexpected blob was opened: %#v", streamer.asset)
	}
}
