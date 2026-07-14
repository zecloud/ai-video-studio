package editing

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/zecloud/ai-video-studio/internal/videoindexerstudio"
)

func TestProjectFromTimelineDraftPreservesPlacementAndMetadata(t *testing.T) {
	draft := videoindexerstudio.TimelineDraft{
		SchemaVersion: videoindexerstudio.TimelineDraftSchemaVersion,
		OriginJobID:   "job-123",
		SuggestionID:  "suggestion-1",
		PromptVersion: "v2",
		PrimaryVideoTrack: videoindexerstudio.TimelineTrack{
			ID:   "primary-video",
			Kind: videoindexerstudio.TimelineTrackKindVideo,
			Clips: []videoindexerstudio.TimelineClip{
				{
					ID:              "clip-a",
					SourceAssetID:   "asset-1",
					InMS:            100,
					OutMS:           1100,
					TimelineStartMS: 0,
					DurationMS:      1000,
					Transition:      videoindexerstudio.TimelineTransition{Kind: videoindexerstudio.TimelineTransitionKindCut},
				},
				{
					ID:              "clip-b",
					SourceAssetID:   "asset-1",
					InMS:            1100,
					OutMS:           2600,
					TimelineStartMS: 1000,
					DurationMS:      1500,
					Transition:      videoindexerstudio.TimelineTransition{Kind: videoindexerstudio.TimelineTransitionKindCut},
				},
			},
		},
	}
	original := draft

	project, err := ProjectFromTimelineDraft(draft)
	if err != nil {
		t.Fatalf("convert draft: %v", err)
	}
	if !reflect.DeepEqual(original, draft) {
		t.Fatalf("draft was mutated: %#v", draft)
	}
	if project.OriginJobID != "job-123" || project.SuggestionID != "suggestion-1" || project.PromptVersion != "v2" {
		t.Fatalf("unexpected project metadata: %#v", project)
	}
	if len(project.AssetIDs) != 1 || project.AssetIDs[0] != "asset-1" {
		t.Fatalf("unexpected asset ids: %#v", project.AssetIDs)
	}
	if len(project.Timeline.Tracks) != 1 {
		t.Fatalf("unexpected track count: %#v", project.Timeline.Tracks)
	}
	track := project.Timeline.Tracks[0]
	if track.Kind != "video" || len(track.Clips) != 2 {
		t.Fatalf("unexpected track: %#v", track)
	}
	if track.Clips[0].TimelineStartMS != 0 || track.Clips[0].DurationMS != 1000 {
		t.Fatalf("unexpected first clip placement: %#v", track.Clips[0])
	}
	if track.Clips[1].TimelineStartMS != 1000 || track.Clips[1].DurationMS != 1500 {
		t.Fatalf("unexpected second clip placement: %#v", track.Clips[1])
	}
	if track.Clips[0].Transition == nil || track.Clips[0].Transition.Kind != videoindexerstudio.TimelineTransitionKindCut {
		t.Fatalf("unexpected transition: %#v", track.Clips[0].Transition)
	}
}

func TestProjectFromTimelineDraftCollectsMultipleSourceAssets(t *testing.T) {
	draft := videoindexerstudio.TimelineDraft{
		SchemaVersion: videoindexerstudio.TimelineDraftSchemaVersion,
		OriginJobID:   "job-123",
		SuggestionID:  "suggestion-1",
		PromptVersion: "v2",
		PrimaryVideoTrack: videoindexerstudio.TimelineTrack{
			ID:   "primary-video",
			Kind: videoindexerstudio.TimelineTrackKindVideo,
			Clips: []videoindexerstudio.TimelineClip{
				{ID: "clip-a", SourceAssetID: "asset-1", InMS: 0, OutMS: 1000, TimelineStartMS: 0, DurationMS: 1000, Transition: videoindexerstudio.TimelineTransition{Kind: videoindexerstudio.TimelineTransitionKindCut}},
				{ID: "clip-b", SourceAssetID: "asset-2", InMS: 1000, OutMS: 2000, TimelineStartMS: 1000, DurationMS: 1000, Transition: videoindexerstudio.TimelineTransition{Kind: videoindexerstudio.TimelineTransitionKindCut}},
			},
		},
	}

	project, err := ProjectFromTimelineDraft(draft)
	if err != nil {
		t.Fatalf("convert multi-source draft: %v", err)
	}
	if !reflect.DeepEqual(project.AssetIDs, []string{"asset-1", "asset-2"}) {
		t.Fatalf("unexpected asset ids: %#v", project.AssetIDs)
	}
	if got := project.Timeline.Tracks[0].Clips[1].SourceAssetID; got != "asset-2" {
		t.Fatalf("second clip source asset id = %q", got)
	}
}

func TestEditProjectBackwardCompatibleJSON(t *testing.T) {
	raw := []byte(`{
		"id":"project-1",
		"name":"Draft",
		"assetIds":["asset-1"],
		"timeline":{"tracks":[{"id":"primary-video","kind":"video","clips":[{"id":"clip-a","sourceAssetId":"asset-1","inMs":0,"outMs":1000}]}]},
		"renderPreset":"h264-1080p"
	}`)

	var project EditProject
	if err := json.Unmarshal(raw, &project); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if project.OriginJobID != "" || project.SuggestionID != "" || project.PromptVersion != "" {
		t.Fatalf("unexpected origin metadata: %#v", project)
	}
	if len(project.Timeline.Tracks) != 1 || len(project.Timeline.Tracks[0].Clips) != 1 {
		t.Fatalf("unexpected timeline: %#v", project.Timeline)
	}
	clip := project.Timeline.Tracks[0].Clips[0]
	if clip.TimelineStartMS != 0 || clip.DurationMS != 0 {
		t.Fatalf("expected zero-value placement fields, got %#v", clip)
	}
	if clip.Transition != nil {
		t.Fatalf("expected nil transition for legacy JSON, got %#v", clip.Transition)
	}
}
