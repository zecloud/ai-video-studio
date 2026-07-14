package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAzureVideoIndexerPipelinePersistsVideoIDAndSkipsDelete(t *testing.T) {
	var mu sync.Mutex
	var deleteSeen bool
	var uploadQuery urlValues
	var indexCalls int

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/generateAccessToken"):
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{"accessToken":"vi-account-token"}`)
		case strings.HasSuffix(r.URL.Path, "/Videos") && r.Method == http.MethodPost:
			mu.Lock()
			uploadQuery = copyValues(r.URL.Query())
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{"id":"video-123"}`)
		case strings.Contains(r.URL.Path, "/Index") && r.Method == http.MethodGet:
			mu.Lock()
			indexCalls++
			call := indexCalls
			mu.Unlock()
			if call == 1 {
				w.WriteHeader(http.StatusOK)
				_, _ = io.WriteString(w, `{"id":"video-123","state":"Processing"}`)
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{"id":"video-123","state":"Processed","insights":{"summary":"done"}}`)
		case r.Method == http.MethodDelete:
			mu.Lock()
			deleteSeen = true
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	client, err := NewVideoIndexerClient(VideoIndexerConfig{
		SubscriptionID: "sub-123",
		ResourceGroup:  "rg-123",
		AccountName:    "account-name",
		AccountID:      "account-456",
		Location:       "westus2",
		ARMBaseURL:     server.URL,
		APIBaseURL:     server.URL,
		PollTimeout:    time.Minute,
	}, staticTokenCredential{token: "arm-token"}, server.Client())
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	client.wait = func(context.Context, time.Duration) error { return nil }

	normalizer := &fakeVideoNormalizer{}
	planner := &fakeEditPlanner{plan: testEditPlan("video-123", "item-001")}
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
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	waitFor(t, time.Second, func() bool {
		stored, err := store.Get(context.Background(), job.ID)
		return err == nil && stored.Status == JobStatusSucceeded
	})

	stored, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if stored.VideoIndexerVideoID != "video-123" {
		t.Fatalf("unexpected video id: %q", stored.VideoIndexerVideoID)
	}
	if stored.VideoIndexerState != "Processed" {
		t.Fatalf("unexpected video index state: %q", stored.VideoIndexerState)
	}
	if len(stored.Checkpoints) < 3 {
		t.Fatalf("expected checkpoints, got %#v", stored.Checkpoints)
	}
	if stored.Status != JobStatusSucceeded {
		t.Fatalf("unexpected job status: %s", stored.Status)
	}
	if len(stored.TimelineDrafts) != 1 {
		t.Fatalf("expected one timeline draft, got %#v", stored.TimelineDrafts)
	}
	if stored.EditPlan == nil || stored.EditPlan.AssetID != "item-001" {
		t.Fatalf("expected edit plan asset id to match OneDrive item id, got %#v", stored.EditPlan)
	}
	draft := stored.TimelineDrafts[0]
	if draft.PromptVersion != editPlannerInstructionsVersion {
		t.Fatalf("unexpected prompt version: %#v", draft)
	}
	if draft.PrimaryVideoTrack.Kind != "video" || len(draft.PrimaryVideoTrack.Clips) != 1 {
		t.Fatalf("unexpected draft track: %#v", draft.PrimaryVideoTrack)
	}
	if got := draft.PrimaryVideoTrack.Clips[0].SourceAssetID; got != "item-001" {
		t.Fatalf("unexpected draft source asset id: %q", got)
	}
	if draft.PrimaryVideoTrack.Clips[0].TimelineStartMS != 0 || draft.PrimaryVideoTrack.Clips[0].DurationMS != 4000 {
		t.Fatalf("unexpected draft clip placement: %#v", draft.PrimaryVideoTrack.Clips[0])
	}
	if stored.VideoIndexResult == nil || stored.VideoIndexResult.VideoID != "video-123" {
		t.Fatalf("expected normalized result to persist, got %#v", stored.VideoIndexResult)
	}
	if stored.EditPlan == nil || len(stored.EditPlan.Suggestions) != 1 {
		t.Fatalf("expected edit plan to persist, got %#v", stored.EditPlan)
	}
	if stored.EditPlan.Suggestions[0].Clips[0].Title != "First cut" {
		t.Fatalf("unexpected edit plan: %#v", stored.EditPlan)
	}
	stager.mu.Lock()
	if len(stager.deleteCalls) == 0 {
		stager.mu.Unlock()
		t.Fatal("expected staging cleanup")
	}
	stager.mu.Unlock()
	if normalizer.calls != 1 {
		t.Fatalf("expected normalizer to be called once, got %d", normalizer.calls)
	}
	if planner.calls != 1 {
		t.Fatalf("expected planner to be called once, got %d", planner.calls)
	}
	mu.Lock()
	if deleteSeen {
		mu.Unlock()
		t.Fatal("unexpected video delete call")
	}
	if got := uploadQuery.Get("language"); got != "" {
		mu.Unlock()
		t.Fatalf("unexpected upload language: %q", got)
	}
	if got := uploadQuery.Get("externalId"); got != job.ID {
		mu.Unlock()
		t.Fatalf("unexpected external id: %q", got)
	}
	mu.Unlock()
}

type urlValues map[string][]string

func copyValues(v map[string][]string) urlValues {
	out := make(urlValues, len(v))
	for k, vals := range v {
		out[k] = append([]string(nil), vals...)
	}
	return out
}

func (v urlValues) Get(key string) string {
	if v == nil {
		return ""
	}
	vals := v[key]
	if len(vals) == 0 {
		return ""
	}
	return vals[0]
}

type fakeVideoNormalizer struct {
	calls int
}

func (n *fakeVideoNormalizer) Normalize(ctx context.Context, job JobDocument, asset StagedAsset, readURL string, index VideoIndexData) (VideoIndexResult, error) {
	n.calls++
	return VideoIndexResult{
		VideoID:          index.VideoID(),
		State:            index.State(),
		DurationMs:       4000,
		DetectedLanguage: "en-US",
		SourceIDs:        []string{"source-a"},
		TechnicalSignals: &MediaSignals{
			SourceURL: "https://staged.example.com/input.mp4",
			Duration:  4 * time.Second,
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
		Insights: VideoIndexInsights{
			Speakers: []VideoIndexSpeaker{{
				ID:            "speaker-1",
				Name:          "Speaker One",
				TranscriptIDs: []string{"transcript-1"},
			}},
			Scenes: []VideoIndexScene{{
				ID:      "scene-1",
				StartMs: 0,
				EndMs:   4000,
			}},
			Shots: []VideoIndexShot{{
				ID:          "shot-1",
				StartMs:     0,
				EndMs:       4000,
				Tags:        []string{"intro"},
				KeyframeIDs: []string{"keyframe-1"},
			}},
			Transcript: []VideoIndexTranscript{{
				ID:          "transcript-1",
				SpeakerID:   "speaker-1",
				SpeakerName: "Speaker One",
				StartMs:     500,
				EndMs:       1500,
				Text:        "Open with the action",
				Confidence:  0.9,
			}},
			OCR: []VideoIndexOCR{{
				ID:         "ocr-1",
				Text:       "Camera settings",
				StartMs:    2000,
				EndMs:      2500,
				Confidence: 0.8,
			}},
			Labels: []VideoIndexLabel{{
				ID:          "label-1",
				Name:        "surfing",
				ReferenceID: "ref-1",
				StartMs:     300,
				EndMs:       3200,
				Confidence:  0.88,
			}},
			Objects: []VideoIndexObject{{
				ID:          "object-1",
				Type:        "person",
				DisplayName: "Rider",
				StartMs:     1000,
				EndMs:       3500,
				Confidence:  0.77,
			}},
		},
	}, nil
}

type fakeEditPlanner struct {
	calls int
	plan  EditPlan
	err   error
}

func (p *fakeEditPlanner) Plan(ctx context.Context, prompt string) (EditPlan, error) {
	p.calls++
	if p.err != nil {
		return EditPlan{}, p.err
	}
	return p.plan, nil
}

func testEditPlan(videoID, assetID string) EditPlan {
	sceneRef := SourceRef{
		RefID:         editPlannerSceneRefID("scene-1"),
		SourceKind:    "video_indexer",
		SourceAssetID: assetID,
		FactKind:      "scene",
		StartMs:       0,
		EndMs:         4000,
	}
	transcriptRef := SourceRef{
		RefID:         editPlannerTranscriptRefID("transcript-1"),
		SourceKind:    "video_indexer",
		SourceAssetID: assetID,
		FactKind:      "transcript",
		StartMs:       500,
		EndMs:         1500,
	}
	return EditPlan{
		SchemaVersion: editPlanSchemaVersion,
		VideoID:       videoID,
		AssetID:       assetID,
		Title:         "Action-first edit",
		Summary:       "Keep the opening action and use the strongest beats.",
		Highlights: []Highlight{{
			ID:         "highlight-1",
			Title:      "Opening action",
			Reason:     "The opening establishes the hook.",
			StartMs:    0,
			EndMs:      4000,
			Score:      0.95,
			SourceRefs: []SourceRef{sceneRef, transcriptRef},
		}},
		Suggestions: []EditSuggestion{{
			ID:         "suggestion-1",
			Title:      "Trim to the hook",
			Reason:     "Start with the strongest action beat.",
			StartMs:    0,
			EndMs:      4000,
			Score:      0.92,
			SourceRefs: []SourceRef{sceneRef, transcriptRef},
			Clips: []SuggestedClip{{
				ID:         "clip-1",
				Title:      "First cut",
				Reason:     "Use the opening action without extra filler.",
				StartMs:    0,
				EndMs:      4000,
				Score:      0.94,
				SourceRefs: []SourceRef{sceneRef, transcriptRef},
			}},
		}},
	}
}
