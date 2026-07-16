package backend

import (
	"context"
	"errors"
	"strings"

	"github.com/zecloud/ai-video-studio/internal/videoindexerstudio"
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

func buildNarrativeSegmentCatalog(composition videoindexerstudio.CompositionEditPlan, dependencies []VideoIndexerStudioJob) (videoindexerstudio.NarrativeSegmentPlanningRequest, error) {
	ranking, err := buildNarrativeRankingRequest(composition, dependencies)
	if err != nil {
		return videoindexerstudio.NarrativeSegmentPlanningRequest{}, err
	}
	evidenceByID := make(map[string]videoindexerstudio.NarrativeEvidence, len(ranking.Evidence))
	for _, evidence := range ranking.Evidence {
		evidenceByID[evidence.ID] = evidence
	}
	catalog := make([]videoindexerstudio.NarrativeSegmentCatalogItem, 0, len(ranking.Candidates))
	for _, candidate := range ranking.Candidates {
		catalog = append(catalog, videoindexerstudio.NarrativeSegmentCatalogItem{SegmentID: candidate.ID, CandidateID: candidate.ID, SourceAssetID: candidate.SourceAssetID, AllowedStartMs: candidate.StartMs, AllowedEndMs: candidate.EndMs, EvidenceIDs: append([]string(nil), candidate.EvidenceIDs...), Descriptor: narrativeSegmentDescriptor(candidate.EvidenceIDs, evidenceByID)})
	}
	request := videoindexerstudio.NarrativeSegmentPlanningRequest{SchemaVersion: videoindexerstudio.NarrativeSegmentPlanningSchemaVersion, CompositionID: composition.CompositionID, NarrativeIntent: composition.NarrativeIntent, Profile: composition.EditorialProfile, Catalog: catalog}
	return request, request.Validate()
}

func narrativeSegmentDescriptor(evidenceIDs []string, evidenceByID map[string]videoindexerstudio.NarrativeEvidence) string {
	parts := make([]string, 0, len(evidenceIDs))
	for _, id := range evidenceIDs {
		evidence, ok := evidenceByID[id]
		if !ok {
			continue
		}
		part := evidence.Kind
		if text := strings.TrimSpace(evidence.Text); text != "" {
			part += ": " + text
		}
		parts = append(parts, part)
	}
	return truncateNarrativeText(strings.Join(parts, "; "))
}

func planMultiVideoCompositionSegments(ctx context.Context, planner narrativeSegmentPlanningClient, plan videoindexerstudio.EditPlan, composition videoindexerstudio.CompositionEditPlan, dependencies []VideoIndexerStudioJob) (videoindexerstudio.EditPlan, videoindexerstudio.CompositionEditPlan, []videoindexerstudio.TimelineDraft, error) {
	if planner == nil {
		return videoindexerstudio.EditPlan{}, videoindexerstudio.CompositionEditPlan{}, nil, errors.New("narrative segment planner is unavailable")
	}
	request, err := buildNarrativeSegmentCatalog(composition, dependencies)
	if err != nil {
		return videoindexerstudio.EditPlan{}, videoindexerstudio.CompositionEditPlan{}, nil, err
	}
	response, err := planner.PlanNarrativeSegments(ctx, request)
	if err != nil {
		return videoindexerstudio.EditPlan{}, videoindexerstudio.CompositionEditPlan{}, nil, err
	}
	return applyNarrativeSegmentPlan(plan, composition, request, response)
}

func applyNarrativeSegmentPlan(plan videoindexerstudio.EditPlan, composition videoindexerstudio.CompositionEditPlan, request videoindexerstudio.NarrativeSegmentPlanningRequest, response *videoindexerstudio.NarrativeSegmentPlanningResponse) (videoindexerstudio.EditPlan, videoindexerstudio.CompositionEditPlan, []videoindexerstudio.TimelineDraft, error) {
	if request.Validate() != nil || response == nil || response.Validate() != nil {
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
		if !narrativePlanCitesKnownEvidence(planned.EvidenceIDs, item.EvidenceIDs) {
			return videoindexerstudio.EditPlan{}, videoindexerstudio.CompositionEditPlan{}, nil, errors.New("narrative segment plan references ungrounded evidence")
		}
		start, end := planned.StartMs, planned.EndMs
		if start == 0 && end == 0 {
			start, end = item.AllowedStartMs, item.AllowedEndMs
		} else {
			if start%narrativePlanningGridMS != 0 || end%narrativePlanningGridMS != 0 || start < item.AllowedStartMs || end > item.AllowedEndMs {
				return videoindexerstudio.EditPlan{}, videoindexerstudio.CompositionEditPlan{}, nil, errors.New("narrative segment plan contains invalid trim")
			}
		}
		if end-start < narrativePlanningMinimumMS || end-start > narrativePlanningMaximumMS {
			return videoindexerstudio.EditPlan{}, videoindexerstudio.CompositionEditPlan{}, nil, errors.New("narrative segment plan violates duration budget")
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
	if errors.Is(err, context.DeadlineExceeded) {
		return videoindexerstudio.NarrativeSegmentPlanningFallbackTimeout
	}
	if err != nil && strings.Contains(err.Error(), "invalid") {
		return videoindexerstudio.NarrativeSegmentPlanningFallbackInvalidResponse
	}
	return videoindexerstudio.NarrativeSegmentPlanningFallbackRequestFailed
}
