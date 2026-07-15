package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/zecloud/ai-video-studio/internal/videoindexerstudio"
)

type narrativePlannerFunc func(context.Context, string) (EditPlan, error)

func (f narrativePlannerFunc) Plan(ctx context.Context, prompt string) (EditPlan, error) {
	return f(ctx, prompt)
}

func narrativeRequest() videoindexerstudio.NarrativeRankingRequest {
	return videoindexerstudio.NarrativeRankingRequest{SchemaVersion: 1, CompositionID: "composition-1", Candidates: []videoindexerstudio.NarrativeRankingCandidate{{ID: "clip-a", SourceAssetID: "asset-a", StartMs: 0, EndMs: 100, EvidenceIDs: []string{"asset-a:scene:scene-a"}}, {ID: "clip-b", SourceAssetID: "asset-b", StartMs: 0, EndMs: 100}}, Evidence: []videoindexerstudio.NarrativeEvidence{{ID: "asset-a:scene:scene-a", SourceAssetID: "asset-a", Kind: "scene", StartMs: 0, EndMs: 100}}}
}

func TestNarrativeRankerAcceptsOnlyCompleteGroundedOrder(t *testing.T) {
	ranker := narrativeRanker{max: 2, timeout: time.Second, planner: narrativePlannerFunc(func(_ context.Context, prompt string) (EditPlan, error) {
		if !strings.Contains(prompt, "clip-a") || !strings.Contains(prompt, "Order every candidate exactly once") {
			t.Fatalf("prompt omitted strict contract: %s", prompt)
		}
		return EditPlan{Suggestions: []EditSuggestion{{ID: "clip-b"}, {ID: "clip-a"}}}, nil
	})}
	response, err := ranker.Rank(context.Background(), narrativeRequest())
	if err != nil {
		t.Fatalf("Rank: %v", err)
	}
	if response.OrderedClips[0].CandidateID != "clip-b" {
		t.Fatalf("unexpected response: %#v", response)
	}
}

func TestNarrativeRankerRejectsUnknownAndTimeout(t *testing.T) {
	t.Run("unknown id", func(t *testing.T) {
		ranker := narrativeRanker{planner: narrativePlannerFunc(func(context.Context, string) (EditPlan, error) {
			return EditPlan{Suggestions: []EditSuggestion{{ID: "clip-a"}, {ID: "invented"}}}, nil
		})}
		if _, err := ranker.Rank(context.Background(), narrativeRequest()); err == nil {
			t.Fatal("expected unknown candidate rejection")
		}
	})
	t.Run("timeout", func(t *testing.T) {
		ranker := narrativeRanker{timeout: time.Millisecond, planner: narrativePlannerFunc(func(ctx context.Context, _ string) (EditPlan, error) {
			<-ctx.Done()
			return EditPlan{}, ctx.Err()
		})}
		_, err := ranker.Rank(context.Background(), narrativeRequest())
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("expected deadline exceeded, got %v", err)
		}
	})
}
