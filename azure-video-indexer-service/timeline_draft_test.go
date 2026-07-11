package main

import (
	"reflect"
	"strings"
	"testing"

	"github.com/zecloud/ai-video-studio/internal/videoindexerstudio"
)

func TestBuildTimelineDraftFromSuggestionIsDeterministic(t *testing.T) {
	suggestion := EditSuggestion{
		ID:     "suggestion-1",
		Title:  "Trim to the hook",
		Reason: "Use the strongest beats first.",
		Clips: []SuggestedClip{
			{ID: "duplicate", Title: "Later cut", Reason: "Keep the later beat.", StartMs: 3000, EndMs: 4000},
			{ID: "duplicate", Title: "Opening cut", Reason: "Open with action.", StartMs: 0, EndMs: 1500},
		},
	}
	original := suggestion

	draft, err := buildTimelineDraftFromSuggestion("job-1", "item-001", editPlannerInstructionsVersion, suggestion)
	if err != nil {
		t.Fatalf("build draft: %v", err)
	}
	again, err := buildTimelineDraftFromSuggestion("job-1", "item-001", editPlannerInstructionsVersion, suggestion)
	if err != nil {
		t.Fatalf("build draft again: %v", err)
	}
	if !reflect.DeepEqual(original, suggestion) {
		t.Fatalf("suggestion was mutated: %#v", suggestion)
	}
	if !reflect.DeepEqual(draft, again) {
		t.Fatalf("draft was not deterministic: %#v != %#v", draft, again)
	}
	if err := draft.Validate(); err != nil {
		t.Fatalf("draft validation failed: %v", err)
	}
	clips := draft.PrimaryVideoTrack.Clips
	if len(clips) != 2 {
		t.Fatalf("unexpected clip count: %#v", clips)
	}
	if clips[0].TimelineStartMS != 0 || clips[0].DurationMS != 1000 {
		t.Fatalf("unexpected first clip placement: %#v", clips[0])
	}
	if clips[1].TimelineStartMS != 1000 || clips[1].DurationMS != 1500 {
		t.Fatalf("unexpected second clip placement: %#v", clips[1])
	}
	if clips[0].ID == clips[1].ID {
		t.Fatalf("expected stable unique clip ids: %#v", clips)
	}
	if clips[0].SourceAssetID != "item-001" || clips[1].SourceAssetID != "item-001" {
		t.Fatalf("unexpected source asset ids: %#v", clips)
	}
	if clips[0].Transition.Kind != videoindexerstudio.TimelineTransitionKindCut || clips[1].Transition.Kind != videoindexerstudio.TimelineTransitionKindCut {
		t.Fatalf("unexpected transitions: %#v", clips)
	}
}

func TestBuildTimelineDraftFromSuggestionRejectsEmptySuggestion(t *testing.T) {
	_, err := buildTimelineDraftFromSuggestion("job-1", "item-001", editPlannerInstructionsVersion, EditSuggestion{ID: "suggestion-1"})
	if err == nil || !strings.Contains(err.Error(), "has no clips") {
		t.Fatalf("expected empty suggestion error, got %v", err)
	}
}
