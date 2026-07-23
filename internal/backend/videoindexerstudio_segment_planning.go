package backend

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/zecloud/ai-video-studio/internal/videoindexerstudio"
)

var (
	errNarrativeSegmentCatalogInvalid   = errors.New("narrative segment catalog is invalid")
	errNarrativeSegmentPlanInvalid      = errors.New("narrative segment plan is invalid")
	errNarrativeSegmentNoVerifiableMatch = errors.New("narrative query has no verifiable match")
)

const (
	narrativePlanningGridMS       int64 = 100
	narrativePlanningMinimumMS    int64 = 1000
	narrativePlanningMaximumMS    int64 = 15000
	narrativePlanningMaxPerSource       = 12
	narrativePlanningMaxTotalMS   int64 = 180000
)

func narrativeIntentProfileForPacing(profile videoindexerstudio.NarrativePacingProfile) videoindexerstudio.NarrativeIntentProfile {
	switch profile {
	case videoindexerstudio.NarrativePacingProfileEnergeticShortForm:
		return videoindexerstudio.NarrativeIntentProfileEnergetic
	case videoindexerstudio.NarrativePacingProfileCalmRecap:
		return videoindexerstudio.NarrativeIntentProfileCalm
	case videoindexerstudio.NarrativePacingProfileChronologicalContinuity:
		return videoindexerstudio.NarrativeIntentProfileChronological
	case videoindexerstudio.NarrativePacingProfileCinematic:
		return videoindexerstudio.NarrativeIntentProfileCinematic
	case videoindexerstudio.NarrativePacingProfileSocialShortForm:
		return videoindexerstudio.NarrativeIntentProfileSocialShortForm
	case videoindexerstudio.NarrativePacingProfileTutorial:
		return videoindexerstudio.NarrativeIntentProfileTutorial
	case videoindexerstudio.NarrativePacingProfileHighlightReel:
		return videoindexerstudio.NarrativeIntentProfileHighlightReel
	case videoindexerstudio.NarrativePacingProfileRecap:
		return videoindexerstudio.NarrativeIntentProfileRecap
	case videoindexerstudio.NarrativePacingProfileStorytelling:
		return videoindexerstudio.NarrativeIntentProfileStorytelling
	case videoindexerstudio.NarrativePacingProfileTravel:
		return videoindexerstudio.NarrativeIntentProfileTravel
	case videoindexerstudio.NarrativePacingProfileInterview:
		return videoindexerstudio.NarrativeIntentProfileInterview
	case videoindexerstudio.NarrativePacingProfileProductShowcase:
		return videoindexerstudio.NarrativeIntentProfileProductShowcase
	default:
		return videoindexerstudio.NarrativeIntentProfileStandard
	}
}

func buildNarrativeSegmentCatalog(composition videoindexerstudio.CompositionEditPlan, dependencies []VideoIndexerStudioJob) (videoindexerstudio.NarrativeSegmentPlanningRequest, videoindexerstudio.NarrativeSelectionOutcome, error) {
	ranking, err := buildNarrativeRankingRequest(composition, dependencies)
	if err != nil {
		return videoindexerstudio.NarrativeSegmentPlanningRequest{}, videoindexerstudio.NarrativeSelectionOutcomeUnavailable, err
	}
	evidenceByID := make(map[string]videoindexerstudio.NarrativeEvidence, len(ranking.Evidence))
	for _, evidence := range ranking.Evidence {
		evidenceByID[evidence.ID] = evidence
	}

	catalogCandidates := ranking.Candidates
	outcome := videoindexerstudio.NarrativeSelectionOutcomeUnavailable
	if composition.NarrativeQuery != nil && composition.NarrativeQuery.Actionable() {
		catalogCandidates, outcome = selectQueryAwareCandidates(ranking.Candidates, evidenceByID, composition.NarrativeQuery)
		if len(catalogCandidates) == 0 {
			return videoindexerstudio.NarrativeSegmentPlanningRequest{}, outcome, errNarrativeSegmentNoVerifiableMatch
		}
	}

	catalog := make([]videoindexerstudio.NarrativeSegmentCatalogItem, 0, len(catalogCandidates))
	for _, candidate := range catalogCandidates {
		item := videoindexerstudio.NarrativeSegmentCatalogItem{SegmentID: candidate.ID, CandidateID: candidate.ID, SourceAssetID: candidate.SourceAssetID, AllowedStartMs: candidate.StartMs, AllowedEndMs: candidate.EndMs}
		for _, evidenceID := range candidate.EvidenceIDs {
			evidence, ok := evidenceByID[evidenceID]
			if !ok {
				continue
			}
			start, end := maxInt64(evidence.StartMs, candidate.StartMs), minInt64(evidence.EndMs, candidate.EndMs)
			if end <= start {
				continue
			}
			item.EvidenceIDs = append(item.EvidenceIDs, evidenceID)
			item.Evidence = append(item.Evidence, videoindexerstudio.NarrativeSegmentEvidence{EvidenceID: evidenceID, Kind: evidence.Kind, StartMs: start, EndMs: end, Descriptor: canonicalNarrativeSegmentDescriptor(evidence.Text)})
		}
		item.Descriptor = narrativeSegmentDescriptor(item.EvidenceIDs, evidenceByID)
		catalog = append(catalog, item)
	}
	request := videoindexerstudio.NarrativeSegmentPlanningRequest{SchemaVersion: videoindexerstudio.NarrativeSegmentPlanningSchemaVersion, CompositionID: composition.CompositionID, NarrativeIntent: composition.NarrativeIntent, Profile: composition.EditorialProfile, Catalog: catalog}
	if err := request.Validate(); err != nil {
		return videoindexerstudio.NarrativeSegmentPlanningRequest{}, videoindexerstudio.NarrativeSelectionOutcomeUnavailable, fmt.Errorf("%w: %v", errNarrativeSegmentCatalogInvalid, err)
	}
	if outcome == videoindexerstudio.NarrativeSelectionOutcomeUnavailable && composition.NarrativeQuery != nil && composition.NarrativeQuery.Actionable() {
		outcome = videoindexerstudio.NarrativeSelectionOutcomePartial
	}
	return request, outcome, nil
}

type narrativeQueryScoredCandidate struct {
	candidate videoindexerstudio.NarrativeRankingCandidate
	eval      videoindexerstudio.NarrativeQueryEvaluation
	score     float64
}

func selectQueryAwareCandidates(candidates []videoindexerstudio.NarrativeRankingCandidate, evidenceByID map[string]videoindexerstudio.NarrativeEvidence, query *videoindexerstudio.NarrativeQuery) ([]videoindexerstudio.NarrativeRankingCandidate, videoindexerstudio.NarrativeSelectionOutcome) {
	qualified := make([]narrativeQueryScoredCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		evidence := make([]videoindexerstudio.NarrativeEvidence, 0, len(candidate.EvidenceIDs))
		for _, id := range candidate.EvidenceIDs {
			if item, ok := evidenceByID[id]; ok {
				evidence = append(evidence, item)
			}
		}
		eval := videoindexerstudio.EvaluateNarrativeQuery(evidence, query)
		if eval.HasUnsupported || !eval.MustSatisfied || eval.AvoidViolated {
			continue
		}
		score := candidate.Score
		if eval.PreferSatisfied {
			score += 1.0
		}
		qualified = append(qualified, narrativeQueryScoredCandidate{candidate: candidate, eval: eval, score: score})
	}
	if len(qualified) == 0 {
		return nil, videoindexerstudio.NarrativeSelectionOutcomeNoMatch
	}
	switch query.Coverage {
	case videoindexerstudio.NarrativeQueryCoveragePerMatchingSource:
		qualified = selectPerMatchingSource(qualified)
	default:
		sort.SliceStable(qualified, func(i, j int) bool {
			if qualified[i].score != qualified[j].score {
				return qualified[i].score > qualified[j].score
			}
			return qualified[i].candidate.ID < qualified[j].candidate.ID
		})
		limit := narrativeMaxCandidates
		if query.Coverage == videoindexerstudio.NarrativeQueryCoverageExhaustive && len(qualified) < narrativeMaxCandidates {
			limit = len(qualified)
		}
		if len(qualified) > limit {
			qualified = qualified[:limit]
		}
	}
	result := make([]videoindexerstudio.NarrativeRankingCandidate, len(qualified))
	preferFound := false
	for i, item := range qualified {
		result[i] = item.candidate
		if item.eval.PreferSatisfied {
			preferFound = true
		}
	}
	outcome := videoindexerstudio.NarrativeSelectionOutcomePartial
	if preferFound {
		outcome = videoindexerstudio.NarrativeSelectionOutcomeComplete
	}
	return result, outcome
}

func selectPerMatchingSource(qualified []narrativeQueryScoredCandidate) []narrativeQueryScoredCandidate {
	bySource := make(map[string][]narrativeQueryScoredCandidate)
	for _, item := range qualified {
		bySource[item.candidate.SourceAssetID] = append(bySource[item.candidate.SourceAssetID], item)
	}
	selected := make([]narrativeQueryScoredCandidate, 0, len(qualified))
	for _, items := range bySource {
		if len(items) == 0 {
			continue
		}
		sort.SliceStable(items, func(i, j int) bool {
			return items[i].score > items[j].score
		})
		selected = append(selected, items[0])
	}
	return selected
}

func narrativeSegmentDescriptor(evidenceIDs []string, evidenceByID map[string]videoindexerstudio.NarrativeEvidence) string {
	parts := make([]string, 0, len(evidenceIDs))
	for _, id := range evidenceIDs {
		evidence, ok := evidenceByID[id]
		if !ok {
			continue
		}
		part := evidence.Kind
		if text := canonicalNarrativeSegmentDescriptor(evidence.Text); text != "" {
			part += ": " + text
		}
		parts = append(parts, part)
	}
	return canonicalNarrativeSegmentDescriptor(strings.Join(parts, "; "))
}

func canonicalNarrativeSegmentDescriptor(value string) string {
	normalized, err := videoindexerstudio.NormalizeNarrativeIntent(value)
	if err == nil {
		return normalized
	}
	normalized, err = videoindexerstudio.NormalizeNarrativeIntent(truncateNarrativeText(value))
	if err != nil {
		return ""
	}
	return normalized
}

func planMultiVideoCompositionSegments(ctx context.Context, planner narrativeSegmentPlanningClient, plan videoindexerstudio.EditPlan, composition videoindexerstudio.CompositionEditPlan, dependencies []VideoIndexerStudioJob) (videoindexerstudio.EditPlan, videoindexerstudio.CompositionEditPlan, []videoindexerstudio.TimelineDraft, error) {
	if planner == nil {
		return videoindexerstudio.EditPlan{}, videoindexerstudio.CompositionEditPlan{}, nil, errors.New("narrative segment planner is unavailable")
	}
	request, outcome, err := buildNarrativeSegmentCatalog(composition, dependencies)
	if err != nil {
		return videoindexerstudio.EditPlan{}, videoindexerstudio.CompositionEditPlan{}, nil, err
	}
	response, err := planner.PlanNarrativeSegments(ctx, request)
	if err != nil {
		return videoindexerstudio.EditPlan{}, videoindexerstudio.CompositionEditPlan{}, nil, err
	}
	plannedPlan, plannedComposition, drafts, err := applyNarrativeSegmentPlan(plan, composition, request, response)
	if err != nil {
		return videoindexerstudio.EditPlan{}, videoindexerstudio.CompositionEditPlan{}, nil, fmt.Errorf("%w: %v", errNarrativeSegmentPlanInvalid, err)
	}
	plannedComposition.SelectionOutcome = outcome
	plannedComposition.PlanningMode = videoindexerstudio.NarrativeSegmentPlanningModeFoundryStructured
	return plannedPlan, plannedComposition, drafts, nil
}

func applyNarrativeSegmentPlan(plan videoindexerstudio.EditPlan, composition videoindexerstudio.CompositionEditPlan, request videoindexerstudio.NarrativeSegmentPlanningRequest, response *videoindexerstudio.NarrativeSegmentPlanningResponse) (videoindexerstudio.EditPlan, videoindexerstudio.CompositionEditPlan, []videoindexerstudio.TimelineDraft, error) {
	if response == nil || response.ValidateAgainst(request) != nil {
		return videoindexerstudio.EditPlan{}, videoindexerstudio.CompositionEditPlan{}, nil, errors.New("invalid narrative segment planning response")
	}
	catalog := map[string]videoindexerstudio.NarrativeSegmentCatalogItem{}
	clips := map[string]videoindexerstudio.CompositionClip{}
	for _, item := range request.Catalog {
		catalog[item.SegmentID] = item
	}
	for _, clip := range composition.Clips {
		clips[clip.ID] = clip
	}
	selected := make([]videoindexerstudio.CompositionClip, 0, len(response.Segments))
	sourceCounts := map[string]int{}
	seen := map[string]struct{}{}
	var total int64
	for _, planned := range response.Segments {
		item, ok := catalog[planned.SegmentID]
		if !ok {
			return videoindexerstudio.EditPlan{}, videoindexerstudio.CompositionEditPlan{}, nil, errors.New("narrative segment plan references unknown segment")
		}
		if _, duplicate := seen[planned.SegmentID]; duplicate {
			return videoindexerstudio.EditPlan{}, videoindexerstudio.CompositionEditPlan{}, nil, errors.New("narrative segment plan duplicates segment")
		}
		seen[planned.SegmentID] = struct{}{}
		if !narrativePlanCitesKnownEvidence(planned.AnchorEvidenceIDs, item.EvidenceIDs) {
			return videoindexerstudio.EditPlan{}, videoindexerstudio.CompositionEditPlan{}, nil, errors.New("narrative segment plan references ungrounded evidence")
		}
		anchorStart, anchorEnd, anchorErr := narrativeSegmentAnchor(item, planned.AnchorEvidenceIDs, planned.AnchorMode)
		if anchorErr != nil {
			return videoindexerstudio.EditPlan{}, videoindexerstudio.CompositionEditPlan{}, nil, anchorErr
		}
		start, end := narrativeSegmentTrim(item, anchorStart, anchorEnd, composition.PacingProfile, planned.Role)
		if end-start < narrativePlanningMinimumMS || end-start > narrativePlanningMaximumMS {
			return videoindexerstudio.EditPlan{}, videoindexerstudio.CompositionEditPlan{}, nil, errors.New("narrative segment plan violates duration budget")
		}
		if start > anchorStart || end < anchorEnd {
			return videoindexerstudio.EditPlan{}, videoindexerstudio.CompositionEditPlan{}, nil, errors.New("narrative segment plan truncates protected evidence")
		}
		clip := clips[item.CandidateID]
		if clip.ID == "" || clip.SourceAssetID != item.SourceAssetID {
			return videoindexerstudio.EditPlan{}, videoindexerstudio.CompositionEditPlan{}, nil, errors.New("narrative segment plan changes source")
		}

		clip.StartMs, clip.EndMs = start, end
		sourceCounts[clip.SourceAssetID]++
		total += end - start
		if sourceCounts[clip.SourceAssetID] > narrativePlanningMaxPerSource || total > narrativePlanningMaxTotalMS || compositionCandidateOverlapsSelected(compositionCandidate{clip: videoindexerstudio.SuggestedClip{SourceAssetID: clip.SourceAssetID, StartMs: start, EndMs: end}}, compositionCandidatesFromClips(selected)) {
			return videoindexerstudio.EditPlan{}, videoindexerstudio.CompositionEditPlan{}, nil, errors.New("narrative segment plan violates overlap or source budget")
		}
		selected = append(selected, clip)
	}
	if len(selected) == 0 {
		return videoindexerstudio.EditPlan{}, videoindexerstudio.CompositionEditPlan{}, nil, errors.New("narrative segment plan is empty")
	}
	return rebuildCompositionFromClips(plan, composition, selected)
}

func narrativeSegmentAnchor(item videoindexerstudio.NarrativeSegmentCatalogItem, ids []string, mode videoindexerstudio.NarrativeSegmentAnchorMode) (int64, int64, error) {
	if !mode.Valid() || len(ids) == 0 {
		return 0, 0, errors.New("narrative segment plan has invalid anchor")
	}
	byID := make(map[string]videoindexerstudio.NarrativeSegmentEvidence, len(item.Evidence))
	for _, evidence := range item.Evidence {
		byID[evidence.EvidenceID] = evidence
	}
	first, ok := byID[ids[0]]
	if !ok {
		return 0, 0, errors.New("narrative segment plan references unknown anchor evidence")
	}
	start, end := first.StartMs, first.EndMs
	seen := map[string]struct{}{ids[0]: {}}
	for _, id := range ids[1:] {
		evidence, known := byID[id]
		if !known {
			return 0, 0, errors.New("narrative segment plan references unknown anchor evidence")
		}
		if _, duplicate := seen[id]; duplicate {
			return 0, 0, errors.New("narrative segment plan duplicates anchor evidence")
		}
		seen[id] = struct{}{}
		if mode == videoindexerstudio.NarrativeSegmentAnchorModeSimultaneous {
			start, end = maxInt64(start, evidence.StartMs), minInt64(end, evidence.EndMs)
		} else {
			start, end = minInt64(start, evidence.StartMs), maxInt64(end, evidence.EndMs)
		}
	}
	if start < item.AllowedStartMs || end > item.AllowedEndMs || end <= start || end-start > narrativePlanningMaximumMS {
		return 0, 0, errors.New("narrative segment plan has an invalid protected anchor")
	}
	return start, end, nil
}

func narrativeSegmentTrim(item videoindexerstudio.NarrativeSegmentCatalogItem, anchorStart, anchorEnd int64, profile videoindexerstudio.NarrativePacingProfile, role videoindexerstudio.NarrativeSegmentRole) (int64, int64) {
	margin := int64(1000)
	switch profile {
	case videoindexerstudio.NarrativePacingProfileEnergeticShortForm, videoindexerstudio.NarrativePacingProfileSocialShortForm, videoindexerstudio.NarrativePacingProfileHighlightReel:
		margin = 500
	case videoindexerstudio.NarrativePacingProfileCalmRecap, videoindexerstudio.NarrativePacingProfileRecap, videoindexerstudio.NarrativePacingProfileTravel, videoindexerstudio.NarrativePacingProfileInterview:
		margin = 1500
	}
	before, after := margin, margin
	if role == videoindexerstudio.NarrativeSegmentRoleHook {
		before = margin / 2
	} else if role == videoindexerstudio.NarrativeSegmentRoleOutro {
		after = margin / 2
	}
	start := maxInt64(item.AllowedStartMs, floorToGrid(anchorStart-before, narrativePlanningGridMS))
	end := minInt64(item.AllowedEndMs, ceilToGrid(anchorEnd+after, narrativePlanningGridMS))
	if end-start > narrativePlanningMaximumMS {
		extra := narrativePlanningMaximumMS - (anchorEnd - anchorStart)
		start = maxInt64(item.AllowedStartMs, floorToGrid(anchorStart-extra/2, narrativePlanningGridMS))
		end = minInt64(item.AllowedEndMs, start+narrativePlanningMaximumMS)
		if end < anchorEnd {
			end = minInt64(item.AllowedEndMs, ceilToGrid(anchorEnd, narrativePlanningGridMS))
			start = maxInt64(item.AllowedStartMs, end-narrativePlanningMaximumMS)
		}
		if start > anchorStart || end < anchorEnd {
			start, end = anchorStart, anchorEnd
		}
	}
	if end-start < narrativePlanningMinimumMS {
		missing := narrativePlanningMinimumMS - (end - start)
		start = maxInt64(item.AllowedStartMs, floorToGrid(start-missing/2, narrativePlanningGridMS))
		end = minInt64(item.AllowedEndMs, start+narrativePlanningMinimumMS)
		if end-start < narrativePlanningMinimumMS {
			start = maxInt64(item.AllowedStartMs, end-narrativePlanningMinimumMS)
		}
	}
	return start, end
}

func floorToGrid(value, grid int64) int64 {
	if value <= 0 {
		return 0
	}
	return value / grid * grid
}

func ceilToGrid(value, grid int64) int64 { return (value + grid - 1) / grid * grid }
func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func compositionCandidatesFromClips(clips []videoindexerstudio.CompositionClip) []compositionCandidate {
	candidates := make([]compositionCandidate, 0, len(clips))
	for _, clip := range clips {
		candidates = append(candidates, compositionCandidate{clip: videoindexerstudio.SuggestedClip{ID: clip.ID, SourceAssetID: clip.SourceAssetID, StartMs: clip.StartMs, EndMs: clip.EndMs}})
	}
	return candidates
}
func narrativePlanCitesKnownEvidence(actual, allowed []string) bool {
	if len(actual) == 0 {
		return false
	}
	known := map[string]struct{}{}
	for _, id := range allowed {
		known[id] = struct{}{}
	}
	seen := map[string]struct{}{}
	for _, id := range actual {
		if _, ok := known[id]; !ok {
			return false
		}
		if _, duplicate := seen[id]; duplicate {
			return false
		}
		seen[id] = struct{}{}
	}
	return true
}
func rebuildCompositionFromClips(plan videoindexerstudio.EditPlan, composition videoindexerstudio.CompositionEditPlan, clips []videoindexerstudio.CompositionClip) (videoindexerstudio.EditPlan, videoindexerstudio.CompositionEditPlan, []videoindexerstudio.TimelineDraft, error) {
	suggested := make([]videoindexerstudio.SuggestedClip, 0, len(clips))
	refs := make([]videoindexerstudio.SourceRef, 0)
	var duration int64
	for _, clip := range clips {
		suggested = append(suggested, videoindexerstudio.SuggestedClip{ID: clip.ID, SourceAssetID: clip.SourceAssetID, Title: clip.Title, Reason: clip.Reason, StartMs: clip.StartMs, EndMs: clip.EndMs, Score: clip.Score, SourceRefs: append([]videoindexerstudio.SourceRef(nil), clip.SourceRefs...)})
		refs = append(refs, clip.SourceRefs...)
		duration += clip.EndMs - clip.StartMs
	}
	if len(plan.Suggestions) != 1 {
		return videoindexerstudio.EditPlan{}, videoindexerstudio.CompositionEditPlan{}, nil, errors.New("invalid local composition")
	}
	suggestion := plan.Suggestions[0]
	suggestion.Clips, suggestion.SourceRefs, suggestion.EndMs, suggestion.Score = suggested, refs, duration, averageClipScore(suggested)
	plan.Suggestions = []videoindexerstudio.EditSuggestion{suggestion}
	plan.SourceRefs = refs
	composition.Clips, composition.SourceRefs, composition.PlanningMode, composition.PlanningFallbackReason = clips, refs, videoindexerstudio.NarrativeSegmentPlanningModeFoundryStructured, ""
	composition.EvidenceFingerprint = compositionEvidenceFingerprint(composition.SourceAssetIDs, composition.Sources, clips)
	draft, err := timelineDraftFromSuggestion(composition.CompositionID, suggestion)
	if err != nil {
		return videoindexerstudio.EditPlan{}, videoindexerstudio.CompositionEditPlan{}, nil, err
	}
	return plan, composition, []videoindexerstudio.TimelineDraft{draft}, nil
}
func narrativeSegmentPlanningFallbackReason(err error) videoindexerstudio.NarrativeSegmentPlanningFallbackReason {
	if errors.Is(err, errNarrativeSegmentCatalogInvalid) {
		return videoindexerstudio.NarrativeSegmentPlanningFallbackCatalogInvalid
	}
	if errors.Is(err, errNarrativeSegmentNoVerifiableMatch) {
		return videoindexerstudio.NarrativeSegmentPlanningFallbackNoVerifiableMatch
	}
	if errors.Is(err, errNarrativeSegmentPlanInvalid) {
		return videoindexerstudio.NarrativeSegmentPlanningFallbackInvalidResponse
	}
	if code := narrativeAPIErrorCode(err); code != "" {
		switch code {
		case "narrative_segment_planning_unavailable":
			return videoindexerstudio.NarrativeSegmentPlanningFallbackUnavailable
		case "narrative_segment_planning_timeout":
			return videoindexerstudio.NarrativeSegmentPlanningFallbackTimeout
		case "narrative_segment_planning_invalid_response", "narrative_segment_planning_invalid", "narrative_segment_planning_request_limit":
			return videoindexerstudio.NarrativeSegmentPlanningFallbackInvalidResponse
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return videoindexerstudio.NarrativeSegmentPlanningFallbackTimeout
	}
	return videoindexerstudio.NarrativeSegmentPlanningFallbackRequestFailed
}
