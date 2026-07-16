package videoindexerstudio

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNormalizeNarrativeIntent(t *testing.T) {
	t.Run("normalizes Unicode whitespace", func(t *testing.T) {
		got, err := NormalizeNarrativeIntent("  chronological\n\t calm\u00a0recap  ")
		if err != nil || got != "chronological calm recap" {
			t.Fatalf("NormalizeNarrativeIntent = %q, %v", got, err)
		}
	})
	t.Run("rejects invalid UTF-8", func(t *testing.T) {
		if _, err := NormalizeNarrativeIntent(string([]byte{0xff})); err == nil {
			t.Fatal("expected invalid UTF-8 rejection")
		}
	})
	t.Run("rejects over limit", func(t *testing.T) {
		if _, err := NormalizeNarrativeIntent(strings.Repeat("a", NarrativeIntentMaxRunes+1)); err == nil {
			t.Fatal("expected maximum length rejection")
		}
	})
}

func TestNarrativeRankingRequestRejectsUnnormalizedIntent(t *testing.T) {
	request := NarrativeRankingRequest{SchemaVersion: NarrativeRankingSchemaVersion, CompositionID: "composition-1", NarrativeIntent: "action\nforward", Candidates: []NarrativeRankingCandidate{{ID: "clip-1", SourceAssetID: "asset-1", StartMs: 0, EndMs: 1, EvidenceIDs: []string{"evidence-1"}}}, Evidence: []NarrativeEvidence{{ID: "evidence-1", SourceAssetID: "asset-1", Kind: "scene", StartMs: 0, EndMs: 1}}}
	if err := request.Validate(); err == nil {
		t.Fatal("expected unnormalized intent rejection")
	}
}

func TestNarrativePacingProfileForIntent(t *testing.T) {
	tests := map[string]struct {
		intent string
		want   NarrativePacingProfile
	}{
		"energetic":        {intent: "energetic social action-forward", want: NarrativePacingProfileEnergeticShortForm},
		"calm":             {intent: "calm recap", want: NarrativePacingProfileCalmRecap},
		"chronological":    {intent: "chronological continuity", want: NarrativePacingProfileChronologicalContinuity},
		"unknown":          {intent: "make it memorable", want: NarrativePacingProfileStandard},
		"precedence":       {intent: "chronological energetic", want: NarrativePacingProfileChronologicalContinuity},
		"social":           {intent: "dynamic TikTok video", want: NarrativePacingProfileSocialShortForm},
		"cinematic":        {intent: "cinematic film", want: NarrativePacingProfileCinematic},
		"tutorial":         {intent: "tutorial guide", want: NarrativePacingProfileTutorial},
		"highlight":        {intent: "highlights", want: NarrativePacingProfileHighlightReel},
		"storytelling":     {intent: "storytelling", want: NarrativePacingProfileStorytelling},
		"travel":           {intent: "travel voyage", want: NarrativePacingProfileTravel},
		"interview":        {intent: "interview", want: NarrativePacingProfileInterview},
		"product showcase": {intent: "product demo", want: NarrativePacingProfileProductShowcase},
		"recap":            {intent: "recap summary", want: NarrativePacingProfileRecap},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			if got := NarrativePacingProfileForIntent(test.intent); got != test.want {
				t.Fatalf("NarrativePacingProfileForIntent(%q) = %q, want %q", test.intent, got, test.want)
			}
		})
	}
}

func TestNarrativeRankingRequestRejectsInvalidPacingMetadata(t *testing.T) {
	request := NarrativeRankingRequest{SchemaVersion: NarrativeRankingSchemaVersion, CompositionID: "composition-1", PacingProfile: "untrusted", Candidates: []NarrativeRankingCandidate{{ID: "clip-1", SourceAssetID: "asset-1", StartMs: 0, EndMs: 1, EvidenceIDs: []string{"evidence-1"}}}, Evidence: []NarrativeEvidence{{ID: "evidence-1", SourceAssetID: "asset-1", Kind: "scene", StartMs: 0, EndMs: 1}}}
	if err := request.Validate(); err == nil {
		t.Fatal("expected invalid pacing metadata rejection")
	}
}

func TestNarrativeRankingRequestRejectsMismatchedPacingProfile(t *testing.T) {
	request := NarrativeRankingRequest{SchemaVersion: NarrativeRankingSchemaVersion, CompositionID: "composition-1", NarrativeIntent: "calm recap", PacingProfile: NarrativePacingProfileEnergeticShortForm, Candidates: []NarrativeRankingCandidate{{ID: "clip-1", SourceAssetID: "asset-1", StartMs: 0, EndMs: 1, EvidenceIDs: []string{"evidence-1"}}}, Evidence: []NarrativeEvidence{{ID: "evidence-1", SourceAssetID: "asset-1", Kind: "scene", StartMs: 0, EndMs: 1}}}
	if err := request.Validate(); err == nil {
		t.Fatal("expected mismatched pacing profile rejection")
	}
}

func TestCompositionEditPlanAcceptsLegacyJSONWithoutPacingMetadata(t *testing.T) {
	var plan CompositionEditPlan
	if err := json.Unmarshal([]byte(`{"schemaVersion":1,"compositionId":"composition-1","title":"Edit","summary":"Summary","rankingMode":"deterministic_grounded_fallback_v1","recommendationVersion":"multi-video-composition-v2","evidenceFingerprint":"fingerprint"}`), &plan); err != nil {
		t.Fatalf("unmarshal legacy composition: %v", err)
	}
	if plan.PacingProfile != "" || plan.VariantCount != 0 || plan.PacingClassifierMode != "" || plan.PacingFallbackReason != "" || plan.EditorialProfile != "" || plan.PlanningMode != "" || plan.PlanningFallbackReason != "" {
		t.Fatalf("legacy composition acquired pacing metadata: %#v", plan)
	}
}
func TestNarrativeIntentClassificationContracts(t *testing.T) {
	valid := NarrativeIntentClassificationRequest{SchemaVersion: NarrativeRankingSchemaVersion, NarrativeIntent: "recapitulatif calme"}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid classification request: %v", err)
	}
	if err := (NarrativeIntentClassificationRequest{SchemaVersion: NarrativeRankingSchemaVersion, NarrativeIntent: "calm\nrecap"}).Validate(); err == nil {
		t.Fatal("expected unnormalized classification request rejection")
	}
	for _, profile := range []NarrativeIntentProfile{NarrativeIntentProfileStandard, NarrativeIntentProfileEnergetic, NarrativeIntentProfileCalm, NarrativeIntentProfileChronological} {
		if err := (NarrativeIntentClassificationResponse{SchemaVersion: NarrativeRankingSchemaVersion, Profile: profile}).Validate(); err != nil {
			t.Fatalf("valid profile %q: %v", profile, err)
		}
	}
	if err := (NarrativeIntentClassificationResponse{SchemaVersion: NarrativeRankingSchemaVersion, Profile: "invented"}).Validate(); err == nil {
		t.Fatal("expected unknown profile rejection")
	}
}

func TestNarrativeSegmentPlanningContractsRejectUngroundedCatalog(t *testing.T) {
	catalog := NarrativeSegmentCatalogItem{SegmentID: "segment-1", CandidateID: "candidate-1", SourceAssetID: "asset-1", AllowedStartMs: 1_000, AllowedEndMs: 3_000, EvidenceIDs: []string{"evidence-1"}}
	request := NarrativeSegmentPlanningRequest{SchemaVersion: NarrativeSegmentPlanningSchemaVersion, CompositionID: "composition-1", NarrativeIntent: "recapitulatif calme", Profile: NarrativeIntentProfileRecap, Catalog: []NarrativeSegmentCatalogItem{catalog}}
	if err := request.Validate(); err != nil {
		t.Fatalf("valid request: %v", err)
	}
	request.Catalog = append(request.Catalog, catalog)
	if err := request.Validate(); err == nil {
		t.Fatal("expected duplicate catalog rejection")
	}
	response := NarrativeSegmentPlanningResponse{SchemaVersion: NarrativeSegmentPlanningSchemaVersion, Segments: []NarrativeSegmentPlanItem{{SegmentID: "segment-1", Role: NarrativeSegmentRoleHook, EvidenceIDs: []string{"evidence-1"}}}}
	if err := response.Validate(); err != nil {
		t.Fatalf("valid response: %v", err)
	}
	response.Segments[0].Role = "invented"
	if err := response.Validate(); err == nil {
		t.Fatal("expected closed role rejection")
	}
}

func TestNarrativeIntentClassificationAcceptsAllClosedProfiles(t *testing.T) {
	profiles := []NarrativeIntentProfile{NarrativeIntentProfileStandard, NarrativeIntentProfileEnergetic, NarrativeIntentProfileCalm, NarrativeIntentProfileChronological, NarrativeIntentProfileCinematic, NarrativeIntentProfileSocialShortForm, NarrativeIntentProfileTutorial, NarrativeIntentProfileHighlightReel, NarrativeIntentProfileRecap, NarrativeIntentProfileStorytelling, NarrativeIntentProfileTravel, NarrativeIntentProfileInterview, NarrativeIntentProfileProductShowcase}
	for _, profile := range profiles {
		if err := (NarrativeIntentClassificationResponse{SchemaVersion: NarrativeRankingSchemaVersion, Profile: profile}).Validate(); err != nil {
			t.Fatalf("profile %q: %v", profile, err)
		}
	}
}
