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
		"energetic":     {intent: "energetic social action-forward", want: NarrativePacingProfileEnergeticShortForm},
		"calm":          {intent: "calm recap", want: NarrativePacingProfileCalmRecap},
		"chronological": {intent: "chronological continuity", want: NarrativePacingProfileChronologicalContinuity},
		"unknown":       {intent: "make it memorable", want: NarrativePacingProfileStandard},
		"precedence":    {intent: "chronological energetic", want: NarrativePacingProfileChronologicalContinuity},
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
	if plan.PacingProfile != "" || plan.VariantCount != 0 {
		t.Fatalf("legacy composition acquired pacing metadata: %#v", plan)
	}
}
