package backend

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/zecloud/ai-video-studio/internal/videoindexerstudio"
)

func TestNarrativeSegmentPlanAppliesOnlyKnownGroundedCandidate(t *testing.T) {
	dependencies := narrativeDependencies()
	plan, composition, _, err := buildMultiVideoComposition("composition-1", []string{"asset-a", "asset-b"}, dependencies)
	if err != nil {
		t.Fatalf("build composition: %v", err)
	}
	composition.EditorialProfile = videoindexerstudio.NarrativeIntentProfileStandard
	request, _, err := buildNarrativeSegmentCatalog(composition, dependencies)
	if err != nil {
		t.Fatalf("build catalog: %v", err)
	}
	first := request.Catalog[0]
	if first.Descriptor == "" {
		t.Fatal("catalog omitted candidate-scoped evidence descriptor")
	}
	response := anchoredPlanningResponse(first.SegmentID, first.EvidenceIDs[0])
	planned, plannedComposition, drafts, err := applyNarrativeSegmentPlan(plan, composition, request, response)
	if err != nil {
		t.Fatalf("apply valid plan: %v", err)
	}
	if len(planned.Suggestions[0].Clips) != 1 || plannedComposition.Clips[0].ID != first.CandidateID || plannedComposition.Clips[0].SourceAssetID != first.SourceAssetID || len(drafts) != 1 || plannedComposition.PlanningMode != videoindexerstudio.NarrativeSegmentPlanningModeFoundryStructured {
		t.Fatalf("plan lost grounding: %#v", plannedComposition)
	}
	wantFingerprint := compositionEvidenceFingerprint(plannedComposition.SourceAssetIDs, plannedComposition.Sources, plannedComposition.Clips)
	if plannedComposition.EvidenceFingerprint != wantFingerprint {
		t.Fatalf("evidence fingerprint was not rebuilt for planned clips: got %q, want %q", plannedComposition.EvidenceFingerprint, wantFingerprint)
	}
}

func TestNarrativeSegmentPlanRejectsUnknownEvidenceAndInvalidTrim(t *testing.T) {
	dependencies := narrativeDependencies()
	plan, composition, _, err := buildMultiVideoComposition("composition-1", []string{"asset-a", "asset-b"}, dependencies)
	if err != nil {
		t.Fatalf("build composition: %v", err)
	}
	composition.EditorialProfile = videoindexerstudio.NarrativeIntentProfileStandard
	request, _, err := buildNarrativeSegmentCatalog(composition, dependencies)
	if err != nil {
		t.Fatalf("build catalog: %v", err)
	}
	first := request.Catalog[0]
	for name, item := range map[string]videoindexerstudio.NarrativeSegmentPlanItem{
		"unknown segment":  {SegmentID: "invented", Role: videoindexerstudio.NarrativeSegmentRoleHook, AnchorEvidenceIDs: []string{first.EvidenceIDs[0]}, AnchorMode: videoindexerstudio.NarrativeSegmentAnchorModeSimultaneous},
		"foreign evidence": {SegmentID: first.SegmentID, Role: videoindexerstudio.NarrativeSegmentRoleHook, AnchorEvidenceIDs: []string{"invented"}, AnchorMode: videoindexerstudio.NarrativeSegmentAnchorModeSimultaneous},
		"model timecode":   {SegmentID: first.SegmentID, StartMs: first.AllowedStartMs, EndMs: first.AllowedEndMs, Role: videoindexerstudio.NarrativeSegmentRoleHook, AnchorEvidenceIDs: []string{first.EvidenceIDs[0]}, AnchorMode: videoindexerstudio.NarrativeSegmentAnchorModeSimultaneous},
	} {
		t.Run(name, func(t *testing.T) {
			response := &videoindexerstudio.NarrativeSegmentPlanningResponse{SchemaVersion: videoindexerstudio.NarrativeSegmentPlanningSchemaVersion, Segments: []videoindexerstudio.NarrativeSegmentPlanItem{item}}
			if _, _, _, err := applyNarrativeSegmentPlan(plan, composition, request, response); err == nil {
				t.Fatal("expected grounded-plan rejection")
			}
		})
	}
}

func TestNarrativeSegmentPlanRejectsDuplicateSegmentAndInvalidRole(t *testing.T) {
	dependencies := narrativeDependencies()
	plan, composition, _, err := buildMultiVideoComposition("composition-1", []string{"asset-a", "asset-b"}, dependencies)
	if err != nil {
		t.Fatalf("build composition: %v", err)
	}
	composition.EditorialProfile = videoindexerstudio.NarrativeIntentProfileStandard
	request, _, err := buildNarrativeSegmentCatalog(composition, dependencies)
	if err != nil {
		t.Fatalf("build catalog: %v", err)
	}
	first := request.Catalog[0]
	duplicate := &videoindexerstudio.NarrativeSegmentPlanningResponse{SchemaVersion: videoindexerstudio.NarrativeSegmentPlanningSchemaVersion, Segments: []videoindexerstudio.NarrativeSegmentPlanItem{
		{SegmentID: first.SegmentID, Role: videoindexerstudio.NarrativeSegmentRoleHook, AnchorEvidenceIDs: []string{first.EvidenceIDs[0]}, AnchorMode: videoindexerstudio.NarrativeSegmentAnchorModeSimultaneous},
		{SegmentID: first.SegmentID, Role: videoindexerstudio.NarrativeSegmentRolePayoff, AnchorEvidenceIDs: []string{first.EvidenceIDs[0]}, AnchorMode: videoindexerstudio.NarrativeSegmentAnchorModeSimultaneous},
	}}
	if _, _, _, err := applyNarrativeSegmentPlan(plan, composition, request, duplicate); err == nil {
		t.Fatal("expected duplicate segment rejection")
	}
	invalidRole := &videoindexerstudio.NarrativeSegmentPlanningResponse{SchemaVersion: videoindexerstudio.NarrativeSegmentPlanningSchemaVersion, Segments: []videoindexerstudio.NarrativeSegmentPlanItem{{SegmentID: first.SegmentID, Role: "invented", AnchorEvidenceIDs: []string{first.EvidenceIDs[0]}, AnchorMode: videoindexerstudio.NarrativeSegmentAnchorModeSimultaneous}}}
	if _, _, _, err := applyNarrativeSegmentPlan(plan, composition, request, invalidRole); err == nil {
		t.Fatal("expected invalid role rejection")
	}
}

type narrativeSegmentPlanningClientFunc func(context.Context, videoindexerstudio.NarrativeSegmentPlanningRequest) (*videoindexerstudio.NarrativeSegmentPlanningResponse, error)

func (f narrativeSegmentPlanningClientFunc) PlanNarrativeSegments(ctx context.Context, request videoindexerstudio.NarrativeSegmentPlanningRequest) (*videoindexerstudio.NarrativeSegmentPlanningResponse, error) {
	return f(ctx, request)
}

func TestNarrativeSegmentCatalogCanonicalizesOCRAndTranscriptDescriptorsBeforePlanning(t *testing.T) {
	dependencies := narrativeDependencies()
	dependencies[0].VideoIndexResult.Insights.Transcript = []videoindexerstudio.VideoIndexTranscript{{ID: "transcript-crlf", Text: "hello\r\n\tworld\u00a0again", StartMs: 0, EndMs: 2_000}}
	dependencies[0].VideoIndexResult.Insights.OCR = []videoindexerstudio.VideoIndexOCR{{ID: "ocr-long", Text: strings.Repeat("OCR\t", narrativeTextLimit), StartMs: 0, EndMs: 2_000}}
	plan, composition, _, err := buildMultiVideoComposition("composition-1", []string{"asset-a", "asset-b"}, dependencies)
	if err != nil {
		t.Fatalf("build composition: %v", err)
	}
	composition.EditorialProfile = videoindexerstudio.NarrativeIntentProfileSocialShortForm
	request, _, err := buildNarrativeSegmentCatalog(composition, dependencies)
	if err != nil || len(request.Catalog) != len(composition.Clips) {
		t.Fatalf("catalog = %#v, %v", request, err)
	}
	for _, item := range request.Catalog {
		if item.Descriptor != "" {
			normalized, normalizeErr := videoindexerstudio.NormalizeNarrativeIntent(item.Descriptor)
			if normalizeErr != nil || normalized != item.Descriptor || utf8.RuneCountInString(item.Descriptor) > narrativeTextLimit {
				t.Fatalf("descriptor is not canonical: %q, %v", item.Descriptor, normalizeErr)
			}
		}
	}
	called := false
	response := anchoredPlanningResponse(request.Catalog[0].SegmentID, request.Catalog[0].EvidenceIDs[0])
	_, _, _, err = planMultiVideoCompositionSegments(context.Background(), narrativeSegmentPlanningClientFunc(func(_ context.Context, got videoindexerstudio.NarrativeSegmentPlanningRequest) (*videoindexerstudio.NarrativeSegmentPlanningResponse, error) {
		called = true
		if validateErr := got.Validate(); validateErr != nil {
			t.Fatalf("planner received invalid catalog: %v", validateErr)
		}
		return response, nil
	}), plan, composition, dependencies)
	if err != nil || !called {
		t.Fatalf("planner was not reached after catalog canonicalization: called=%t err=%v", called, err)
	}
}

func anchoredPlanningResponse(segmentID, evidenceID string) *videoindexerstudio.NarrativeSegmentPlanningResponse {
	return &videoindexerstudio.NarrativeSegmentPlanningResponse{SchemaVersion: videoindexerstudio.NarrativeSegmentPlanningSchemaVersion, Segments: []videoindexerstudio.NarrativeSegmentPlanItem{{SegmentID: segmentID, Role: videoindexerstudio.NarrativeSegmentRoleHook, AnchorEvidenceIDs: []string{evidenceID}, AnchorMode: videoindexerstudio.NarrativeSegmentAnchorModeSimultaneous}}}
}

func TestNarrativeSegmentAnchorUsesTemporalIntersectionAndSequenceEnvelope(t *testing.T) {
	item := videoindexerstudio.NarrativeSegmentCatalogItem{
		AllowedStartMs: 10_000,
		AllowedEndMs:   20_000,
		Evidence: []videoindexerstudio.NarrativeSegmentEvidence{
			{EvidenceID: "robot", Kind: "object", StartMs: 11_500, EndMs: 16_000},
			{EvidenceID: "dancing", Kind: "label", StartMs: 12_000, EndMs: 15_000},
		},
	}
	start, end, err := narrativeSegmentAnchor(item, []string{"robot", "dancing"}, videoindexerstudio.NarrativeSegmentAnchorModeSimultaneous)
	if err != nil || start != 12_000 || end != 15_000 {
		t.Fatalf("simultaneous anchor = %d-%d, %v", start, end, err)
	}
	start, end, err = narrativeSegmentAnchor(item, []string{"robot", "dancing"}, videoindexerstudio.NarrativeSegmentAnchorModeSequence)
	if err != nil || start != 11_500 || end != 16_000 {
		t.Fatalf("sequence anchor = %d-%d, %v", start, end, err)
	}
}

func TestNarrativeSegmentTrimNeverCutsProtectedAnchor(t *testing.T) {
	item := videoindexerstudio.NarrativeSegmentCatalogItem{AllowedStartMs: 0, AllowedEndMs: 30_000}
	for _, profile := range []videoindexerstudio.NarrativePacingProfile{
		videoindexerstudio.NarrativePacingProfileEnergeticShortForm,
		videoindexerstudio.NarrativePacingProfileCalmRecap,
		videoindexerstudio.NarrativePacingProfileCinematic,
	} {
		start, end := narrativeSegmentTrim(item, 12_050, 14_950, profile, videoindexerstudio.NarrativeSegmentRoleHook)
		if start > 12_050 || end < 14_950 || start%narrativePlanningGridMS != 0 || end%narrativePlanningGridMS != 0 {
			t.Fatalf("profile %q trim %d-%d cuts or misaligns anchor", profile, start, end)
		}
	}
}

func TestNarrativeSegmentTrimPreservesExactMaximumAnchorWhenGridCannotEncloseIt(t *testing.T) {
	item := videoindexerstudio.NarrativeSegmentCatalogItem{AllowedStartMs: 0, AllowedEndMs: 30_000}
	start, end := narrativeSegmentTrim(item, 10_050, 25_050, videoindexerstudio.NarrativePacingProfileStandard, videoindexerstudio.NarrativeSegmentRoleDevelopment)
	if start != 10_050 || end != 25_050 {
		t.Fatalf("maximum protected anchor trim = %d-%d", start, end)
	}
}

func TestNarrativeSegmentAnchorRejectsEmptyIntersection(t *testing.T) {
	item := videoindexerstudio.NarrativeSegmentCatalogItem{AllowedStartMs: 0, AllowedEndMs: 10_000, Evidence: []videoindexerstudio.NarrativeSegmentEvidence{
		{EvidenceID: "before", Kind: "label", StartMs: 1_000, EndMs: 2_000},
		{EvidenceID: "after", Kind: "object", StartMs: 3_000, EndMs: 4_000},
	}}
	if _, _, err := narrativeSegmentAnchor(item, []string{"before", "after"}, videoindexerstudio.NarrativeSegmentAnchorModeSimultaneous); err == nil {
		t.Fatal("expected empty simultaneous intersection rejection")
	}
}

func TestNarrativeSegmentPlanningFallbackReportsLocalCatalogFailure(t *testing.T) {
	if got := narrativeSegmentPlanningFallbackReason(fmt.Errorf("wrapped: %w", errNarrativeSegmentCatalogInvalid)); got != videoindexerstudio.NarrativeSegmentPlanningFallbackCatalogInvalid {
		t.Fatalf("catalog fallback = %q", got)
	}
}

func TestNarrativeSegmentPlanningFallbackReportsLocalPlanFailure(t *testing.T) {
	if got := narrativeSegmentPlanningFallbackReason(fmt.Errorf("wrapped: %w", errNarrativeSegmentPlanInvalid)); got != videoindexerstudio.NarrativeSegmentPlanningFallbackInvalidResponse {
		t.Fatalf("plan fallback = %q", got)
	}
}

func TestNarrativeSegmentCatalogFiltersByActionableQuery(t *testing.T) {
	dependencies := narrativeDependencies()
	dependencies[0].VideoIndexResult.Insights.Labels = []videoindexerstudio.VideoIndexLabel{{ID: "robot", Name: "robot", StartMs: 0, EndMs: 2_000}}
	dependencies[1].VideoIndexResult.Insights.Labels = []videoindexerstudio.VideoIndexLabel{{ID: "tree", Name: "tree", StartMs: 0, EndMs: 2_000}}
	plan, composition, _, err := buildMultiVideoComposition("composition-1", []string{"asset-a", "asset-b"}, dependencies)
	if err != nil {
		t.Fatalf("build composition: %v", err)
	}
	composition.NarrativeQuery = &videoindexerstudio.NarrativeQuery{SchemaVersion: videoindexerstudio.NarrativeQuerySchemaVersion, Coverage: videoindexerstudio.NarrativeQueryCoverageBestSubset, Clauses: []videoindexerstudio.NarrativeQueryClause{
		{ID: "c1", Importance: videoindexerstudio.NarrativeQueryMust, Predicate: videoindexerstudio.NarrativeQueryVisibleEntity, Terms: []string{"robot"}, MatchMode: videoindexerstudio.NarrativeQueryMatchAny, Relation: videoindexerstudio.NarrativeQueryRelationOverlap},
	}}
	composition.EditorialProfile = videoindexerstudio.NarrativeIntentProfileStandard
	request, outcome, err := buildNarrativeSegmentCatalog(composition, dependencies)
	if err != nil {
		t.Fatalf("build catalog: %v", err)
	}
	if len(request.Catalog) != 1 || request.Catalog[0].SourceAssetID != "asset-a" {
		t.Fatalf("expected only asset-a to qualify, got %#v", request.Catalog)
	}
	if outcome != videoindexerstudio.NarrativeSelectionOutcomePartial {
		t.Fatalf("expected partial outcome, got %q", outcome)
	}

	response := anchoredPlanningResponse(request.Catalog[0].SegmentID, request.Catalog[0].EvidenceIDs[0])
	planned, plannedComposition, _, err := applyNarrativeSegmentPlan(plan, composition, request, response)
	if err != nil {
		t.Fatalf("apply plan: %v", err)
	}
	plannedComposition.SelectionOutcome = outcome
	if plannedComposition.SelectionOutcome != videoindexerstudio.NarrativeSelectionOutcomePartial {
		t.Fatalf("expected outcome persisted in composition, got %q", plannedComposition.SelectionOutcome)
	}
	if len(planned.Suggestions[0].Clips) != 1 {
		t.Fatalf("expected one planned clip, got %d", len(planned.Suggestions[0].Clips))
	}
}

func TestNarrativeSegmentCatalogReportsNoVerifiableMatch(t *testing.T) {
	dependencies := narrativeDependencies()
	dependencies[0].VideoIndexResult.Insights.Labels = []videoindexerstudio.VideoIndexLabel{{ID: "tree", Name: "tree", StartMs: 0, EndMs: 2_000}}
	dependencies[1].VideoIndexResult.Insights.Labels = []videoindexerstudio.VideoIndexLabel{{ID: "sky", Name: "sky", StartMs: 0, EndMs: 2_000}}
	_, composition, _, err := buildMultiVideoComposition("composition-1", []string{"asset-a", "asset-b"}, dependencies)
	if err != nil {
		t.Fatalf("build composition: %v", err)
	}
	composition.NarrativeQuery = &videoindexerstudio.NarrativeQuery{SchemaVersion: videoindexerstudio.NarrativeQuerySchemaVersion, Coverage: videoindexerstudio.NarrativeQueryCoverageBestSubset, Clauses: []videoindexerstudio.NarrativeQueryClause{
		{ID: "c1", Importance: videoindexerstudio.NarrativeQueryMust, Predicate: videoindexerstudio.NarrativeQueryVisibleEntity, Terms: []string{"robot"}, MatchMode: videoindexerstudio.NarrativeQueryMatchAny, Relation: videoindexerstudio.NarrativeQueryRelationOverlap},
	}}
	composition.EditorialProfile = videoindexerstudio.NarrativeIntentProfileStandard
	_, outcome, err := buildNarrativeSegmentCatalog(composition, dependencies)
	if !errors.Is(err, errNarrativeSegmentNoVerifiableMatch) {
		t.Fatalf("expected no_verifiable_match error, got %v (outcome %q)", err, outcome)
	}
	if outcome != videoindexerstudio.NarrativeSelectionOutcomeNoMatch {
		t.Fatalf("expected no_match outcome, got %q", outcome)
	}
	if got := narrativeSegmentPlanningFallbackReason(err); got != videoindexerstudio.NarrativeSegmentPlanningFallbackNoVerifiableMatch {
		t.Fatalf("expected no_verifiable_match fallback reason, got %q", got)
	}
}

func TestNarrativeSegmentCatalogExcludesCandidatesShorterThanMinimumDuration(t *testing.T) {
	dependencies := narrativeDependencies()
	degenerate := completedAnalysisJob("analysis-c", "asset-c", 3_003, 3_036, .95)
	degenerate.VideoIndexResult.Insights.Scenes = []videoindexerstudio.VideoIndexScene{{ID: "scene", StartMs: 3_003, EndMs: 3_036}}
	dependencies = append(dependencies, degenerate)
	_, composition, _, err := buildMultiVideoComposition("composition-1", []string{"asset-a", "asset-b", "asset-c"}, dependencies)
	if err != nil {
		t.Fatalf("build composition: %v", err)
	}
	composition.EditorialProfile = videoindexerstudio.NarrativeIntentProfileStandard
	if len(composition.Clips) != 3 {
		t.Fatalf("expected deterministic composition to keep every candidate including the degenerate one, got %d", len(composition.Clips))
	}
	request, _, err := buildNarrativeSegmentCatalog(composition, dependencies)
	if err != nil {
		t.Fatalf("build catalog: %v", err)
	}
	if len(request.Catalog) != 2 {
		t.Fatalf("expected the 33ms candidate to be excluded from the segment-planning catalog, got %d items: %#v", len(request.Catalog), request.Catalog)
	}
	for _, item := range request.Catalog {
		if item.SourceAssetID == "asset-c" {
			t.Fatalf("candidate shorter than the minimum segment duration leaked into the planner catalog: %#v", item)
		}
		if item.AllowedEndMs-item.AllowedStartMs < narrativePlanningMinimumMS {
			t.Fatalf("catalog item %q has a native span below the minimum segment duration", item.SegmentID)
		}
	}
}

func TestNarrativeSegmentCatalogRejectsWhenEveryCandidateIsBelowMinimumDuration(t *testing.T) {
	tinyA := completedAnalysisJob("analysis-tiny-a", "asset-tiny-a", 0, 200, .9)
	tinyA.VideoIndexResult.Insights.Scenes = []videoindexerstudio.VideoIndexScene{{ID: "scene", StartMs: 0, EndMs: 200}}
	tinyB := completedAnalysisJob("analysis-tiny-b", "asset-tiny-b", 0, 200, .9)
	tinyB.VideoIndexResult.Insights.Scenes = []videoindexerstudio.VideoIndexScene{{ID: "scene", StartMs: 0, EndMs: 200}}
	dependencies := []VideoIndexerStudioJob{tinyA, tinyB}
	_, composition, _, err := buildMultiVideoComposition("composition-1", []string{"asset-tiny-a", "asset-tiny-b"}, dependencies)
	if err != nil {
		t.Fatalf("build composition: %v", err)
	}
	composition.EditorialProfile = videoindexerstudio.NarrativeIntentProfileStandard
	_, _, err = buildNarrativeSegmentCatalog(composition, dependencies)
	if !errors.Is(err, errNarrativeSegmentCatalogInvalid) {
		t.Fatalf("expected catalog-invalid error when every candidate is below the minimum segment duration, got %v", err)
	}
	if got := narrativeSegmentPlanningFallbackReason(err); got != videoindexerstudio.NarrativeSegmentPlanningFallbackCatalogInvalid {
		t.Fatalf("expected planner_catalog_invalid fallback reason, got %q", got)
	}
}

func TestNarrativeSegmentCatalogPreservesExistingBehaviorWithoutQuery(t *testing.T) {
	dependencies := narrativeDependencies()
	plan, composition, _, err := buildMultiVideoComposition("composition-1", []string{"asset-a", "asset-b"}, dependencies)
	if err != nil {
		t.Fatalf("build composition: %v", err)
	}
	composition.EditorialProfile = videoindexerstudio.NarrativeIntentProfileStandard
	request, outcome, err := buildNarrativeSegmentCatalog(composition, dependencies)
	if err != nil {
		t.Fatalf("build catalog: %v", err)
	}
	if len(request.Catalog) != len(composition.Clips) {
		t.Fatalf("expected all clips without query, got %d", len(request.Catalog))
	}
	if outcome != videoindexerstudio.NarrativeSelectionOutcomeUnavailable {
		t.Fatalf("expected unavailable outcome without query, got %q", outcome)
	}
	response := anchoredPlanningResponse(request.Catalog[0].SegmentID, request.Catalog[0].EvidenceIDs[0])
	_, plannedComposition, _, err := applyNarrativeSegmentPlan(plan, composition, request, response)
	if err != nil {
		t.Fatalf("apply plan: %v", err)
	}
	plannedComposition.SelectionOutcome = outcome
	if plannedComposition.SelectionOutcome != videoindexerstudio.NarrativeSelectionOutcomeUnavailable {
		t.Fatalf("expected unavailable outcome persisted, got %q", plannedComposition.SelectionOutcome)
	}
}
