package backend

import (
	"context"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/zecloud/ai-video-studio/internal/videoindexerstudio"
)

type rankingClientFunc func(context.Context, videoindexerstudio.NarrativeRankingRequest) (*videoindexerstudio.NarrativeRankingResponse, error)

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

func TestTruncateNarrativeTextPreservesUTF8(t *testing.T) {
	text := strings.Repeat("é", narrativeTextLimit+1)
	truncated := truncateNarrativeText(text)
	if !utf8.ValidString(truncated) || utf8.RuneCountInString(truncated) != narrativeTextLimit {
		t.Fatalf("invalid rune-safe truncation: %q", truncated)
	}
}
