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

func TestExpandedNarrativePacingProfilesHaveBoundedDeterministicSemantics(t *testing.T) {
	candidate := compositionCandidate{clip: videoindexerstudio.SuggestedClip{ID: "clip-1", SourceAssetID: "asset-1", StartMs: 1_000, EndMs: 21_000, Score: 0.8}}
	profiles := map[videoindexerstudio.NarrativePacingProfile]int64{
		videoindexerstudio.NarrativePacingProfileCinematic:       10_000,
		videoindexerstudio.NarrativePacingProfileSocialShortForm: 6_000,
		videoindexerstudio.NarrativePacingProfileTutorial:        14_000,
		videoindexerstudio.NarrativePacingProfileHighlightReel:   7_000,
		videoindexerstudio.NarrativePacingProfileRecap:           15_000,
		videoindexerstudio.NarrativePacingProfileStorytelling:    11_000,
		videoindexerstudio.NarrativePacingProfileTravel:          15_000,
		videoindexerstudio.NarrativePacingProfileInterview:       15_000,
		videoindexerstudio.NarrativePacingProfileProductShowcase: 8_000,
	}
	for profile, wantDuration := range profiles {
		t.Run(string(profile), func(t *testing.T) {
			paced, variants := applyNarrativePacing([]compositionCandidate{candidate}, profile)
			if variants != 1 || paced[0].clip.EndMs-paced[0].clip.StartMs != wantDuration || paced[0].clip.StartMs != candidate.clip.StartMs || paced[0].clip.EndMs > candidate.clip.EndMs {
				t.Fatalf("profile %q escaped its bounded source range: %#v", profile, paced)
			}
			again, againVariants := applyNarrativePacing([]compositionCandidate{candidate}, profile)
			if againVariants != variants || !reflect.DeepEqual(again, paced) {
				t.Fatalf("profile %q pacing was not deterministic", profile)
			}
		})
	}
}

func TestTutorialPacingPreservesContinuityOrder(t *testing.T) {
	candidates := []compositionCandidate{
		{clip: videoindexerstudio.SuggestedClip{ID: "later", SourceAssetID: "asset-a", Score: 1}, sourceStartMS: 5_000},
		{clip: videoindexerstudio.SuggestedClip{ID: "earlier", SourceAssetID: "asset-b", Score: 0.1}, sourceStartMS: 1_000},
	}
	sortPacedCompositionCandidates(candidates, videoindexerstudio.NarrativePacingProfileTutorial)
	if candidates[0].clip.ID != "earlier" {
		t.Fatalf("tutorial candidates were not ordered for continuity: %#v", candidates)
	}
}
