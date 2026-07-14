package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/microsoft/agent-framework-go/agent/format/jsonformat"
)

func TestEditPlanStructuredOutputSchemaRequiresEveryProperty(t *testing.T) {
	format, err := jsonformat.For[EditPlan]()
	if err != nil {
		t.Fatalf("generate EditPlan response format: %v", err)
	}
	schema, err := json.Marshal(format.Schema)
	if err != nil {
		t.Fatalf("marshal EditPlan schema: %v", err)
	}

	var root map[string]any
	if err := json.Unmarshal(schema, &root); err != nil {
		t.Fatalf("decode EditPlan schema: %v", err)
	}
	assertStrictObjectSchemas(t, root, "EditPlan")
}

func assertStrictObjectSchemas(t *testing.T, node any, path string) {
	t.Helper()
	switch value := node.(type) {
	case map[string]any:
		properties, hasProperties := value["properties"].(map[string]any)
		if hasProperties {
			if additional, ok := value["additionalProperties"].(bool); !ok || additional {
				t.Errorf("%s must set additionalProperties to false", path)
			}
			requiredValues, ok := value["required"].([]any)
			if !ok {
				t.Fatalf("%s has properties but no required array", path)
			}
			required := make(map[string]struct{}, len(requiredValues))
			for _, item := range requiredValues {
				name, ok := item.(string)
				if !ok {
					t.Fatalf("%s has a non-string required entry: %#v", path, item)
				}
				required[name] = struct{}{}
			}
			for name := range properties {
				if _, ok := required[name]; !ok {
					t.Errorf("%s is missing required property %q", path, name)
				}
			}
			if len(required) != len(properties) {
				t.Errorf("%s requires %d properties but defines %d", path, len(required), len(properties))
			}
		}
		for name, child := range value {
			assertStrictObjectSchemas(t, child, fmt.Sprintf("%s.%s", path, name))
		}
	case []any:
		for i, child := range value {
			assertStrictObjectSchemas(t, child, fmt.Sprintf("%s[%d]", path, i))
		}
	}
}

func TestBuildEditPlannerPromptRejectsOversizedPacket(t *testing.T) {
	result := normalizedTestResult(t)
	result.Insights.Transcript[0].Text = strings.Repeat("x", 70000)

	_, _, err := buildEditPlannerPrompt(
		context.Background(),
		JobDocument{ID: "job-1", OneDriveItemID: "item-001", VideoIndexerVideoID: "video-123"},
		StagedAsset{},
		result,
		1024,
		nil,
	)
	if err == nil {
		t.Fatal("expected oversized packet error")
	}
	var tooLarge *editPlannerEvidenceTooLargeError
	if !errors.As(err, &tooLarge) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateEditPlanRejectsUnknownCitation(t *testing.T) {
	_, index := normalizedEvidenceIndex(t)
	plan := testEditPlan("video-123", "item-001")
	plan.Highlights[0].SourceRefs = []SourceRef{{
		RefID:         "vi:scene:missing",
		SourceKind:    "video_indexer",
		SourceAssetID: "item-001",
	}}

	_, err := validateEditPlan(plan, index)
	if err == nil || !strings.Contains(err.Error(), "unknown source ref") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateEditPlanRejectsOutOfRangeClip(t *testing.T) {
	_, index := normalizedEvidenceIndex(t)
	plan := testEditPlan("video-123", "item-001")
	plan.Suggestions[0].Clips[0].EndMs = 5001

	_, err := validateEditPlan(plan, index)
	if err == nil || !strings.Contains(err.Error(), "exceeds duration") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateEditPlanDedupesDuplicateClips(t *testing.T) {
	_, index := normalizedEvidenceIndex(t)
	plan := testEditPlan("video-123", "item-001")
	plan.Suggestions[0].Clips = append(plan.Suggestions[0].Clips, plan.Suggestions[0].Clips[0])

	validated, err := validateEditPlan(plan, index)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if len(validated.Suggestions[0].Clips) != 1 {
		t.Fatalf("expected duplicate clip to be removed, got %#v", validated.Suggestions[0].Clips)
	}
}

func TestValidateEditPlanAllowsIndependentSuggestionsBeyondSourceDurationInAggregate(t *testing.T) {
	_, index := normalizedEvidenceIndex(t)
	plan := testEditPlan("video-123", "item-001")
	alternative := plan.Suggestions[0]
	alternative.ID = "suggestion-2"
	alternative.Title = "Alternative cut"
	alternative.Clips[0].ID = "clip-2"
	alternative.Clips[0].Title = "Alternative first cut"
	plan.Suggestions = append(plan.Suggestions, alternative)

	validated, err := validateEditPlan(plan, index)
	if err != nil {
		t.Fatalf("validate independent suggestions: %v", err)
	}
	if len(validated.Suggestions) != 2 {
		t.Fatalf("expected two independent suggestions, got %#v", validated.Suggestions)
	}
}

func TestValidateEditPlanRejectsSuggestionBeyondDurationLimit(t *testing.T) {
	_, index := normalizedEvidenceIndex(t)
	index.DurationMs = int64(30 * time.Minute / time.Millisecond)
	plan := testEditPlan("video-123", "item-001")
	plan.Suggestions[0].EndMs = int64(21 * time.Minute / time.Millisecond)
	base := plan.Suggestions[0].Clips[0]
	plan.Suggestions[0].Clips = []SuggestedClip{
		clipWithRange(base, "clip-1", 0, 10*time.Minute),
		clipWithRange(base, "clip-2", 10*time.Minute, 20*time.Minute),
		clipWithRange(base, "clip-3", 20*time.Minute, 21*time.Minute),
	}

	_, err := validateEditPlan(plan, index)
	if err == nil || !strings.Contains(err.Error(), "suggestions[0] total clip duration 1260000ms exceeds limit 1200000ms") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateEditPlanCountsOverlappingClipsOnce(t *testing.T) {
	_, index := normalizedEvidenceIndex(t)
	plan := testEditPlan("video-123", "item-001")
	base := plan.Suggestions[0].Clips[0]
	plan.Suggestions[0].Clips = []SuggestedClip{
		clipWithRange(base, "clip-1", 0, 3*time.Second),
		clipWithRange(base, "clip-2", time.Second, 4*time.Second),
	}

	if _, err := validateEditPlan(plan, index); err != nil {
		t.Fatalf("validate overlapping clips: %v", err)
	}
}

func TestMergedClipDurationCountsOverlapsOnce(t *testing.T) {
	clips := []SuggestedClip{
		{StartMs: 0, EndMs: 10_000},
		{StartMs: 5_000, EndMs: 15_000},
		{StartMs: 20_000, EndMs: 22_000},
	}
	if got := mergedClipDuration(clips); got != 17_000 {
		t.Fatalf("merged clip duration = %d, want 17000", got)
	}
}

func TestMergedClipDurationCountsOverlapsAcrossAssetsSeparately(t *testing.T) {
	clips := []SuggestedClip{
		{SourceAssetID: "asset-1", StartMs: 0, EndMs: 10_000},
		{SourceAssetID: "asset-2", StartMs: 0, EndMs: 10_000},
	}
	if got := mergedClipDuration(clips); got != 20_000 {
		t.Fatalf("merged clip duration = %d, want 20000", got)
	}
}

func clipWithRange(base SuggestedClip, id string, start, end time.Duration) SuggestedClip {
	base.ID = id
	base.Title = id
	base.StartMs = int64(start / time.Millisecond)
	base.EndMs = int64(end / time.Millisecond)
	return base
}

func TestPipelinePreservesNormalizedResultOnPlannerFailure(t *testing.T) {
	client := &fakeVideoIndexerClient{
		uploadVideoID: "video-123",
		index: RawVideoIndex{
			videoID: "video-123",
			state:   "Processed",
			raw:     []byte(`{"id":"video-123","state":"Processed"}`),
		},
	}
	normalizer := &fakeVideoNormalizer{}
	planner := &fakeEditPlanner{err: errors.New("planner failed")}
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
		return err == nil && stored.Status == JobStatusFailed
	})

	stored, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if stored.Status != JobStatusFailed {
		t.Fatalf("unexpected job status: %s", stored.Status)
	}
	if stored.VideoIndexResult == nil || stored.VideoIndexResult.VideoID != "video-123" {
		t.Fatalf("expected normalized result to persist, got %#v", stored.VideoIndexResult)
	}
	if stored.EditPlan != nil {
		t.Fatalf("expected edit plan to remain unset, got %#v", stored.EditPlan)
	}
	if planner.calls != 1 {
		t.Fatalf("expected planner to be called once, got %d", planner.calls)
	}
	stager.mu.Lock()
	defer stager.mu.Unlock()
	if len(stager.deleteCalls) == 0 {
		t.Fatal("expected staging cleanup")
	}
}

func TestPipelineFailsWhenNoTimelineDraftsAreGenerated(t *testing.T) {
	client := &fakeVideoIndexerClient{
		uploadVideoID: "video-123",
		index: RawVideoIndex{
			videoID: "video-123",
			state:   "Processed",
			raw:     []byte(`{"id":"video-123","state":"Processed"}`),
		},
	}
	normalizer := &fakeVideoNormalizer{}
	plan := testEditPlan("video-123", "item-001")
	plan.Suggestions = nil
	planner := &fakeEditPlanner{plan: plan}
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
		return err == nil && stored.Status == JobStatusFailed
	})

	stored, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if stored.Status != JobStatusFailed {
		t.Fatalf("unexpected job status: %s", stored.Status)
	}
	if stored.Error == nil || stored.Error.Code != "no_valid_timeline_drafts" {
		t.Fatalf("expected no timeline drafts error, got %#v", stored.Error)
	}
	if len(stored.TimelineDrafts) != 0 {
		t.Fatalf("expected no drafts to persist, got %#v", stored.TimelineDrafts)
	}
}

func normalizedTestResult(t *testing.T) VideoIndexResult {
	t.Helper()
	result, _ := normalizedEvidenceIndex(t)
	return result
}

func normalizedEvidenceIndex(t *testing.T) (VideoIndexResult, editPlannerEvidenceIndex) {
	t.Helper()
	normalizer := &fakeVideoNormalizer{}
	result, err := normalizer.Normalize(context.Background(), JobDocument{OneDriveItemID: "item-001", VideoIndexerVideoID: "video-123"}, StagedAsset{}, "https://staged.example.com/input.mp4", RawVideoIndex{videoID: "video-123", state: "Processed", raw: []byte(`{"id":"video-123","state":"Processed"}`)})
	if err != nil {
		t.Fatalf("normalize fixture: %v", err)
	}
	_, index, err := buildEditPlannerPrompt(context.Background(), JobDocument{ID: "job-1", OneDriveItemID: "item-001", VideoIndexerVideoID: "video-123"}, StagedAsset{}, result, defaultEditPlannerEvidenceLimit, nil)
	if err != nil {
		t.Fatalf("build evidence: %v", err)
	}
	return result, index
}

type fakeVideoIndexerClient struct {
	uploadVideoID string
	index         VideoIndexData
	pollTimeout   time.Duration
	uploadCalls   int
	pollCalls     int
}

func (c *fakeVideoIndexerClient) UploadVideoURL(ctx context.Context, readURL, sourceName, externalID string) (string, error) {
	c.uploadCalls++
	if c.uploadVideoID == "" {
		return externalID, nil
	}
	return c.uploadVideoID, nil
}

func (c *fakeVideoIndexerClient) PollVideoIndex(ctx context.Context, videoID string, timeout time.Duration) (VideoIndexData, error) {
	c.pollCalls++
	if c.index == nil {
		return RawVideoIndex{videoID: videoID, state: "Processed", raw: []byte(`{"id":"` + videoID + `","state":"Processed"}`)}, nil
	}
	return c.index, nil
}

func (c *fakeVideoIndexerClient) PollTimeout() time.Duration {
	if c.pollTimeout <= 0 {
		return time.Minute
	}
	return c.pollTimeout
}
