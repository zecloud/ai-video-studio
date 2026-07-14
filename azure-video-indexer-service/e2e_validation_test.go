package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zecloud/ai-video-studio/internal/backend"
	"github.com/zecloud/ai-video-studio/internal/editing"
	"github.com/zecloud/ai-video-studio/internal/mediaservice"
	"github.com/zecloud/ai-video-studio/internal/videoindexerstudio"
)

func TestEndToEndCreateJobCreatesEditProjectFromFixture(t *testing.T) {
	fixturePath := filepath.Join("testdata", "videoindexer_normalization_fixture.json")
	fixture, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	const videoID = "video-root-001"

	var (
		mu          sync.Mutex
		uploadQuery urlValues
		deleteSeen  bool
		indexCalls  int
	)

	viServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/generateAccessToken"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"accessToken":"vi-account-token"}`)
		case strings.HasSuffix(r.URL.Path, "/Videos") && r.Method == http.MethodPost:
			mu.Lock()
			uploadQuery = copyValues(r.URL.Query())
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"`+videoID+`"}`)
		case strings.Contains(r.URL.Path, "/Index") && r.Method == http.MethodGet:
			mu.Lock()
			indexCalls++
			call := indexCalls
			mu.Unlock()
			if call == 1 {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"id":"`+videoID+`","state":"Processing"}`)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(fixture)
		case r.Method == http.MethodDelete:
			mu.Lock()
			deleteSeen = true
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(viServer.Close)

	client, err := NewVideoIndexerClient(VideoIndexerConfig{
		SubscriptionID: "sub-123",
		ResourceGroup:  "rg-123",
		AccountName:    "account-name",
		AccountID:      "account-456",
		Location:       "westus2",
		ARMBaseURL:     viServer.URL,
		APIBaseURL:     viServer.URL,
		PollTimeout:    time.Minute,
	}, staticTokenCredential{token: "arm-token"}, viServer.Client())
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	client.wait = func(context.Context, time.Duration) error { return nil }

	signals := &fakeMediaSignalExtractor{
		signals: MediaSignals{
			Duration: 12500 * time.Millisecond,
			Video: MediaVideoSignals{
				Present: true,
				Codec:   "h264",
				Width:   1920,
				Height:  1080,
				FPS:     30,
			},
			Audio: MediaAudioSignals{
				Present:    true,
				Codec:      "aac",
				Channels:   2,
				SampleRate: 48000,
			},
			Silences: []SilenceInterval{{Start: 2500 * time.Millisecond, End: 3000 * time.Millisecond}},
		},
	}
	normalizer := NewVideoIndexNormalizer(signals)
	planner := &promptAwareEditPlanner{}
	pipeline := NewAzureVideoIndexerPipeline(client, normalizer, planner, fixedClock{now: time.Date(2026, 7, 10, 13, 46, 9, 0, time.UTC)})
	store := newMemoryJobStore()
	stager := &fakeStager{baseURL: "https://staged.example.com"}
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
		SourceName:          "camera clip?.mp4",
		CorrelationID:       "e2e-validation",
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	waitFor(t, 5*time.Second, func() bool {
		stored, err := store.Get(context.Background(), job.ID)
		return err == nil && (stored.Status == JobStatusSucceeded || stored.Status == JobStatusFailed || stored.Status == JobStatusCanceled)
	})

	stored, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if stored.Status != JobStatusSucceeded {
		t.Fatalf("unexpected job status: %s err=%#v checkpoints=%#v", stored.Status, stored.Error, stored.Checkpoints)
	}
	if stored.VideoIndexerVideoID != videoID {
		t.Fatalf("unexpected video id: %q", stored.VideoIndexerVideoID)
	}
	if stored.VideoIndexResult == nil || stored.VideoIndexResult.VideoID != videoID {
		t.Fatalf("expected normalized result, got %#v", stored.VideoIndexResult)
	}
	if len(stored.TimelineDrafts) != 1 {
		t.Fatalf("expected one timeline draft, got %#v", stored.TimelineDrafts)
	}
	if stored.EditPlan == nil || stored.EditPlan.AssetID != "item-001" {
		t.Fatalf("expected validated edit plan asset id, got %#v", stored.EditPlan)
	}
	if got := stored.TimelineDrafts[0].PrimaryVideoTrack.Clips[0].SourceAssetID; got != "item-001" {
		t.Fatalf("unexpected timeline draft source asset id: %q", got)
	}
	if planner.calls != 1 {
		t.Fatalf("expected planner to run once, got %d", planner.calls)
	}
	if signals.calls != 1 {
		t.Fatalf("expected fake FFmpeg extractor to run once, got %d", signals.calls)
	}
	if !strings.Contains(signals.sourceURL, "sig=ephemeral") {
		t.Fatalf("expected staged read URL to be used, got %q", signals.sourceURL)
	}

	for _, payload := range store.payloadStrings(job.ID) {
		if strings.Contains(payload, "token-abc") || strings.Contains(payload, "sig=ephemeral") {
			t.Fatalf("secret leaked into persistent payload: %s", payload)
		}
	}

	stager.mu.Lock()
	if len(stager.deleteCalls) == 0 {
		stager.mu.Unlock()
		t.Fatal("expected staged blob cleanup")
	}
	stager.mu.Unlock()

	mu.Lock()
	if deleteSeen {
		mu.Unlock()
		t.Fatal("unexpected video delete call")
	}
	if got := uploadQuery.Get("externalId"); got != job.ID {
		mu.Unlock()
		t.Fatalf("unexpected external id: %q", got)
	}
	if got := uploadQuery.Get("language"); got != "" {
		mu.Unlock()
		t.Fatalf("unexpected upload language: %q", got)
	}
	if indexCalls < 2 {
		mu.Unlock()
		t.Fatalf("expected poll and processed index calls, got %d", indexCalls)
	}
	mu.Unlock()

	jobStore := &e2eVideoIndexerJobStore{
		jobs: []backend.VideoIndexerStudioJob{
			{
				ID:             stored.ID,
				AssetID:        "asset-1",
				AssetName:      "camera clip?.mp4",
				Status:         "succeeded",
				TimelineDrafts: append([]videoindexerstudio.TimelineDraft(nil), stored.TimelineDrafts...),
				CreatedAt:      stored.CreatedAt,
				UpdatedAt:      stored.UpdatedAt,
			},
		},
	}
	editStore := &e2eEditingProjectStore{}
	studio := backend.NewVideoIndexerStudioService(nil, nil, editStore, nil, jobStore)

	project, err := studio.CreateEditProject(context.Background(), stored.ID, "suggestion-1")
	if err != nil {
		t.Fatalf("CreateEditProject: %v", err)
	}
	if project.ID != e2eDeterministicProjectID(stored.ID, "suggestion-1") {
		t.Fatalf("unexpected project id: %q", project.ID)
	}
	if project.Name != "camera clip?.mp4 - suggestion-1" {
		t.Fatalf("unexpected project name: %q", project.Name)
	}
	if len(project.AssetIDs) != 1 || project.AssetIDs[0] != "item-001" {
		t.Fatalf("unexpected project asset ids: %#v", project.AssetIDs)
	}
	if got := project.Timeline.Tracks[0].Clips[0].SourceAssetID; got != "item-001" {
		t.Fatalf("unexpected project clip source asset id: %q", got)
	}
	if len(editStore.saved) != 1 {
		t.Fatalf("expected one saved project, got %d", len(editStore.saved))
	}

	converted, err := editing.ProjectFromTimelineDraft(stored.TimelineDrafts[0])
	if err != nil {
		t.Fatalf("ProjectFromTimelineDraft: %v", err)
	}
	if !reflect.DeepEqual(project.Timeline, converted.Timeline) || !reflect.DeepEqual(project.AssetIDs, converted.AssetIDs) {
		t.Fatalf("project conversion mismatch: %#v != %#v", project, converted)
	}

	renderBackend := &e2eRenderBackend{}
	editingSvc := backend.NewEditingService(nil, renderBackend, nil, editStore)
	renderJob, err := editingSvc.Render(context.Background(), project.ID)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if renderJob.Status != "completed" {
		t.Fatalf("unexpected render job: %#v", renderJob)
	}
	if len(renderBackend.requests) != 1 {
		t.Fatalf("expected one render request, got %d", len(renderBackend.requests))
	}
	if got := renderBackend.requests[0].Clips[0].Input; got != "item-001" {
		t.Fatalf("unexpected render clip input: %q", got)
	}

	roundTrip, err := json.Marshal(project)
	if err != nil {
		t.Fatalf("marshal project: %v", err)
	}
	var decoded editing.EditProject
	if err := json.Unmarshal(roundTrip, &decoded); err != nil {
		t.Fatalf("unmarshal project: %v", err)
	}
	if !reflect.DeepEqual(project, decoded) {
		t.Fatalf("project JSON round-trip changed data: %#v != %#v", project, decoded)
	}
}

type e2eVideoIndexerJobStore struct {
	jobs []backend.VideoIndexerStudioJob
}

func (s *e2eVideoIndexerJobStore) Load(context.Context) ([]backend.VideoIndexerStudioJob, error) {
	return append([]backend.VideoIndexerStudioJob(nil), s.jobs...), nil
}

func (s *e2eVideoIndexerJobStore) Save(_ context.Context, jobs []backend.VideoIndexerStudioJob) error {
	s.jobs = append([]backend.VideoIndexerStudioJob(nil), jobs...)
	return nil
}

type e2eEditingProjectStore struct {
	saved []editing.EditProject
}

func (s *e2eEditingProjectStore) SaveProject(_ context.Context, project editing.EditProject) (editing.EditProject, error) {
	s.saved = append(s.saved, project)
	return project, nil
}

func (s *e2eEditingProjectStore) Load(context.Context) ([]editing.EditProject, error) {
	return append([]editing.EditProject(nil), s.saved...), nil
}

func (s *e2eEditingProjectStore) Save(_ context.Context, projects []editing.EditProject) error {
	s.saved = append([]editing.EditProject(nil), projects...)
	return nil
}

type e2eRenderBackend struct {
	mu       sync.Mutex
	requests []mediaservice.RenderRequest
}

func (b *e2eRenderBackend) Render(_ context.Context, req mediaservice.RenderRequest) (*mediaservice.RenderResult, error) {
	b.mu.Lock()
	b.requests = append(b.requests, req)
	b.mu.Unlock()
	return &mediaservice.RenderResult{Status: "completed", OutputURL: "https://render.example.com/output.mp4"}, nil
}

func e2eDeterministicProjectID(jobID, suggestionID string) string {
	return "video-indexer-" + e2eSanitizeIdentifier(jobID) + "-" + e2eSanitizeIdentifier(suggestionID)
}

func e2eSanitizeIdentifier(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r
		case r >= '0' && r <= '9':
			return r
		case r == '-', r == '_':
			return r
		default:
			return '-'
		}
	}, value)
	value = strings.Trim(value, "-_")
	if value == "" {
		return "item"
	}
	return value
}

type promptAwareEditPlanner struct {
	calls int
}

func (p *promptAwareEditPlanner) Plan(ctx context.Context, prompt string) (EditPlan, error) {
	p.calls++
	var packet editPlannerEvidencePacket
	if err := json.Unmarshal([]byte(prompt), &packet); err != nil {
		return EditPlan{}, err
	}
	if len(packet.Scenes) == 0 {
		return EditPlan{}, errors.New("edit planner evidence packet has no scenes")
	}
	scene := packet.Scenes[0]
	sourceRefs := []SourceRef{scene.SourceRef}
	if len(scene.Transcript) > 0 {
		sourceRefs = append(sourceRefs, scene.Transcript[0].SourceRef)
	}
	assetID := ""
	if len(packet.SourceAssetIDs) > 0 {
		assetID = packet.SourceAssetIDs[0]
	}
	return EditPlan{
		SchemaVersion: editPlanSchemaVersion,
		VideoID:       packet.VideoID,
		AssetID:       assetID,
		Title:         "Action-first edit",
		Summary:       "Keep the opening action and use the strongest beats.",
		Highlights: []Highlight{{
			ID:         "highlight-1",
			Title:      "Opening action",
			Reason:     "The opening establishes the hook.",
			StartMs:    scene.StartMs,
			EndMs:      scene.EndMs,
			Score:      0.95,
			SourceRefs: sourceRefs,
		}},
		Suggestions: []EditSuggestion{{
			ID:         "suggestion-1",
			Title:      "Trim to the hook",
			Reason:     "Start with the strongest action beat.",
			StartMs:    scene.StartMs,
			EndMs:      scene.EndMs,
			Score:      0.92,
			SourceRefs: sourceRefs,
			Clips: []SuggestedClip{{
				ID:         "clip-1",
				Title:      "First cut",
				Reason:     "Use the opening action without extra filler.",
				StartMs:    scene.StartMs,
				EndMs:      scene.EndMs,
				Score:      0.94,
				SourceRefs: sourceRefs,
			}},
		}},
	}, nil
}

type fakeMediaSignalExtractor struct {
	calls     int
	sourceURL string
	signals   MediaSignals
	err       error
}

func (e *fakeMediaSignalExtractor) Extract(ctx context.Context, sourceURL string) (MediaSignals, error) {
	e.calls++
	e.sourceURL = sourceURL
	if e.err != nil {
		return MediaSignals{}, e.err
	}
	out := e.signals
	out.SourceURL = redactURL(sourceURL)
	return out, nil
}
