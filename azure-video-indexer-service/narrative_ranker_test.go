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
	return videoindexerstudio.NarrativeRankingRequest{SchemaVersion: 1, CompositionID: "composition-1", NarrativeIntent: "action-forward", Candidates: []videoindexerstudio.NarrativeRankingCandidate{{ID: "clip-a", SourceAssetID: "asset-a", StartMs: 0, EndMs: 100, EvidenceIDs: []string{"asset-a:scene:scene-a"}}, {ID: "clip-b", SourceAssetID: "asset-b", StartMs: 0, EndMs: 100, EvidenceIDs: []string{"asset-b:scene:scene-b"}}}, Evidence: []videoindexerstudio.NarrativeEvidence{{ID: "asset-a:scene:scene-a", SourceAssetID: "asset-a", Kind: "scene", StartMs: 0, EndMs: 100}, {ID: "asset-b:scene:scene-b", SourceAssetID: "asset-b", Kind: "scene", StartMs: 0, EndMs: 100}}}
}

func TestNarrativeRankerAcceptsOnlyCompleteGroundedOrder(t *testing.T) {
	ranker := narrativeRanker{max: 2, timeout: time.Second, planner: narrativePlannerFunc(func(_ context.Context, prompt string) (EditPlan, error) {
		if !strings.Contains(prompt, "clip-a") || !strings.Contains(prompt, `"narrativeIntent":"action-forward"`) || !strings.Contains(prompt, "Local candidate selection is already complete") || !strings.Contains(prompt, "Order every candidate exactly once") {
			t.Fatalf("prompt omitted strict contract: %s", prompt)
		}
		return EditPlan{Suggestions: []EditSuggestion{{ID: "clip-b", SourceRefs: []SourceRef{{RefID: "asset-b:scene:scene-b"}}}, {ID: "clip-a", SourceRefs: []SourceRef{{RefID: "asset-a:scene:scene-a"}}}}}, nil
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
			return EditPlan{Suggestions: []EditSuggestion{{ID: "clip-a", SourceRefs: []SourceRef{{RefID: "asset-a:scene:scene-a"}}}, {ID: "invented", SourceRefs: []SourceRef{{RefID: "asset-b:scene:scene-b"}}}}}, nil
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

func TestNarrativeRankerRejectsMissingOrUngroundedCitations(t *testing.T) {
	for name, suggestions := range map[string][]EditSuggestion{
		"missing":    {{ID: "clip-a"}, {ID: "clip-b", SourceRefs: []SourceRef{{RefID: "asset-b:scene:scene-b"}}}},
		"ungrounded": {{ID: "clip-a", SourceRefs: []SourceRef{{RefID: "asset-b:scene:scene-b"}}}, {ID: "clip-b", SourceRefs: []SourceRef{{RefID: "asset-b:scene:scene-b"}}}},
	} {
		t.Run(name, func(t *testing.T) {
			ranker := narrativeRanker{planner: narrativePlannerFunc(func(context.Context, string) (EditPlan, error) {
				return EditPlan{Suggestions: suggestions}, nil
			})}
			if _, err := ranker.Rank(context.Background(), narrativeRequest()); err == nil {
				t.Fatal("expected citation validation error")
			}
		})
	}
}

func TestNarrativeRankerRetriesTransientAndDoesNotRetryInvalidPlan(t *testing.T) {
	attempts := 0
	ranker := narrativeRanker{timeout: time.Second, planner: narrativePlannerFunc(func(context.Context, string) (EditPlan, error) {
		attempts++
		if attempts == 1 {
			return EditPlan{}, errors.New("temporary upstream failure")
		}
		return EditPlan{Suggestions: []EditSuggestion{{ID: "clip-a", SourceRefs: []SourceRef{{RefID: "asset-a:scene:scene-a"}}}, {ID: "clip-b", SourceRefs: []SourceRef{{RefID: "asset-b:scene:scene-b"}}}}}, nil
	})}
	if _, err := ranker.Rank(context.Background(), narrativeRequest()); err != nil || attempts != 2 {
		t.Fatalf("transient retry = %v, attempts = %d", err, attempts)
	}
	attempts = 0
	ranker.planner = narrativePlannerFunc(func(context.Context, string) (EditPlan, error) {
		attempts++
		return EditPlan{Suggestions: []EditSuggestion{{ID: "clip-a", SourceRefs: []SourceRef{{RefID: "asset-a:scene:scene-a"}}}, {ID: "invented", SourceRefs: []SourceRef{{RefID: "asset-b:scene:scene-b"}}}}}, nil
	})
	if _, err := ranker.Rank(context.Background(), narrativeRequest()); narrativeFailureFor(err) != narrativeFailureInvalid || attempts != 1 {
		t.Fatalf("invalid response = %v, attempts = %d", err, attempts)
	}
}

func TestNarrativeRankingValidationReasonsRemainStrict(t *testing.T) {
	request := narrativeRequest()
	valid := videoindexerstudio.NarrativeRankingResponse{SchemaVersion: 1, OrderedClips: []videoindexerstudio.NarrativeRankedClip{{CandidateID: "clip-a", EvidenceIDs: []string{"asset-a:scene:scene-a"}}, {CandidateID: "clip-b", EvidenceIDs: []string{"asset-b:scene:scene-b"}}}}
	for name, testCase := range map[string]struct {
		response videoindexerstudio.NarrativeRankingResponse
		reason   string
	}{
		"invalid schema":          {response: videoindexerstudio.NarrativeRankingResponse{SchemaVersion: 2, OrderedClips: valid.OrderedClips}, reason: "invalid_schema_version"},
		"missing candidate count": {response: videoindexerstudio.NarrativeRankingResponse{SchemaVersion: 1, OrderedClips: valid.OrderedClips[:1]}, reason: "missing_candidate_count"},
		"unknown candidate":       {response: videoindexerstudio.NarrativeRankingResponse{SchemaVersion: 1, OrderedClips: []videoindexerstudio.NarrativeRankedClip{{CandidateID: "unknown", EvidenceIDs: []string{"asset-a:scene:scene-a"}}, valid.OrderedClips[1]}}, reason: "unknown_candidate"},
		"duplicate candidate":     {response: videoindexerstudio.NarrativeRankingResponse{SchemaVersion: 1, OrderedClips: []videoindexerstudio.NarrativeRankedClip{valid.OrderedClips[0], valid.OrderedClips[0]}}, reason: "duplicate_candidate"},
		"missing evidence":        {response: videoindexerstudio.NarrativeRankingResponse{SchemaVersion: 1, OrderedClips: []videoindexerstudio.NarrativeRankedClip{{CandidateID: "clip-a"}, valid.OrderedClips[1]}}, reason: "missing_evidence"},
		"duplicate evidence":      {response: videoindexerstudio.NarrativeRankingResponse{SchemaVersion: 1, OrderedClips: []videoindexerstudio.NarrativeRankedClip{{CandidateID: "clip-a", EvidenceIDs: []string{"asset-a:scene:scene-a", "asset-a:scene:scene-a"}}, valid.OrderedClips[1]}}, reason: "duplicate_evidence"},
		"ungrounded evidence":     {response: videoindexerstudio.NarrativeRankingResponse{SchemaVersion: 1, OrderedClips: []videoindexerstudio.NarrativeRankedClip{{CandidateID: "clip-a", EvidenceIDs: []string{"asset-b:scene:scene-b"}}, valid.OrderedClips[1]}}, reason: "ungrounded_evidence"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := narrativeRankingValidationReason(validateNarrativeRankingResponse(request, testCase.response)); got != testCase.reason {
				t.Fatalf("validation reason = %q, want %q", got, testCase.reason)
			}
		})
	}
	if err := validateNarrativeRankingResponse(request, valid); err != nil || narrativeRankingValidationReason(err) != "" {
		t.Fatalf("valid order = %v", err)
	}
}

func TestNarrativeRankerReportsInvalidProviderPlanAfterValidation(t *testing.T) {
	ranker := narrativeRanker{planner: narrativePlannerFunc(func(context.Context, string) (EditPlan, error) {
		return EditPlan{Suggestions: []EditSuggestion{{ID: "clip-a", SourceRefs: []SourceRef{{RefID: "asset-a:scene:scene-a"}}}}}, nil
	})}
	if _, err := ranker.Rank(context.Background(), narrativeRequest()); narrativeFailureFor(err) != narrativeFailureInvalid || narrativeRankingValidationReason(errors.Unwrap(err)) != "missing_candidate_count" {
		t.Fatalf("post-provider validation failure = %v", err)
	}
}

func TestNarrativeRankerRepairsOnlyIncompleteOrder(t *testing.T) {
	attempts := 0
	ranker := narrativeRanker{timeout: time.Second, planner: narrativePlannerFunc(func(_ context.Context, prompt string) (EditPlan, error) {
		attempts++
		if attempts == 1 {
			if strings.Contains(prompt, "repair instructions") {
				t.Fatal("initial prompt must not be a repair")
			}
			return EditPlan{Suggestions: []EditSuggestion{{ID: "clip-a", SourceRefs: []SourceRef{{RefID: "asset-a:scene:scene-a"}}}}}, nil
		}
		if !strings.Contains(prompt, "repair instructions") || !strings.Contains(prompt, "missing_candidate_count") || strings.Contains(prompt, "action-forward") || !strings.Contains(prompt, `"candidateId":"clip-a"`) || !strings.Contains(prompt, `"candidateId":"clip-b"`) {
			t.Fatalf("repair prompt must include only safe required identifiers: %s", prompt)
		}
		return EditPlan{Suggestions: []EditSuggestion{{ID: "clip-b", SourceRefs: []SourceRef{{RefID: "asset-b:scene:scene-b"}}}, {ID: "clip-a", SourceRefs: []SourceRef{{RefID: "asset-a:scene:scene-a"}}}}}, nil
	})}
	response, err := ranker.Rank(context.Background(), narrativeRequest())
	if err != nil || attempts != 2 || response.OrderedClips[0].CandidateID != "clip-b" {
		t.Fatalf("repaired response = %#v, %v, attempts = %d", response, err, attempts)
	}
}

func TestNarrativeRankerRejectsInvalidRepairAndDoesNotRepairOtherFailures(t *testing.T) {
	t.Run("invalid repair", func(t *testing.T) {
		attempts := 0
		ranker := narrativeRanker{timeout: time.Second, planner: narrativePlannerFunc(func(context.Context, string) (EditPlan, error) {
			attempts++
			return EditPlan{Suggestions: []EditSuggestion{{ID: "clip-a", SourceRefs: []SourceRef{{RefID: "asset-a:scene:scene-a"}}}}}, nil
		})}
		if _, err := ranker.Rank(context.Background(), narrativeRequest()); narrativeFailureFor(err) != narrativeFailureInvalid || narrativeRankingValidationReason(errors.Unwrap(err)) != "missing_candidate_count" || attempts != 2 {
			t.Fatalf("invalid repair = %v, attempts = %d", err, attempts)
		}
	})
	t.Run("provider failure", func(t *testing.T) {
		attempts := 0
		ranker := narrativeRanker{timeout: time.Second, planner: narrativePlannerFunc(func(context.Context, string) (EditPlan, error) {
			attempts++
			return EditPlan{}, errors.New("invalid structured output")
		})}
		if _, err := ranker.Rank(context.Background(), narrativeRequest()); narrativeFailureFor(err) != narrativeFailureInvalid || attempts != 1 {
			t.Fatalf("provider failure = %v, attempts = %d", err, attempts)
		}
	})
	t.Run("valid response", func(t *testing.T) {
		attempts := 0
		ranker := narrativeRanker{timeout: time.Second, planner: narrativePlannerFunc(func(context.Context, string) (EditPlan, error) {
			attempts++
			return EditPlan{Suggestions: []EditSuggestion{{ID: "clip-a", SourceRefs: []SourceRef{{RefID: "asset-a:scene:scene-a"}}}, {ID: "clip-b", SourceRefs: []SourceRef{{RefID: "asset-b:scene:scene-b"}}}}}, nil
		})}
		if _, err := ranker.Rank(context.Background(), narrativeRequest()); err != nil || attempts != 1 {
			t.Fatalf("valid response = %v, attempts = %d", err, attempts)
		}
	})
}

func TestNarrativeRankerDoesNotRepairOtherValidationFailures(t *testing.T) {
	attempts := 0
	ranker := narrativeRanker{timeout: time.Second, planner: narrativePlannerFunc(func(context.Context, string) (EditPlan, error) {
		attempts++
		return EditPlan{Suggestions: []EditSuggestion{
			{ID: "clip-a", SourceRefs: []SourceRef{{RefID: "asset-a:scene:scene-a"}}},
			{ID: "clip-a", SourceRefs: []SourceRef{{RefID: "asset-a:scene:scene-a"}}},
		}}, nil
	})}
	if _, err := ranker.Rank(context.Background(), narrativeRequest()); narrativeFailureFor(err) != narrativeFailureInvalid || attempts != 1 {
		t.Fatalf("non-repairable validation failure = %v, attempts = %d", err, attempts)
	}
}
