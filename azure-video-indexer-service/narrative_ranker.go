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
	narrativeRankerInstructionsVersion = "v3"
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
		r.timeout = narrativeRankingDefaultTimeout
	}
	if r.timeout > narrativeRankingMaxTimeout {
		r.timeout = narrativeRankingMaxTimeout
	}
	raw, err := json.Marshal(req)
	if err != nil {
		return videoindexerstudio.NarrativeRankingResponse{}, narrativeFailureError(narrativeFailureInvalidReq, err)
	}
	rankCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	start := time.Now()
	attempts := 0
	initialValidationReason := ""
	repairValidationReason := ""
	repairAttempted := false

	plan, err := r.runRankingPlan(rankCtx, narrativeRankingPrompt(string(raw)), &attempts)
	response := narrativeRankingResponseFromPlan(plan)
	if err == nil {
		if validationErr := validateNarrativeRankingResponse(req, response); validationErr != nil {
			initialValidationReason = narrativeRankingValidationReason(validationErr)
			err = narrativeFailureError(narrativeFailureInvalid, validationErr)
		}
	}
	if err != nil && initialValidationReason == "missing_candidate_count" && attempts < narrativeRankerAttempts && rankCtx.Err() == nil {
		repairAttempted = true
		repairPrompt, promptErr := narrativeRankingRepairPrompt(req, initialValidationReason)
		if promptErr != nil {
			err = narrativeFailureError(narrativeFailureInvalidReq, promptErr)
		} else {
			plan, err = r.runRankingPlan(rankCtx, repairPrompt, &attempts)
			response = narrativeRankingResponseFromPlan(plan)
		}
		if err == nil {
			if validationErr := validateNarrativeRankingResponse(req, response); validationErr != nil {
				repairValidationReason = narrativeRankingValidationReason(validationErr)
				err = narrativeFailureError(narrativeFailureInvalid, validationErr)
			}
		}
	}
	if rankCtx.Err() != nil && err != nil {
		err = narrativeFailureError(narrativeFailureTimeout, rankCtx.Err())
	}
	validationReason := initialValidationReason
	if repairValidationReason != "" {
		validationReason = repairValidationReason
	}
	if r.obs != nil {
		r.obs.FinishSpan(ctx, nil, "narrative.rank", start, []attribute.KeyValue{
			attribute.String("prompt_version", narrativeRankerInstructionsVersion),
			attribute.Int("candidate_count", len(req.Candidates)),
			attribute.Int("evidence_count", len(req.Evidence)),
			attribute.Int("packet_bytes", len(raw)),
			attribute.Int("attempt_count", attempts),
			attribute.String("failure_kind", string(narrativeFailureFor(err))),
			attribute.String("validation_reason", validationReason),
			attribute.String("initial_validation_reason", initialValidationReason),
			attribute.Bool("repair_attempted", repairAttempted),
			attribute.String("repair_validation_reason", repairValidationReason),
			attribute.Bool("narrative_intent_present", req.NarrativeIntent != ""),
			attribute.Int("narrative_intent_length", len([]rune(req.NarrativeIntent))),
		}, err)
	}
	if err != nil {
		return videoindexerstudio.NarrativeRankingResponse{}, err
	}
	return response, nil
}

func (r narrativeRanker) runRankingPlan(ctx context.Context, prompt string, attempts *int) (EditPlan, error) {
	var plan EditPlan
	var err error
	for *attempts < narrativeRankerAttempts {
		(*attempts)++
		plan, err = r.planner.Plan(ctx, prompt)
		if err != nil {
			err = classifyNarrativeProviderError(err)
		}
		if err == nil || !isRetryableNarrativeFailure(err) || ctx.Err() != nil || *attempts == narrativeRankerAttempts {
			break
		}
		if r.obs != nil {
			r.obs.RecordRetry(ctx, "narrative.rank", 0, attribute.String("failure_kind", string(narrativeFailureFor(err))))
		}
		select {
		case <-ctx.Done():
		case <-time.After(100 * time.Millisecond):
		}
	}
	return plan, err
}

func narrativeRankingResponseFromPlan(plan EditPlan) videoindexerstudio.NarrativeRankingResponse {
	response := videoindexerstudio.NarrativeRankingResponse{SchemaVersion: videoindexerstudio.NarrativeRankingSchemaVersion, OrderedClips: make([]videoindexerstudio.NarrativeRankedClip, 0, len(plan.Suggestions))}
	for _, suggestion := range plan.Suggestions {
		ids := make([]string, 0, len(suggestion.SourceRefs))
		for _, ref := range suggestion.SourceRefs {
			ids = append(ids, ref.RefID)
		}
		response.OrderedClips = append(response.OrderedClips, videoindexerstudio.NarrativeRankedClip{CandidateID: suggestion.ID, EvidenceIDs: ids})
	}
	return response
}

func narrativeRankingPrompt(packet string) string {
	return fmt.Sprintf("narrative-ranker instructions %s\nThe optional narrativeIntent is an editorial ordering preference only. Local candidate selection is already complete. It must never justify adding, removing, altering, or duplicating candidates, sources, ranges, evidence, or timeline invariants. Order every candidate exactly once. Use only candidate IDs from this JSON. Do not create, remove, alter, or duplicate IDs. Return every candidate as EditPlan.suggestions[].id with at least one matching EvidenceID in suggestions[].sourceRefs[].refId. Populate the generic EditPlan structured schema exactly; use empty or zero values only where its required non-ranking fields are irrelevant.\n%s", narrativeRankerInstructionsVersion, packet)
}

type narrativeRankingRepairPacket struct {
	InvalidReason string                            `json:"invalidReason"`
	Candidates    []narrativeRankingRepairCandidate `json:"candidates"`
}
type narrativeRankingRepairCandidate struct {
	CandidateID string   `json:"candidateId"`
	EvidenceIDs []string `json:"evidenceIds"`
}

func narrativeRankingRepairPrompt(req videoindexerstudio.NarrativeRankingRequest, invalidReason string) (string, error) {
	packet := narrativeRankingRepairPacket{InvalidReason: invalidReason, Candidates: make([]narrativeRankingRepairCandidate, 0, len(req.Candidates))}
	for _, candidate := range req.Candidates {
		packet.Candidates = append(packet.Candidates, narrativeRankingRepairCandidate{CandidateID: candidate.ID, EvidenceIDs: candidate.EvidenceIDs})
	}
	raw, err := json.Marshal(packet)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("narrative-ranker repair instructions %s\nThe prior response was rejected for %s. Return a corrected generic EditPlan structured response. EditPlan.suggestions must contain exactly every candidateId below once, in the intended order. Each suggestion must include at least one sourceRefs[].refId listed for that same candidate. Do not add, omit, duplicate, rename, or modify candidates or evidence. Populate the generic EditPlan structured schema exactly; use empty or zero values only where its required non-ranking fields are irrelevant.\n%s", narrativeRankerInstructionsVersion, invalidReason, raw), nil
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

type narrativeRankingValidationError struct{ reason string }

func (e narrativeRankingValidationError) Error() string {
	return "narrative ranking response validation failed"
}
func narrativeRankingValidationReason(err error) string {
	var validationErr narrativeRankingValidationError
	if errors.As(err, &validationErr) {
		return validationErr.reason
	}
	return ""
}
func narrativeRankingValidationFailure(reason string) error {
	return narrativeRankingValidationError{reason: reason}
}

func validateNarrativeRankingResponse(req videoindexerstudio.NarrativeRankingRequest, response videoindexerstudio.NarrativeRankingResponse) error {
	if response.SchemaVersion != videoindexerstudio.NarrativeRankingSchemaVersion {
		return narrativeRankingValidationFailure("invalid_schema_version")
	}
	if len(response.OrderedClips) != len(req.Candidates) {
		return narrativeRankingValidationFailure("missing_candidate_count")
	}
	known := make(map[string]struct{}, len(req.Candidates))
	for _, candidate := range req.Candidates {
		known[candidate.ID] = struct{}{}
	}
	seen := make(map[string]struct{}, len(response.OrderedClips))
	for _, ranked := range response.OrderedClips {
		if _, ok := known[ranked.CandidateID]; !ok {
			return narrativeRankingValidationFailure("unknown_candidate")
		}
		if _, exists := seen[ranked.CandidateID]; exists {
			return narrativeRankingValidationFailure("duplicate_candidate")
		}
		if len(ranked.EvidenceIDs) == 0 {
			return narrativeRankingValidationFailure("missing_evidence")
		}
		seen[ranked.CandidateID] = struct{}{}
		rankedEvidence := make(map[string]struct{}, len(ranked.EvidenceIDs))
		for _, evidenceID := range ranked.EvidenceIDs {
			if _, duplicate := rankedEvidence[evidenceID]; duplicate {
				return narrativeRankingValidationFailure("duplicate_evidence")
			}
			rankedEvidence[evidenceID] = struct{}{}
			if !candidateHasEvidence(req, ranked.CandidateID, evidenceID) {
				return narrativeRankingValidationFailure("ungrounded_evidence")
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
