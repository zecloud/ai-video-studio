package backend

import (
	"context"
	"errors"
	"testing"

	"github.com/zecloud/ai-video-studio/internal/videoindexerstudio"
)

type rankingClientFunc func(context.Context, videoindexerstudio.NarrativeRankingRequest) (*videoindexerstudio.NarrativeRankingResponse, error)

func (f rankingClientFunc) RankNarrative(ctx context.Context, request videoindexerstudio.NarrativeRankingRequest) (*videoindexerstudio.NarrativeRankingResponse, error) {
	return f(ctx, request)
}

func TestRankMultiVideoCompositionPreservesKnownClips(t *testing.T) {
	dependencies := []VideoIndexerStudioJob{completedAnalysisJob("analysis-a", "asset-a", 0, 1000, .8), completedAnalysisJob("analysis-b", "asset-b", 100, 1100, .9)}
	plan, composition, _, err := buildMultiVideoComposition("composition-1", []string{"asset-a", "asset-b"}, dependencies)
	if err != nil {
		t.Fatalf("build composition: %v", err)
	}
	ranked, rankedComposition, drafts, err := rankMultiVideoComposition(context.Background(), rankingClientFunc(func(_ context.Context, request videoindexerstudio.NarrativeRankingRequest) (*videoindexerstudio.NarrativeRankingResponse, error) {
		return &videoindexerstudio.NarrativeRankingResponse{SchemaVersion: 1, OrderedClips: []videoindexerstudio.NarrativeRankedClip{{CandidateID: request.Candidates[1].ID}, {CandidateID: request.Candidates[0].ID}}}, nil
	}), plan, composition, dependencies)
	if err != nil {
		t.Fatalf("rank composition: %v", err)
	}
	if rankedComposition.RankingMode != "azure_openai_grounded_v1" || rankedComposition.Clips[0].ID != composition.Clips[1].ID || drafts[0].PrimaryVideoTrack.Clips[0].ID != composition.Clips[1].ID || ranked.Suggestions[0].Clips[0].ID != composition.Clips[1].ID {
		t.Fatalf("ranking altered grounded clips: %#v", rankedComposition)
	}
}

func TestRankMultiVideoCompositionRejectsInvalidOrder(t *testing.T) {
	dependencies := []VideoIndexerStudioJob{completedAnalysisJob("analysis-a", "asset-a", 0, 1000, .8), completedAnalysisJob("analysis-b", "asset-b", 100, 1100, .9)}
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
