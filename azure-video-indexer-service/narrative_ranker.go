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
		r.obs.FinishSpan(ctx, nil, "narrative.rank", start, []attribute.KeyValue{attribute.Int("candidate_count", len(req.Candidates)), attribute.Int("evidence_count", len(req.Evidence)), attribute.String("narrative_pacing_profile", string(req.PacingProfile)), attribute.Int("narrative_variant_count", req.VariantCount), attribute.Bool("narrative_intent_present", req.NarrativeIntent != ""), attribute.Int("narrative_intent_length", len([]rune(req.NarrativeIntent)))}, err)
	}
	if err != nil {
		return videoindexerstudio.NarrativeRankingResponse{}, err
	}
	response := videoindexerstudio.NarrativeRankingResponse{SchemaVersion: videoindexerstudio.NarrativeRankingSchemaVersion, OrderedClips: make([]videoindexerstudio.NarrativeRankedClip, 0, len(plan.Suggestions))}
	for _, suggestion := range plan.Suggestions {
		evidenceIDs := make([]string, 0, len(suggestion.SourceRefs))
		for _, ref := range suggestion.SourceRefs {
			evidenceIDs = append(evidenceIDs, ref.RefID)
		}
		response.OrderedClips = append(response.OrderedClips, videoindexerstudio.NarrativeRankedClip{CandidateID: suggestion.ID, EvidenceIDs: evidenceIDs})
	}
	if err := validateNarrativeRankingResponse(req, response); err != nil {
		return videoindexerstudio.NarrativeRankingResponse{}, err
	}
	return response, nil
}

func narrativeRankingPrompt(packet string) string {
	return fmt.Sprintf("narrative-ranker instructions %s\nThe optional narrativeIntent and pacingProfile describe local, already-selected candidate pacing. They are editorial ordering preferences only. It must never justify adding, removing, altering, or duplicating candidates, sources, ranges, evidence, or timeline invariants. Order every candidate exactly once. Use only candidate IDs from this JSON. Do not create, remove, alter, or duplicate IDs. Return every candidate as EditPlan.suggestions[].id with at least one matching EvidenceID in suggestions[].sourceRefs[].refId. Do not use unsupported fields.\n%s", narrativeRankerInstructionsVersion, packet)
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
