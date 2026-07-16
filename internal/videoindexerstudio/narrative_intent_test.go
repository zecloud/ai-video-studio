package videoindexerstudio

import (
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
