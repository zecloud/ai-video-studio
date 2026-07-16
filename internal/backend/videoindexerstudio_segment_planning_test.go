package backend

import (
	"testing"

	"github.com/zecloud/ai-video-studio/internal/videoindexerstudio"
)

func TestNarrativeSegmentPlanAppliesOnlyKnownGroundedCandidate(t *testing.T) {
	dependencies := narrativeDependencies()
	plan, composition, _, err := buildMultiVideoComposition("composition-1", []string{"asset-a", "asset-b"}, dependencies)
	if err != nil {
		t.Fatalf("build composition: %v", err)
	}
	composition.EditorialProfile = videoindexerstudio.NarrativeIntentProfileStandard
	request, err := buildNarrativeSegmentCatalog(composition, dependencies)
	if err != nil {
		t.Fatalf("build catalog: %v", err)
	}
	first := request.Catalog[0]
	if first.Descriptor == "" {
		t.Fatal("catalog omitted candidate-scoped evidence descriptor")
	}
	response := &videoindexerstudio.NarrativeSegmentPlanningResponse{SchemaVersion: videoindexerstudio.NarrativeSegmentPlanningSchemaVersion, Segments: []videoindexerstudio.NarrativeSegmentPlanItem{{SegmentID: first.SegmentID, Role: videoindexerstudio.NarrativeSegmentRoleHook, EvidenceIDs: []string{first.EvidenceIDs[0]}}}}
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
	request, err := buildNarrativeSegmentCatalog(composition, dependencies)
	if err != nil {
		t.Fatalf("build catalog: %v", err)
	}
	first := request.Catalog[0]
	for name, item := range map[string]videoindexerstudio.NarrativeSegmentPlanItem{
		"unknown segment":  {SegmentID: "invented", Role: videoindexerstudio.NarrativeSegmentRoleHook, EvidenceIDs: []string{first.EvidenceIDs[0]}},
		"foreign evidence": {SegmentID: first.SegmentID, Role: videoindexerstudio.NarrativeSegmentRoleHook, EvidenceIDs: []string{"invented"}},
		"off-grid trim":    {SegmentID: first.SegmentID, StartMs: first.AllowedStartMs + 1, EndMs: first.AllowedEndMs, Role: videoindexerstudio.NarrativeSegmentRoleHook, EvidenceIDs: []string{first.EvidenceIDs[0]}},
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
	request, err := buildNarrativeSegmentCatalog(composition, dependencies)
	if err != nil {
		t.Fatalf("build catalog: %v", err)
	}
	first := request.Catalog[0]
	duplicate := &videoindexerstudio.NarrativeSegmentPlanningResponse{SchemaVersion: videoindexerstudio.NarrativeSegmentPlanningSchemaVersion, Segments: []videoindexerstudio.NarrativeSegmentPlanItem{
		{SegmentID: first.SegmentID, Role: videoindexerstudio.NarrativeSegmentRoleHook, EvidenceIDs: []string{first.EvidenceIDs[0]}},
		{SegmentID: first.SegmentID, Role: videoindexerstudio.NarrativeSegmentRolePayoff, EvidenceIDs: []string{first.EvidenceIDs[0]}},
	}}
	if _, _, _, err := applyNarrativeSegmentPlan(plan, composition, request, duplicate); err == nil {
		t.Fatal("expected duplicate segment rejection")
	}
	invalidRole := &videoindexerstudio.NarrativeSegmentPlanningResponse{SchemaVersion: videoindexerstudio.NarrativeSegmentPlanningSchemaVersion, Segments: []videoindexerstudio.NarrativeSegmentPlanItem{{SegmentID: first.SegmentID, Role: "invented", EvidenceIDs: []string{first.EvidenceIDs[0]}}}}
	if _, _, _, err := applyNarrativeSegmentPlan(plan, composition, request, invalidRole); err == nil {
		t.Fatal("expected invalid role rejection")
	}
}
