package backend

import (
	"reflect"
	"testing"

	"github.com/zecloud/ai-video-studio/internal/videoindexerstudio"
)

func TestBuildMultiVideoCompositionWithIntentBoundsGroundedVariants(t *testing.T) {
	dependencies := []VideoIndexerStudioJob{
		completedAnalysisJob("analysis-1", "asset-1", 1_000, 21_000, 0.9),
		completedAnalysisJob("analysis-2", "asset-2", 2_000, 22_000, 0.8),
	}
	_, composition, _, err := buildMultiVideoCompositionWithIntent("composition-1", []string{"asset-1", "asset-2"}, dependencies, "energetic social")
	if err != nil {
		t.Fatalf("build composition: %v", err)
	}
	if composition.PacingProfile != videoindexerstudio.NarrativePacingProfileEnergeticShortForm || composition.VariantCount != 2 {
		t.Fatalf("pacing metadata = %#v", composition)
	}
	for _, clip := range composition.Clips {
		if clip.EndMs-clip.StartMs != narrativeEnergeticMaximumMS || clip.StartMs < 0 || clip.EndMs > 22_000 || clip.SuggestionID != "best" {
			t.Fatalf("variant escaped its grounded source range: %#v", clip)
		}
	}
}

func TestBuildMultiVideoCompositionWithIntentIsDeterministicAndDefaultsSafely(t *testing.T) {
	dependencies := []VideoIndexerStudioJob{
		completedAnalysisJob("analysis-1", "asset-1", 1_000, 21_000, 0.9),
		completedAnalysisJob("analysis-2", "asset-2", 2_000, 22_000, 0.8),
	}
	_, withoutIntent, _, err := buildMultiVideoComposition("composition-1", []string{"asset-1", "asset-2"}, dependencies)
	if err != nil {
		t.Fatalf("build default composition: %v", err)
	}
	_, unknownIntent, _, err := buildMultiVideoCompositionWithIntent("composition-1", []string{"asset-1", "asset-2"}, dependencies, "make it memorable")
	if err != nil {
		t.Fatalf("build unknown intent composition: %v", err)
	}
	if !reflect.DeepEqual(withoutIntent.Clips, unknownIntent.Clips) || unknownIntent.VariantCount != 0 || unknownIntent.PacingProfile != videoindexerstudio.NarrativePacingProfileStandard {
		t.Fatalf("unknown intent changed standard selection: %#v %#v", withoutIntent, unknownIntent)
	}
	_, first, firstDrafts, err := buildMultiVideoCompositionWithIntent("composition-1", []string{"asset-1", "asset-2"}, dependencies, "calm recap")
	if err != nil {
		t.Fatalf("build first calm composition: %v", err)
	}
	_, second, secondDrafts, err := buildMultiVideoCompositionWithIntent("composition-1", []string{"asset-1", "asset-2"}, dependencies, "calm recap")
	if err != nil || !reflect.DeepEqual(first, second) || !reflect.DeepEqual(firstDrafts, secondDrafts) {
		t.Fatalf("paced composition must be deterministic: %#v %#v %v", first, second, err)
	}
}

func TestChronologicalPacingOrdersLocalCandidatesBySourceTime(t *testing.T) {
	dependencies := []VideoIndexerStudioJob{
		completedAnalysisJob("analysis-1", "asset-1", 10_000, 30_000, 0.9),
		completedAnalysisJob("analysis-2", "asset-2", 1_000, 21_000, 0.8),
	}
	_, composition, _, err := buildMultiVideoCompositionWithIntent("composition-1", []string{"asset-1", "asset-2"}, dependencies, "chronological continuity")
	if err != nil {
		t.Fatalf("build chronological composition: %v", err)
	}
	if composition.PacingProfile != videoindexerstudio.NarrativePacingProfileChronologicalContinuity || composition.Clips[0].StartMs != 1_000 || composition.Clips[0].EndMs-composition.Clips[0].StartMs != narrativeContinuityMaximumMS {
		t.Fatalf("chronological pacing did not select local continuity order: %#v", composition.Clips)
	}
}
