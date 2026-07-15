package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go.opentelemetry.io/otel/attribute"
	"time"

	"github.com/zecloud/ai-video-studio/internal/videoindexerstudio"
)

const narrativeRankerInstructionsVersion = "v1"

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
		return videoindexerstudio.NarrativeRankingResponse{}, errors.New("narrative ranker is not configured")
	}
	if err := validateNarrativeRankingRequest(req, r.max, r.maxSources); err != nil {
		return videoindexerstudio.NarrativeRankingResponse{}, err
	}
	if r.timeout <= 0 {
		r.timeout = 20 * time.Second
	}
	rankCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	raw, err := json.Marshal(req)
	if err != nil {
		return videoindexerstudio.NarrativeRankingResponse{}, err
	}
	start := time.Now()
	plan, err := r.planner.Plan(rankCtx, narrativeRankingPrompt(string(raw)))
	if r.obs != nil {
		r.obs.FinishSpan(ctx, nil, "narrative.rank", start, []attribute.KeyValue{attribute.Int("candidate_count", len(req.Candidates)), attribute.Int("evidence_count", len(req.Evidence))}, err)
	}
	if err != nil {
		return videoindexerstudio.NarrativeRankingResponse{}, err
	}
	response := videoindexerstudio.NarrativeRankingResponse{SchemaVersion: videoindexerstudio.NarrativeRankingSchemaVersion, OrderedClips: make([]videoindexerstudio.NarrativeRankedClip, 0, len(plan.Suggestions))}
	for _, suggestion := range plan.Suggestions {
		response.OrderedClips = append(response.OrderedClips, videoindexerstudio.NarrativeRankedClip{CandidateID: suggestion.ID})
	}
	if err := validateNarrativeRankingResponse(req, response); err != nil {
		return videoindexerstudio.NarrativeRankingResponse{}, err
	}
	return response, nil
}

func narrativeRankingPrompt(packet string) string {
	return fmt.Sprintf("narrative-ranker instructions %s\nOrder every candidate exactly once. Use only candidate IDs from this JSON. Do not create, remove, alter, or duplicate IDs. Return the ordered IDs as EditPlan.suggestions[].id and no unsupported fields.\n%s", narrativeRankerInstructionsVersion, packet)
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
		seen[ranked.CandidateID] = struct{}{}
		for _, evidenceID := range ranked.EvidenceIDs {
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
