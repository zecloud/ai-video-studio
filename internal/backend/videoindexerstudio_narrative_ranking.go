package backend

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/zecloud/ai-video-studio/internal/videoindexerstudio"
)

const (
	narrativeMaxCandidates = 48
	narrativeMaxEvidence   = 54
	narrativeTextLimit     = 240
)

func rankMultiVideoComposition(ctx context.Context, ranker narrativeRankingClient, plan videoindexerstudio.EditPlan, composition videoindexerstudio.CompositionEditPlan, dependencies []VideoIndexerStudioJob) (videoindexerstudio.EditPlan, videoindexerstudio.CompositionEditPlan, []videoindexerstudio.TimelineDraft, error) {
	if ranker == nil || len(plan.Suggestions) != 1 || len(composition.Clips) == 0 {
		return videoindexerstudio.EditPlan{}, videoindexerstudio.CompositionEditPlan{}, nil, errors.New("narrative ranker is unavailable")
	}
	request, err := buildNarrativeRankingRequest(composition, dependencies)
	if err != nil {
		return videoindexerstudio.EditPlan{}, videoindexerstudio.CompositionEditPlan{}, nil, err
	}
	response, err := ranker.RankNarrative(ctx, request)
	if err != nil {
		return videoindexerstudio.EditPlan{}, videoindexerstudio.CompositionEditPlan{}, nil, err
	}
	if err := validateNarrativeRankingResponse(request, response); err != nil {
		return videoindexerstudio.EditPlan{}, videoindexerstudio.CompositionEditPlan{}, nil, err
	}
	byID := make(map[string]videoindexerstudio.CompositionClip, len(composition.Clips))
	for _, clip := range composition.Clips {
		byID[clip.ID] = clip
	}
	ordered := make([]videoindexerstudio.CompositionClip, 0, len(composition.Clips))
	for _, ranked := range response.OrderedClips {
		ordered = append(ordered, byID[ranked.CandidateID])
	}
	clips := make([]videoindexerstudio.SuggestedClip, 0, len(ordered))
	var duration int64
	for _, clip := range ordered {
		clips = append(clips, videoindexerstudio.SuggestedClip{ID: clip.ID, SourceAssetID: clip.SourceAssetID, Title: clip.Title, Reason: clip.Reason, StartMs: clip.StartMs, EndMs: clip.EndMs, Score: clip.Score, SourceRefs: append([]videoindexerstudio.SourceRef(nil), clip.SourceRefs...)})
		duration += clip.EndMs - clip.StartMs
	}
	suggestion := plan.Suggestions[0]
	suggestion.Clips, suggestion.EndMs, suggestion.Score = clips, duration, averageClipScore(clips)
	plan.Suggestions = []videoindexerstudio.EditSuggestion{suggestion}
	composition.Clips = ordered
	composition.RankingMode = "azure_openai_grounded_v1"
	composition.RecommendationVersion = "multi-video-composition-v3"
	draft, err := timelineDraftFromSuggestion(composition.CompositionID, suggestion)
	if err != nil {
		return videoindexerstudio.EditPlan{}, videoindexerstudio.CompositionEditPlan{}, nil, err
	}
	return plan, composition, []videoindexerstudio.TimelineDraft{draft}, nil
}

func buildNarrativeRankingRequest(composition videoindexerstudio.CompositionEditPlan, dependencies []VideoIndexerStudioJob) (videoindexerstudio.NarrativeRankingRequest, error) {
	if len(composition.Clips) > narrativeMaxCandidates {
		return videoindexerstudio.NarrativeRankingRequest{}, errors.New("narrative candidate limit exceeded")
	}
	request := videoindexerstudio.NarrativeRankingRequest{SchemaVersion: videoindexerstudio.NarrativeRankingSchemaVersion, CompositionID: composition.CompositionID, NarrativeIntent: composition.NarrativeIntent}
	for _, clip := range composition.Clips {
		request.Candidates = append(request.Candidates, videoindexerstudio.NarrativeRankingCandidate{ID: clip.ID, SourceAssetID: clip.SourceAssetID, StartMs: clip.StartMs, EndMs: clip.EndMs, Score: clip.Score})
	}
	jobs := make(map[string]*videoindexerstudio.VideoIndexResult, len(dependencies))
	for _, job := range dependencies {
		if job.VideoIndexResult != nil {
			jobs[job.AssetID] = job.VideoIndexResult
		}
	}
	for _, sourceID := range composition.SourceAssetIDs {
		result := jobs[sourceID]
		if result == nil {
			continue
		}
		request.Evidence = append(request.Evidence, narrativeEvidenceForSource(sourceID, *result)...)
	}
	sort.SliceStable(request.Evidence, func(i, j int) bool {
		if request.Evidence[i].SourceAssetID != request.Evidence[j].SourceAssetID {
			return request.Evidence[i].SourceAssetID < request.Evidence[j].SourceAssetID
		}
		if request.Evidence[i].StartMs != request.Evidence[j].StartMs {
			return request.Evidence[i].StartMs < request.Evidence[j].StartMs
		}
		return request.Evidence[i].ID < request.Evidence[j].ID
	})
	var err error
	request.Evidence, err = selectNarrativeEvidence(request.Candidates, request.Evidence)
	if err != nil {
		return videoindexerstudio.NarrativeRankingRequest{}, err
	}
	for i := range request.Candidates {
		for _, evidence := range request.Evidence {
			if evidence.SourceAssetID == request.Candidates[i].SourceAssetID && overlaps(request.Candidates[i].StartMs, request.Candidates[i].EndMs, evidence.StartMs, evidence.EndMs) {
				request.Candidates[i].EvidenceIDs = append(request.Candidates[i].EvidenceIDs, evidence.ID)
			}
		}
		if len(request.Candidates[i].EvidenceIDs) == 0 {
			return videoindexerstudio.NarrativeRankingRequest{}, fmt.Errorf("narrative candidate %q has no grounded evidence", request.Candidates[i].ID)
		}
	}
	return request, request.Validate()
}

func selectNarrativeEvidence(candidates []videoindexerstudio.NarrativeRankingCandidate, evidence []videoindexerstudio.NarrativeEvidence) ([]videoindexerstudio.NarrativeEvidence, error) {
	selected := make(map[string]struct{}, len(candidates)*3)
	for _, candidate := range candidates {
		for _, item := range evidence {
			if item.SourceAssetID == candidate.SourceAssetID && overlaps(candidate.StartMs, candidate.EndMs, item.StartMs, item.EndMs) {
				selected[item.ID] = struct{}{}
				break
			}
		}
		if len(selected) > narrativeMaxEvidence {
			return nil, errors.New("narrative evidence budget cannot cover every candidate")
		}
	}
	const perCandidateLimit = 3
	for _, candidate := range candidates {
		selectedForCandidate := 0
		for _, item := range evidence {
			if item.SourceAssetID == candidate.SourceAssetID && overlaps(candidate.StartMs, candidate.EndMs, item.StartMs, item.EndMs) {
				if _, exists := selected[item.ID]; exists {
					selectedForCandidate++
				}
			}
		}
		for _, item := range evidence {
			if len(selected) == narrativeMaxEvidence || selectedForCandidate == perCandidateLimit {
				break
			}
			if item.SourceAssetID == candidate.SourceAssetID && overlaps(candidate.StartMs, candidate.EndMs, item.StartMs, item.EndMs) {
				if _, exists := selected[item.ID]; !exists {
					selected[item.ID] = struct{}{}
					selectedForCandidate++
				}
			}
		}
	}
	limited := make([]videoindexerstudio.NarrativeEvidence, 0, len(selected))
	for _, item := range evidence {
		if _, ok := selected[item.ID]; ok {
			limited = append(limited, item)
		}
	}
	return limited, nil
}
func narrativeEvidenceForSource(assetID string, result videoindexerstudio.VideoIndexResult) []videoindexerstudio.NarrativeEvidence {
	evidence := make([]videoindexerstudio.NarrativeEvidence, 0)
	add := func(id, kind, text string, start, end int64) {
		if strings.TrimSpace(id) != "" && start >= 0 && end >= start {
			evidence = append(evidence, videoindexerstudio.NarrativeEvidence{ID: assetID + ":" + kind + ":" + id, SourceAssetID: assetID, Kind: kind, StartMs: start, EndMs: end, Text: truncateNarrativeText(text)})
		}
	}
	for _, item := range result.Insights.Scenes {
		add(item.ID, "scene", "", item.StartMs, item.EndMs)
	}
	for _, item := range result.Insights.Transcript {
		add(item.ID, "transcript", item.Text, item.StartMs, item.EndMs)
	}
	for _, item := range result.Insights.OCR {
		add(item.ID, "ocr", item.Text, item.StartMs, item.EndMs)
	}
	for _, item := range result.Insights.Labels {
		add(item.ID, "label", item.Name, item.StartMs, item.EndMs)
	}
	for _, item := range result.Insights.Objects {
		add(item.ID, "object", firstNonEmpty(item.DisplayName, item.Type), item.StartMs, item.EndMs)
	}
	return evidence
}

func validateNarrativeRankingResponse(request videoindexerstudio.NarrativeRankingRequest, response *videoindexerstudio.NarrativeRankingResponse) error {
	if response == nil || response.SchemaVersion != videoindexerstudio.NarrativeRankingSchemaVersion || len(response.OrderedClips) != len(request.Candidates) {
		return errors.New("narrative response must order every candidate")
	}
	known := make(map[string]struct{}, len(request.Candidates))
	evidenceByCandidate := make(map[string]map[string]struct{}, len(request.Candidates))
	for _, candidate := range request.Candidates {
		known[candidate.ID] = struct{}{}
		allowed := make(map[string]struct{}, len(candidate.EvidenceIDs))
		for _, evidenceID := range candidate.EvidenceIDs {
			allowed[evidenceID] = struct{}{}
		}
		evidenceByCandidate[candidate.ID] = allowed
	}
	seen := make(map[string]struct{}, len(response.OrderedClips))
	for _, ranked := range response.OrderedClips {
		if _, ok := known[ranked.CandidateID]; !ok {
			return fmt.Errorf("narrative response references unknown candidate")
		}
		if _, duplicate := seen[ranked.CandidateID]; duplicate {
			return fmt.Errorf("narrative response duplicates candidate")
		}
		if len(ranked.EvidenceIDs) == 0 {
			return fmt.Errorf("narrative response must cite candidate evidence")
		}
		rankedEvidence := make(map[string]struct{}, len(ranked.EvidenceIDs))
		for _, evidenceID := range ranked.EvidenceIDs {
			if _, duplicate := rankedEvidence[evidenceID]; duplicate {
				return fmt.Errorf("narrative response contains duplicate evidence")
			}
			rankedEvidence[evidenceID] = struct{}{}
			if _, ok := evidenceByCandidate[ranked.CandidateID][evidenceID]; !ok {
				return fmt.Errorf("narrative response references ungrounded evidence")
			}
		}
		seen[ranked.CandidateID] = struct{}{}
	}
	return nil
}
func overlaps(aStart, aEnd, bStart, bEnd int64) bool { return aStart < bEnd && bStart < aEnd }
func truncateNarrativeText(value string) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) <= narrativeTextLimit {
		return value
	}
	return string(runes[:narrativeTextLimit])
}
