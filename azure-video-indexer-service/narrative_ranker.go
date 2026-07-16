package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/zecloud/ai-video-studio/internal/videoindexerstudio"
	"go.opentelemetry.io/otel/attribute"
)

const (
	narrativeRankerInstructionsVersion = "v2"
	narrativeRankerAttempts            = 2
)

type NarrativeRanker interface {
	Rank(context.Context, videoindexerstudio.NarrativeRankingRequest) (videoindexerstudio.NarrativeRankingResponse, error)
}
type narrativeRankerFunc func(context.Context, videoindexerstudio.NarrativeRankingRequest) (videoindexerstudio.NarrativeRankingResponse, error)

func (f narrativeRankerFunc) Rank(ctx context.Context, req videoindexerstudio.NarrativeRankingRequest) (videoindexerstudio.NarrativeRankingResponse, error) {
	return f(ctx, req)
}

type narrativeRanker struct {
	planner    EditPlanner
	max        int
	maxSources int
	timeout    time.Duration
	obs        *Observability
}

func (r narrativeRanker) Rank(ctx context.Context, req videoindexerstudio.NarrativeRankingRequest) (videoindexerstudio.NarrativeRankingResponse, error) {
	if r.planner == nil {
		return videoindexerstudio.NarrativeRankingResponse{}, narrativeFailureError(narrativeFailureUnavailable, errors.New("ranker not configured"))
	}
	if err := validateNarrativeRankingRequest(req, r.max, r.maxSources); err != nil {
		return videoindexerstudio.NarrativeRankingResponse{}, narrativeFailureError(narrativeFailureInvalidReq, err)
	}
	if r.timeout <= 0 {
		r.timeout = time.Minute
	}
	raw, err := json.Marshal(req)
	if err != nil {
		return videoindexerstudio.NarrativeRankingResponse{}, narrativeFailureError(narrativeFailureInvalidReq, err)
	}
	rankCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	start := time.Now()
	attempts := 0
	var plan EditPlan
	for attempts = 1; attempts <= narrativeRankerAttempts; attempts++ {
		plan, err = r.planner.Plan(rankCtx, narrativeRankingPrompt(string(raw)))
		if err != nil {
			err = classifyNarrativeProviderError(err)
		}
		if err == nil || !isRetryableNarrativeFailure(err) || rankCtx.Err() != nil || attempts == narrativeRankerAttempts {
			break
		}
		if r.obs != nil {
			r.obs.RecordRetry(ctx, "narrative.rank", 0, attribute.String("failure_kind", string(narrativeFailureFor(err))))
		}
		select {
		case <-rankCtx.Done():
		case <-time.After(100 * time.Millisecond):
		}
	}
	if rankCtx.Err() != nil && err != nil {
		err = narrativeFailureError(narrativeFailureTimeout, rankCtx.Err())
	}
	if r.obs != nil {
		r.obs.FinishSpan(ctx, nil, "narrative.rank", start, []attribute.KeyValue{attribute.String("prompt_version", narrativeRankerInstructionsVersion), attribute.Int("candidate_count", len(req.Candidates)), attribute.Int("evidence_count", len(req.Evidence)), attribute.Int("packet_bytes", len(raw)), attribute.Int("attempt_count", attempts), attribute.String("failure_kind", string(narrativeFailureFor(err))), attribute.Bool("narrative_intent_present", req.NarrativeIntent != ""), attribute.Int("narrative_intent_length", len([]rune(req.NarrativeIntent)))}, err)
	}
	if err != nil {
		return videoindexerstudio.NarrativeRankingResponse{}, err
	}
	response := videoindexerstudio.NarrativeRankingResponse{SchemaVersion: videoindexerstudio.NarrativeRankingSchemaVersion, OrderedClips: make([]videoindexerstudio.NarrativeRankedClip, 0, len(plan.Suggestions))}
	for _, suggestion := range plan.Suggestions {
		ids := make([]string, 0, len(suggestion.SourceRefs))
		for _, ref := range suggestion.SourceRefs {
			ids = append(ids, ref.RefID)
		}
		response.OrderedClips = append(response.OrderedClips, videoindexerstudio.NarrativeRankedClip{CandidateID: suggestion.ID, EvidenceIDs: ids})
	}
	if err := validateNarrativeRankingResponse(req, response); err != nil {
		return videoindexerstudio.NarrativeRankingResponse{}, narrativeFailureError(narrativeFailureInvalid, err)
	}
	return response, nil
}
func narrativeRankingPrompt(packet string) string {
	return fmt.Sprintf("narrative-ranker instructions %s\nThe optional narrativeIntent is an editorial ordering preference only. Local candidate selection is already complete. It must never justify adding, removing, altering, or duplicating candidates, sources, ranges, evidence, or timeline invariants. Order every candidate exactly once. Use only candidate IDs from this JSON. Do not create, remove, alter, or duplicate IDs. Return every candidate as EditPlan.suggestions[].id with at least one matching EvidenceID in suggestions[].sourceRefs[].refId. Do not use unsupported fields.\n%s", narrativeRankerInstructionsVersion, packet)
}
func validateNarrativeRankingRequest(req videoindexerstudio.NarrativeRankingRequest, maxCandidates, maxSources int) error {
	if err := req.Validate(); err != nil {
		return err
	}
	if maxCandidates > 0 && len(req.Candidates) > maxCandidates {
		return errors.New("narrative ranking candidate limit exceeded")
	}
	sources := map[string]struct{}{}
	for _, candidate := range req.Candidates {
		if len(candidate.EvidenceIDs) == 0 {
			return errors.New("narrative ranking candidates must include grounded evidence")
		}
		sources[candidate.SourceAssetID] = struct{}{}
	}
	if maxSources > 0 && len(sources) > maxSources {
		return errors.New("narrative ranking source limit exceeded")
	}
	return nil
}
func validateNarrativeRankingResponse(req videoindexerstudio.NarrativeRankingRequest, response videoindexerstudio.NarrativeRankingResponse) error {
	if response.SchemaVersion != videoindexerstudio.NarrativeRankingSchemaVersion || len(response.OrderedClips) != len(req.Candidates) {
		return errors.New("narrative ranking response must order every candidate")
	}
	known := make(map[string]struct{}, len(req.Candidates))
	for _, candidate := range req.Candidates {
		known[candidate.ID] = struct{}{}
	}
	seen := make(map[string]struct{}, len(response.OrderedClips))
	for _, ranked := range response.OrderedClips {
		if _, ok := known[ranked.CandidateID]; !ok {
			return errors.New("narrative ranking response references an unknown candidate")
		}
		if _, exists := seen[ranked.CandidateID]; exists {
			return errors.New("narrative ranking response contains duplicate candidates")
		}
		if len(ranked.EvidenceIDs) == 0 {
			return errors.New("narrative ranking response must cite candidate evidence")
		}
		seen[ranked.CandidateID] = struct{}{}
		rankedEvidence := make(map[string]struct{}, len(ranked.EvidenceIDs))
		for _, evidenceID := range ranked.EvidenceIDs {
			if _, duplicate := rankedEvidence[evidenceID]; duplicate {
				return errors.New("narrative ranking response contains duplicate evidence")
			}
			rankedEvidence[evidenceID] = struct{}{}
			if !candidateHasEvidence(req, ranked.CandidateID, evidenceID) {
				return errors.New("narrative ranking response references ungrounded evidence")
			}
		}
	}
	return nil
}
func candidateHasEvidence(req videoindexerstudio.NarrativeRankingRequest, candidateID, evidenceID string) bool {
	for _, candidate := range req.Candidates {
		if candidate.ID == candidateID {
			for _, id := range candidate.EvidenceIDs {
				if id == evidenceID {
					return true
				}
			}
		}
	}
	return false
}
