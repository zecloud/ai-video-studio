package backend

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/zecloud/ai-video-studio/internal/library"
	"github.com/zecloud/ai-video-studio/internal/videoindexerstudio"
)

type rankingClientFunc func(context.Context, videoindexerstudio.NarrativeRankingRequest) (*videoindexerstudio.NarrativeRankingResponse, error)

type classifierClientFunc func(context.Context, videoindexerstudio.NarrativeIntentClassificationRequest) (*videoindexerstudio.NarrativeIntentClassificationResponse, error)

type classifierAndRankingClient struct {
	classifier classifierClientFunc
	ranker     rankingClientFunc
}

func (c classifierAndRankingClient) CreateJob(context.Context, videoindexerstudio.CreateJobRequest) (*videoindexerstudio.JobResponse, error) {
	return nil, errors.New("not implemented")
}

func (c classifierAndRankingClient) GetJob(context.Context, string) (*videoindexerstudio.JobResponse, error) {
	return nil, errors.New("not implemented")
}

func (c classifierAndRankingClient) CancelJob(context.Context, string) (*videoindexerstudio.JobResponse, error) {
	return nil, errors.New("not implemented")
}

func (c classifierAndRankingClient) ClassifyNarrativeIntent(ctx context.Context, req videoindexerstudio.NarrativeIntentClassificationRequest) (*videoindexerstudio.NarrativeIntentClassificationResponse, error) {
	return c.classifier(ctx, req)
}

func (c classifierAndRankingClient) RankNarrative(ctx context.Context, req videoindexerstudio.NarrativeRankingRequest) (*videoindexerstudio.NarrativeRankingResponse, error) {
	return c.ranker(ctx, req)
}

func (f rankingClientFunc) RankNarrative(ctx context.Context, request videoindexerstudio.NarrativeRankingRequest) (*videoindexerstudio.NarrativeRankingResponse, error) {
	return f(ctx, request)
}

func narrativeDependencies() []VideoIndexerStudioJob {
	dependencies := []VideoIndexerStudioJob{completedAnalysisJob("analysis-a", "asset-a", 0, 1000, .8), completedAnalysisJob("analysis-b", "asset-b", 100, 1100, .9)}
	for index := range dependencies {
		dependencies[index].VideoIndexResult.Insights.Scenes = []videoindexerstudio.VideoIndexScene{{ID: "scene", StartMs: 0, EndMs: 2000}}
	}
	return dependencies
}

func TestRankMultiVideoCompositionPreservesKnownClips(t *testing.T) {
	dependencies := narrativeDependencies()
	plan, composition, _, err := buildMultiVideoComposition("composition-1", []string{"asset-a", "asset-b"}, dependencies)
	if err != nil {
		t.Fatalf("build composition: %v", err)
	}
	ranked, rankedComposition, drafts, err := rankMultiVideoComposition(context.Background(), rankingClientFunc(func(_ context.Context, request videoindexerstudio.NarrativeRankingRequest) (*videoindexerstudio.NarrativeRankingResponse, error) {
		return &videoindexerstudio.NarrativeRankingResponse{SchemaVersion: 1, OrderedClips: []videoindexerstudio.NarrativeRankedClip{{CandidateID: request.Candidates[1].ID, EvidenceIDs: []string{request.Candidates[1].EvidenceIDs[0]}}, {CandidateID: request.Candidates[0].ID, EvidenceIDs: []string{request.Candidates[0].EvidenceIDs[0]}}}}, nil
	}), plan, composition, dependencies)
	if err != nil {
		t.Fatalf("rank composition: %v", err)
	}
	if rankedComposition.RankingMode != "azure_openai_grounded_v1" || rankedComposition.Clips[0].ID != composition.Clips[1].ID || drafts[0].PrimaryVideoTrack.Clips[0].ID != composition.Clips[1].ID || ranked.Suggestions[0].Clips[0].ID != composition.Clips[1].ID {
		t.Fatalf("ranking altered grounded clips: %#v", rankedComposition)
	}
}

func TestNarrativeIntentPropagatesWithoutChangingGroundedClips(t *testing.T) {
	dependencies := narrativeDependencies()
	plan, composition, _, err := buildMultiVideoComposition("composition-1", []string{"asset-a", "asset-b"}, dependencies)
	if err != nil {
		t.Fatalf("build composition: %v", err)
	}
	composition.NarrativeIntent = "action-forward"
	composition.PacingProfile = videoindexerstudio.NarrativePacingProfileEnergeticShortForm
	_, ranked, _, err := rankMultiVideoComposition(context.Background(), rankingClientFunc(func(_ context.Context, request videoindexerstudio.NarrativeRankingRequest) (*videoindexerstudio.NarrativeRankingResponse, error) {
		if request.NarrativeIntent != "action-forward" {
			t.Fatalf("narrative intent = %q", request.NarrativeIntent)
		}
		return &videoindexerstudio.NarrativeRankingResponse{SchemaVersion: 1, OrderedClips: []videoindexerstudio.NarrativeRankedClip{{CandidateID: request.Candidates[1].ID, EvidenceIDs: []string{request.Candidates[1].EvidenceIDs[0]}}, {CandidateID: request.Candidates[0].ID, EvidenceIDs: []string{request.Candidates[0].EvidenceIDs[0]}}}}, nil
	}), plan, composition, dependencies)
	if err != nil || ranked.NarrativeIntent != composition.NarrativeIntent || len(ranked.Clips) != len(composition.Clips) {
		t.Fatalf("ranked composition = %#v, %v", ranked, err)
	}
}

func TestRankMultiVideoCompositionPermutesFilteredCandidates(t *testing.T) {
	dependencies := narrativeDependencies()
	dependencies[0].EditPlan.Suggestions = []videoindexerstudio.EditSuggestion{
		{ID: "first", Score: 0.9, Clips: []videoindexerstudio.SuggestedClip{{ID: "first", StartMs: 0, EndMs: 100, Score: 0.9}}},
		{ID: "duplicate", Score: 0.8, Clips: []videoindexerstudio.SuggestedClip{{ID: "duplicate", StartMs: 0, EndMs: 100, Score: 0.8}}},
		{ID: "adjacent", Score: 0.7, Clips: []videoindexerstudio.SuggestedClip{{ID: "adjacent", StartMs: 100, EndMs: 200, Score: 0.7}}},
	}
	plan, composition, _, err := buildMultiVideoComposition("composition-1", []string{"asset-a", "asset-b"}, dependencies)
	if err != nil {
		t.Fatalf("build filtered composition: %v", err)
	}
	if len(composition.Clips) != 3 {
		t.Fatalf("filtered candidate count = %d, want 3", len(composition.Clips))
	}
	ranked, rankedComposition, _, err := rankMultiVideoComposition(context.Background(), rankingClientFunc(func(_ context.Context, request videoindexerstudio.NarrativeRankingRequest) (*videoindexerstudio.NarrativeRankingResponse, error) {
		if len(request.Candidates) != len(composition.Clips) {
			t.Fatalf("Azure request candidates = %d, want %d filtered candidates", len(request.Candidates), len(composition.Clips))
		}
		ordered := make([]videoindexerstudio.NarrativeRankedClip, 0, len(request.Candidates))
		for i := len(request.Candidates) - 1; i >= 0; i-- {
			candidate := request.Candidates[i]
			ordered = append(ordered, videoindexerstudio.NarrativeRankedClip{CandidateID: candidate.ID, EvidenceIDs: []string{candidate.EvidenceIDs[0]}})
		}
		return &videoindexerstudio.NarrativeRankingResponse{SchemaVersion: 1, OrderedClips: ordered}, nil
	}), plan, composition, dependencies)
	if err != nil {
		t.Fatalf("rank filtered composition: %v", err)
	}
	if len(rankedComposition.Clips) != len(composition.Clips) || len(ranked.Suggestions[0].Clips) != len(composition.Clips) {
		t.Fatalf("Azure ranking did not preserve a permutation of filtered candidates: %#v", rankedComposition.Clips)
	}
}
func TestRankMultiVideoCompositionRejectsInvalidOrder(t *testing.T) {
	dependencies := narrativeDependencies()
	plan, composition, _, err := buildMultiVideoComposition("composition-1", []string{"asset-a", "asset-b"}, dependencies)
	if err != nil {
		t.Fatalf("build composition: %v", err)
	}
	_, _, _, err = rankMultiVideoComposition(context.Background(), rankingClientFunc(func(_ context.Context, _ videoindexerstudio.NarrativeRankingRequest) (*videoindexerstudio.NarrativeRankingResponse, error) {
		return nil, errors.New("unavailable")
	}), plan, composition, dependencies)
	if err == nil {
		t.Fatal("expected ranking error for caller fallback")
	}
}

func TestRankMultiVideoCompositionRejectsMissingCitation(t *testing.T) {
	dependencies := narrativeDependencies()
	plan, composition, _, err := buildMultiVideoComposition("composition-1", []string{"asset-a", "asset-b"}, dependencies)
	if err != nil {
		t.Fatalf("build composition: %v", err)
	}
	_, _, _, err = rankMultiVideoComposition(context.Background(), rankingClientFunc(func(_ context.Context, request videoindexerstudio.NarrativeRankingRequest) (*videoindexerstudio.NarrativeRankingResponse, error) {
		return &videoindexerstudio.NarrativeRankingResponse{SchemaVersion: 1, OrderedClips: []videoindexerstudio.NarrativeRankedClip{{CandidateID: request.Candidates[0].ID}, {CandidateID: request.Candidates[1].ID, EvidenceIDs: []string{request.Candidates[1].EvidenceIDs[0]}}}}, nil
	}), plan, composition, dependencies)
	if err == nil {
		t.Fatal("expected missing citation rejection")
	}
}

func TestBuildNarrativeRankingRequestRetainsEvidenceForEveryCandidateWithinBudget(t *testing.T) {
	firstResult := &videoindexerstudio.VideoIndexResult{}
	for i := 0; i < narrativeMaxEvidence; i++ {
		firstResult.Insights.Scenes = append(firstResult.Insights.Scenes, videoindexerstudio.VideoIndexScene{ID: fmt.Sprintf("scene-%03d", i), StartMs: int64(i * 10), EndMs: int64(i*10 + 5)})
	}
	lateResult := &videoindexerstudio.VideoIndexResult{Insights: videoindexerstudio.VideoIndexInsights{Scenes: []videoindexerstudio.VideoIndexScene{{ID: "late-scene", StartMs: 0, EndMs: 5}}}}
	composition := videoindexerstudio.CompositionEditPlan{
		CompositionID:   "composition-1",
		NarrativeIntent: "chronological",
		SourceAssetIDs:  []string{"asset-a", "asset-z"},
		Clips: []videoindexerstudio.CompositionClip{
			{ID: "candidate-a", SourceAssetID: "asset-a", StartMs: 0, EndMs: 5},
			{ID: "candidate-z", SourceAssetID: "asset-z", StartMs: 0, EndMs: 5},
		},
	}
	request, err := buildNarrativeRankingRequest(composition, []VideoIndexerStudioJob{
		{AssetID: "asset-a", VideoIndexResult: firstResult},
		{AssetID: "asset-z", VideoIndexResult: lateResult},
	})
	if err != nil {
		t.Fatalf("build narrative ranking request: %v", err)
	}
	if request.NarrativeIntent != composition.NarrativeIntent {
		t.Fatalf("narrative intent = %q", request.NarrativeIntent)
	}
	if len(request.Evidence) != 2 {
		t.Fatalf("evidence count = %d, want %d", len(request.Evidence), 2)
	}
	for _, candidate := range request.Candidates {
		if len(candidate.EvidenceIDs) == 0 {
			t.Fatalf("candidate %q has no evidence after bounded selection", candidate.ID)
		}
	}
	if got := request.Candidates[1].EvidenceIDs; len(got) != 1 || got[0] != "asset-z:scene:late-scene" {
		t.Fatalf("late candidate evidence = %#v", got)
	}
}

func TestSelectNarrativeEvidenceRejectsBudgetWithoutCandidateCoverage(t *testing.T) {
	candidates := make([]videoindexerstudio.NarrativeRankingCandidate, narrativeMaxEvidence+1)
	evidence := make([]videoindexerstudio.NarrativeEvidence, narrativeMaxEvidence+1)
	for i := range candidates {
		assetID := fmt.Sprintf("asset-%03d", i)
		candidates[i] = videoindexerstudio.NarrativeRankingCandidate{ID: assetID, SourceAssetID: assetID, StartMs: 0, EndMs: 1}
		evidence[i] = videoindexerstudio.NarrativeEvidence{ID: assetID + ":scene", SourceAssetID: assetID, Kind: "scene", StartMs: 0, EndMs: 1}
	}
	if _, err := selectNarrativeEvidence(candidates, evidence); err == nil || !strings.Contains(err.Error(), "budget cannot cover") {
		t.Fatalf("expected explicit evidence budget error, got %v", err)
	}
}

func TestTruncateNarrativeTextPreservesUTF8(t *testing.T) {
	text := strings.Repeat("é", narrativeTextLimit+1)
	truncated := truncateNarrativeText(text)
	if !utf8.ValidString(truncated) || utf8.RuneCountInString(truncated) != narrativeTextLimit {
		t.Fatalf("invalid rune-safe truncation: %q", truncated)
	}
}
func TestResolveNarrativePacingUsesFoundryProfileAndSafeFallbacks(t *testing.T) {
	validResponse := func(profile videoindexerstudio.NarrativeIntentProfile) *videoindexerstudio.NarrativeIntentClassificationResponse {
		return &videoindexerstudio.NarrativeIntentClassificationResponse{SchemaVersion: videoindexerstudio.NarrativeRankingSchemaVersion, Profile: profile}
	}
	tests := map[string]struct {
		intent       string
		response     *videoindexerstudio.NarrativeIntentClassificationResponse
		err          error
		wantProfile  videoindexerstudio.NarrativePacingProfile
		wantMode     videoindexerstudio.NarrativePacingClassifierMode
		wantFallback videoindexerstudio.NarrativePacingClassifierFallbackReason
	}{
		"multilingual Foundry energetic":        {intent: "dynamic tiktok video", response: validResponse(videoindexerstudio.NarrativeIntentProfileEnergetic), wantProfile: videoindexerstudio.NarrativePacingProfileEnergeticShortForm, wantMode: videoindexerstudio.NarrativePacingClassifierModeFoundryStructured},
		"French Foundry calm":                   {intent: "recapitulatif calme", response: validResponse(videoindexerstudio.NarrativeIntentProfileCalm), wantProfile: videoindexerstudio.NarrativePacingProfileCalmRecap, wantMode: videoindexerstudio.NarrativePacingClassifierModeFoundryStructured},
		"timeout uses keyword fallback":         {intent: "energetic", err: context.DeadlineExceeded, wantProfile: videoindexerstudio.NarrativePacingProfileEnergeticShortForm, wantMode: videoindexerstudio.NarrativePacingClassifierModeDeterministicKeywordFallback, wantFallback: videoindexerstudio.NarrativePacingClassifierFallbackTimeout},
		"invalid result uses standard fallback": {intent: "recapitulatif calme", response: &videoindexerstudio.NarrativeIntentClassificationResponse{SchemaVersion: 1, Profile: "untrusted"}, wantProfile: videoindexerstudio.NarrativePacingProfileStandard, wantMode: videoindexerstudio.NarrativePacingClassifierModeStandardFallback, wantFallback: videoindexerstudio.NarrativePacingClassifierFallbackInvalidResponse},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			client := classifierAndRankingClient{
				classifier: func(_ context.Context, req videoindexerstudio.NarrativeIntentClassificationRequest) (*videoindexerstudio.NarrativeIntentClassificationResponse, error) {
					if req.NarrativeIntent != test.intent || req.SchemaVersion != videoindexerstudio.NarrativeRankingSchemaVersion {
						t.Fatalf("unexpected classification request: %#v", req)
					}
					return test.response, test.err
				},
				ranker: func(context.Context, videoindexerstudio.NarrativeRankingRequest) (*videoindexerstudio.NarrativeRankingResponse, error) {
					return nil, errors.New("not used")
				},
			}
			svc := NewVideoIndexerStudioService(nil, nil, nil, client)
			got := svc.resolveNarrativePacing(context.Background(), test.intent)
			if got.profile != test.wantProfile || got.mode != test.wantMode || got.fallbackReason != test.wantFallback {
				t.Fatalf("resolution = %#v", got)
			}
		})
	}
}

func TestBuildNarrativeRankingRequestExcludesLocalPacingMetadata(t *testing.T) {
	dependencies := narrativeDependencies()
	_, composition, _, err := buildMultiVideoCompositionWithPacing("composition-1", []string{"asset-a", "asset-b"}, dependencies, "recapitulatif calme", narrativePacingResolution{profile: videoindexerstudio.NarrativePacingProfileCalmRecap, mode: videoindexerstudio.NarrativePacingClassifierModeFoundryStructured})
	if err != nil {
		t.Fatalf("build composition: %v", err)
	}
	request, err := buildNarrativeRankingRequest(composition, dependencies)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if request.PacingProfile != "" || request.VariantCount != 0 || request.NarrativeIntent != "recapitulatif calme" {
		t.Fatalf("ranking packet leaked local pacing metadata: %#v", request)
	}
}

func TestCompositionPersistsFoundryClassificationWhenRankingFallsBack(t *testing.T) {
	assets := []library.ProjectAsset{{ID: "asset-a", Name: "a.mp4", CloudAssetID: "drive-a"}, {ID: "asset-b", Name: "b.mp4", CloudAssetID: "drive-b"}}
	store := &memoryVideoIndexerJobStore{jobs: narrativeDependencies()}
	client := classifierAndRankingClient{
		classifier: func(_ context.Context, req videoindexerstudio.NarrativeIntentClassificationRequest) (*videoindexerstudio.NarrativeIntentClassificationResponse, error) {
			if req.NarrativeIntent != "recapitulatif calme" {
				t.Fatalf("classifier intent = %q", req.NarrativeIntent)
			}
			return &videoindexerstudio.NarrativeIntentClassificationResponse{SchemaVersion: videoindexerstudio.NarrativeRankingSchemaVersion, Profile: videoindexerstudio.NarrativeIntentProfileCalm}, nil
		},
		ranker: func(context.Context, videoindexerstudio.NarrativeRankingRequest) (*videoindexerstudio.NarrativeRankingResponse, error) {
			return nil, errors.New("ranking unavailable")
		},
	}
	svc := NewVideoIndexerStudioService(&fakeLibraryStore{assets: assets}, nil, &fakeEditingSaver{}, client, store)
	composition, err := svc.GenerateMultiVideoEditWithIntent(context.Background(), []string{"asset-a", "asset-b"}, "recapitulatif calme")
	if err != nil {
		t.Fatalf("GenerateMultiVideoEditWithIntent: %v", err)
	}
	if composition.CompositionPlan == nil {
		t.Fatal("composition plan is missing")
	}
	plan := composition.CompositionPlan
	if plan.PacingProfile != videoindexerstudio.NarrativePacingProfileCalmRecap || plan.PacingClassifierMode != videoindexerstudio.NarrativePacingClassifierModeFoundryStructured || plan.PacingFallbackReason != "" {
		t.Fatalf("persisted classifier metadata = %#v", plan)
	}
	if plan.RankingMode != "deterministic_grounded_fallback_v1" || len(plan.Clips) != 2 {
		t.Fatalf("ranking fallback did not preserve locally selected candidates: %#v", plan)
	}
	for _, clip := range plan.Clips {
		if clip.SourceAssetID != "asset-a" && clip.SourceAssetID != "asset-b" || clip.StartMs < 0 || clip.EndMs <= clip.StartMs {
			t.Fatalf("fallback changed grounded clip: %#v", clip)
		}
	}
}

func TestSelectNarrativeEvidenceDistributesBudgetDeterministically(t *testing.T) {
	candidates := []videoindexerstudio.NarrativeRankingCandidate{{ID: "candidate-a", SourceAssetID: "asset-a", StartMs: 0, EndMs: 10_000}, {ID: "candidate-b", SourceAssetID: "asset-b", StartMs: 0, EndMs: 10_000}}
	evidence := make([]videoindexerstudio.NarrativeEvidence, 0, narrativeMaxEvidence*2)
	for _, assetID := range []string{"asset-a", "asset-b"} {
		for i := 0; i < narrativeMaxEvidence; i++ {
			evidence = append(evidence, videoindexerstudio.NarrativeEvidence{ID: fmt.Sprintf("%s:scene:%03d", assetID, i), SourceAssetID: assetID, Kind: "scene", StartMs: int64(i * 10), EndMs: int64(i*10 + 5)})
		}
	}
	first, err := selectNarrativeEvidence(candidates, evidence)
	if err != nil {
		t.Fatalf("select evidence: %v", err)
	}
	second, err := selectNarrativeEvidence(candidates, evidence)
	if err != nil || len(first) != narrativeMaxEvidence || len(second) != len(first) {
		t.Fatalf("bounded selection = %d, %d, %v", len(first), len(second), err)
	}
	counts := map[string]int{}
	for i := range first {
		counts[first[i].SourceAssetID]++
		if first[i] != second[i] {
			t.Fatalf("selection is not deterministic: %#v != %#v", first, second)
		}
	}
	if counts["asset-a"] != narrativeMaxEvidence/2 || counts["asset-b"] != narrativeMaxEvidence/2 {
		t.Fatalf("budget is not fairly distributed: %#v", counts)
	}
}
