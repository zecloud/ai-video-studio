package backend

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zecloud/ai-video-studio/internal/editing"
	"github.com/zecloud/ai-video-studio/internal/library"
	"github.com/zecloud/ai-video-studio/internal/mediaservice"
	"github.com/zecloud/ai-video-studio/internal/onedrive"
	"github.com/zecloud/ai-video-studio/internal/videoindexerstudio"
)

type renderTestTokenProvider struct{}

func (renderTestTokenProvider) AccessToken(context.Context, []string) (string, error) {
	return "delegated-token", nil
}

type fakeAsyncRenderClient struct {
	mu          sync.Mutex
	createReq   videoindexerstudio.CreateRenderJobRequest
	getCalls    int
	cancelCalls int
	output      string
}

func (f *fakeAsyncRenderClient) CreateRenderJob(_ context.Context, req videoindexerstudio.CreateRenderJobRequest) (*videoindexerstudio.RenderJobResponse, error) {
	f.mu.Lock()
	f.createReq = req
	f.mu.Unlock()
	return &videoindexerstudio.RenderJobResponse{Job: videoindexerstudio.RenderJob{ID: "remote-render-1", ProjectID: req.ProjectID, Status: videoindexerstudio.RenderJobStatusQueued, Preset: req.Preset, OutputName: req.OutputName}}, nil
}

func (f *fakeAsyncRenderClient) GetRenderJob(context.Context, string) (*videoindexerstudio.RenderJobResponse, error) {
	f.mu.Lock()
	f.getCalls++
	f.mu.Unlock()
	return &videoindexerstudio.RenderJobResponse{Job: videoindexerstudio.RenderJob{ID: "remote-render-1", ProjectID: "project-1", Status: videoindexerstudio.RenderJobStatusSucceeded, Preset: "mpeg4-1080p", OutputName: "My edit.mp4", Output: &videoindexerstudio.RenderOutput{Size: int64(len(f.output)), MediaType: "video/mp4"}}}, nil
}

func (f *fakeAsyncRenderClient) CancelRenderJob(context.Context, string) (*videoindexerstudio.RenderJobResponse, error) {
	f.mu.Lock()
	f.cancelCalls++
	f.mu.Unlock()
	return &videoindexerstudio.RenderJobResponse{Job: videoindexerstudio.RenderJob{ID: "remote-render-1", ProjectID: "project-1", Status: videoindexerstudio.RenderJobStatusCanceled, Preset: "mpeg4-1080p", OutputName: "My edit.mp4"}}, nil
}

func (f *fakeAsyncRenderClient) OpenRenderOutput(context.Context, string) (*videoindexerstudio.RenderOutputStream, error) {
	return &videoindexerstudio.RenderOutputStream{Body: io.NopCloser(strings.NewReader(f.output)), ContentLength: int64(len(f.output)), ContentType: "video/mp4", FileName: "My edit.mp4"}, nil
}

func TestEditingServiceAsyncRenderDownloadsAndPublishesToOneDrive(t *testing.T) {
	var uploaded string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/createUploadSession"):
			_ = json.NewEncoder(w).Encode(map[string]any{"uploadUrl": "http://" + r.Host + "/upload", "nextExpectedRanges": []string{"0-"}})
		case r.Method == http.MethodPut && r.URL.Path == "/upload":
			data, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatal(err)
			}
			uploaded += string(data)
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "drive-render-1", "name": "My edit.mp4", "size": len(uploaded)})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	lib := &fakeLibraryStore{assets: []library.ProjectAsset{{ID: "asset-1", Name: "source.mp4", CloudAssetID: "drive-source-1"}}}
	od := &onedrive.Client{HTTPClient: server.Client(), TokenProvider: renderTestTokenProvider{}, GraphBaseURL: server.URL, Scopes: onedrive.DefaultGraphScopes, Destination: onedrive.OneDriveDestination{Mode: "app_folder"}}
	remote := &fakeAsyncRenderClient{output: "rendered-video"}
	service := NewEditingService(lib, nil, od, &memoryEditingProjectStore{})
	service.renderJobFactory = func(context.Context) (asyncRenderJobClient, error) { return remote, nil }
	service.renderPollInterval = time.Millisecond
	_, err := service.SaveProject(context.Background(), editing.EditProject{ID: "project-1", Name: "My edit", Timeline: editing.Timeline{Tracks: []editing.Track{{ID: "video", Kind: "video", Clips: []editing.ClipSegment{{ID: "clip-1", SourceAssetID: "asset-1", InMS: 100, OutMS: 1100}}}}}})
	if err != nil {
		t.Fatal(err)
	}
	job, err := service.Render(context.Background(), "project-1")
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if job.Status != "completed" || job.RemoteJobID != "remote-render-1" || job.OutputDriveItemID != "drive-render-1" || job.Percent != 100 {
		t.Fatalf("unexpected render job: %#v", job)
	}
	if uploaded != remote.output {
		t.Fatalf("uploaded = %q, want %q", uploaded, remote.output)
	}
	remote.mu.Lock()
	request := remote.createReq
	remote.mu.Unlock()
	if len(request.Clips) != 1 || request.Clips[0].OneDriveItemID != "drive-source-1" || request.OneDriveAccessToken != "delegated-token" {
		t.Fatalf("unexpected render request: %#v", request)
	}
	if job.OutputURL != "" {
		t.Fatalf("render job exposed output URL %q", job.OutputURL)
	}
}

type cancelRaceRenderClient struct {
	started     chan struct{}
	startedOnce sync.Once
	mu          sync.Mutex
	cancelCalls int
}

func (c *cancelRaceRenderClient) CreateRenderJob(_ context.Context, req videoindexerstudio.CreateRenderJobRequest) (*videoindexerstudio.RenderJobResponse, error) {
	return &videoindexerstudio.RenderJobResponse{Job: videoindexerstudio.RenderJob{ID: "remote-cancel-race", ProjectID: req.ProjectID, Status: videoindexerstudio.RenderJobStatusQueued, Preset: req.Preset, OutputName: req.OutputName}}, nil
}

func (c *cancelRaceRenderClient) GetRenderJob(ctx context.Context, _ string) (*videoindexerstudio.RenderJobResponse, error) {
	c.startedOnce.Do(func() { close(c.started) })
	<-ctx.Done()
	return nil, ctx.Err()
}

func (c *cancelRaceRenderClient) CancelRenderJob(context.Context, string) (*videoindexerstudio.RenderJobResponse, error) {
	c.mu.Lock()
	c.cancelCalls++
	c.mu.Unlock()
	return &videoindexerstudio.RenderJobResponse{Job: videoindexerstudio.RenderJob{ID: "remote-cancel-race", Status: videoindexerstudio.RenderJobStatusCanceled}}, nil
}

func (*cancelRaceRenderClient) OpenRenderOutput(context.Context, string) (*videoindexerstudio.RenderOutputStream, error) {
	return nil, errors.New("output must not be opened after cancellation")
}

func TestEditingServiceCancellationDuringPollRemainsCanceled(t *testing.T) {
	lib := &fakeLibraryStore{assets: []library.ProjectAsset{{ID: "asset-1", Name: "source.mp4", CloudAssetID: "drive-source-1"}}}
	od := &onedrive.Client{TokenProvider: renderTestTokenProvider{}, Scopes: onedrive.DefaultGraphScopes}
	remote := &cancelRaceRenderClient{started: make(chan struct{})}
	service := NewEditingService(lib, nil, od, &memoryEditingProjectStore{})
	service.renderJobFactory = func(context.Context) (asyncRenderJobClient, error) { return remote, nil }
	service.renderPollInterval = time.Millisecond
	_, err := service.SaveProject(context.Background(), editing.EditProject{ID: "project-1", Name: "Cancel edit", Timeline: editing.Timeline{Tracks: []editing.Track{{ID: "video", Kind: "video", Clips: []editing.ClipSegment{{ID: "clip-1", SourceAssetID: "asset-1", InMS: 100, OutMS: 1100}}}}}})
	if err != nil {
		t.Fatal(err)
	}

	renderDone := make(chan error, 1)
	go func() {
		_, renderErr := service.Render(context.Background(), "project-1")
		renderDone <- renderErr
	}()
	select {
	case <-remote.started:
	case <-time.After(time.Second):
		t.Fatal("render poll did not start")
	}
	jobs, err := service.RenderJobs()
	if err != nil || len(jobs) != 1 {
		t.Fatalf("RenderJobs() = %#v, %v", jobs, err)
	}
	canceled, err := service.CancelRender(context.Background(), jobs[0].ID)
	if err != nil {
		t.Fatalf("CancelRender() error = %v", err)
	}
	if canceled.Status != "cancellation_requested" && canceled.Status != "canceled" {
		t.Fatalf("CancelRender() returned stale job: %#v", canceled)
	}
	select {
	case err := <-renderDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Render() error = %v, want context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("render did not stop after cancellation")
	}
	stored, err := service.RenderJob(jobs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != "canceled" || stored.ErrorDetail != "" {
		t.Fatalf("cancellation was overwritten: %#v", stored)
	}
	remote.mu.Lock()
	cancelCalls := remote.cancelCalls
	remote.mu.Unlock()
	if cancelCalls == 0 {
		t.Fatal("remote cancellation was not requested")
	}
}

type blockingLegacyRenderBackend struct {
	started chan struct{}
	once    sync.Once
}

func (b *blockingLegacyRenderBackend) Render(ctx context.Context, _ mediaservice.RenderRequest) (*mediaservice.RenderResult, error) {
	b.once.Do(func() { close(b.started) })
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestEditingServiceRejectsRenderWhenAsyncIsUnconfigured(t *testing.T) {
	legacy := &blockingLegacyRenderBackend{started: make(chan struct{})}
	od := &onedrive.Client{TokenProvider: renderTestTokenProvider{}, Scopes: onedrive.DefaultGraphScopes}
	service := NewEditingService(&fakeLibraryStore{}, legacy, od, &memoryEditingProjectStore{})
	service.renderJobFactory = func(context.Context) (asyncRenderJobClient, error) { return nil, errNotConfigured }
	_, err := service.SaveProject(context.Background(), editing.EditProject{ID: "legacy-project", Name: "Legacy edit", Timeline: editing.Timeline{Tracks: []editing.Track{{ID: "video", Kind: "video", Clips: []editing.ClipSegment{{ID: "clip-1", SourceAssetID: "asset-1", InMS: 0, OutMS: 1000}}}}}})
	if err != nil {
		t.Fatal(err)
	}

	_, err = service.Render(context.Background(), "legacy-project")
	if !errors.Is(err, errNotConfigured) {
		t.Fatalf("Render() error = %v, want async configuration error", err)
	}
	select {
	case <-legacy.started:
		t.Fatal("legacy render started despite missing async configuration")
	default:
	}
}

func TestEditingServicePreservesSavedCutTransition(t *testing.T) {
	service := NewEditingService(&fakeLibraryStore{assets: []library.ProjectAsset{
		{ID: "asset-1", Name: "first.mp4", CloudAssetID: "drive-source-1"},
		{ID: "asset-2", Name: "second.mp4", CloudAssetID: "drive-source-2"},
	}}, nil, nil, nil)
	project := editing.EditProject{
		ID: "project-cut-transition",
		Timeline: editing.Timeline{Tracks: []editing.Track{{
			Kind: "video",
			Clips: []editing.ClipSegment{
				{ID: "clip-1", SourceAssetID: "asset-1", InMS: 0, OutMS: 1000},
				{ID: "clip-2", SourceAssetID: "asset-2", InMS: 100, OutMS: 1200, Transition: &editing.Transition{Kind: "CUT", DurationMS: 0}},
			},
		}}},
	}
	_, transitions, _, err := service.buildAsyncRenderTimeline(context.Background(), project)
	if err != nil {
		t.Fatalf("buildAsyncRenderTimeline() error = %v", err)
	}
	if len(transitions) != 1 || transitions[0].Kind != "cut" || transitions[0].DurationMS != 0 {
		t.Fatalf("saved cut transition was not preserved: %#v", transitions)
	}
}
func TestEditingServiceRejectsUnsupportedSavedTransition(t *testing.T) {
	service := NewEditingService(&fakeLibraryStore{assets: []library.ProjectAsset{{ID: "asset-1", Name: "source.mp4", CloudAssetID: "drive-source-1"}}}, nil, nil, nil)
	project := editing.EditProject{
		ID: "project-transition",
		Timeline: editing.Timeline{Tracks: []editing.Track{{
			Kind: "video",
			Clips: []editing.ClipSegment{{
				ID: "clip-1", SourceAssetID: "asset-1", InMS: 0, OutMS: 1000,
				Transition: &editing.Transition{Kind: "fade", DurationMS: 250},
			}},
		}}},
	}
	_, _, _, err := service.buildAsyncRenderTimeline(context.Background(), project)
	if err == nil || !strings.Contains(err.Error(), "unsupported transition") {
		t.Fatalf("buildAsyncRenderTimeline() error = %v", err)
	}
}
