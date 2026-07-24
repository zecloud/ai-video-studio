package videoindexerstudio

import (
	"testing"
)

func TestEvaluateNarrativeQueryRequiresVisibleEntity(t *testing.T) {
	query := NarrativeQuery{SchemaVersion: NarrativeQuerySchemaVersion, Coverage: NarrativeQueryCoverageBestSubset, Clauses: []NarrativeQueryClause{
		{ID: "c1", Importance: NarrativeQueryMust, Predicate: NarrativeQueryVisibleEntity, Terms: []string{"robot"}, MatchMode: NarrativeQueryMatchAny, Relation: NarrativeQueryRelationOverlap},
	}}
	evidence := []NarrativeEvidence{
		{ID: "e1", SourceAssetID: "asset-1", Kind: "label", Text: "Robot", StartMs: 0, EndMs: 1000},
		{ID: "e2", SourceAssetID: "asset-1", Kind: "scene", Text: "", StartMs: 0, EndMs: 1000},
	}
	eval := EvaluateNarrativeQuery(evidence, &query)
	if !eval.MustSatisfied || eval.ClauseEvaluations["c1"] != NarrativeClauseSatisfied {
		t.Fatalf("expected robot label to satisfy must clause, got %#v", eval)
	}
	if SelectionOutcomeFromEvaluation(eval) != NarrativeSelectionOutcomePartial {
		t.Fatalf("expected partial outcome without prefer, got %q", SelectionOutcomeFromEvaluation(eval))
	}
}

func TestEvaluateNarrativeQueryRejectsAvoidedContent(t *testing.T) {
	query := NarrativeQuery{SchemaVersion: NarrativeQuerySchemaVersion, Coverage: NarrativeQueryCoverageBestSubset, Clauses: []NarrativeQueryClause{
		{ID: "c1", Importance: NarrativeQueryMust, Predicate: NarrativeQueryVisibleEntity, Terms: []string{"robot"}, MatchMode: NarrativeQueryMatchAny, Relation: NarrativeQueryRelationOverlap},
		{ID: "c2", Importance: NarrativeQueryAvoid, Predicate: NarrativeQueryVisibleEntity, Terms: []string{"crowd"}, MatchMode: NarrativeQueryMatchAny, Relation: NarrativeQueryRelationOverlap},
	}}
	evidence := []NarrativeEvidence{
		{ID: "e1", SourceAssetID: "asset-1", Kind: "label", Text: "robot", StartMs: 0, EndMs: 1000},
		{ID: "e2", SourceAssetID: "asset-1", Kind: "label", Text: "crowd", StartMs: 500, EndMs: 1500},
	}
	eval := EvaluateNarrativeQuery(evidence, &query)
	if !eval.MustSatisfied || !eval.AvoidViolated {
		t.Fatalf("expected must ok and avoid violated, got %#v", eval)
	}
	if SelectionOutcomeFromEvaluation(eval) != NarrativeSelectionOutcomeNoMatch {
		t.Fatalf("expected no_match when avoid is violated, got %q", SelectionOutcomeFromEvaluation(eval))
	}
}

func TestEvaluateNarrativeQueryPreferBoostsOutcome(t *testing.T) {
	query := NarrativeQuery{SchemaVersion: NarrativeQuerySchemaVersion, Coverage: NarrativeQueryCoverageBestSubset, Clauses: []NarrativeQueryClause{
		{ID: "c1", Importance: NarrativeQueryMust, Predicate: NarrativeQueryVisibleEntity, Terms: []string{"robot"}, MatchMode: NarrativeQueryMatchAny, Relation: NarrativeQueryRelationOverlap},
		{ID: "c2", Importance: NarrativeQueryPrefer, Predicate: NarrativeQueryVisibleEntity, Terms: []string{"dancing"}, MatchMode: NarrativeQueryMatchAny, Relation: NarrativeQueryRelationOverlap},
	}}
	evidence := []NarrativeEvidence{
		{ID: "e1", SourceAssetID: "asset-1", Kind: "object", Text: "robot", StartMs: 0, EndMs: 1000},
		{ID: "e2", SourceAssetID: "asset-1", Kind: "label", Text: "dancing", StartMs: 0, EndMs: 1000},
	}
	eval := EvaluateNarrativeQuery(evidence, &query)
	if !eval.MustSatisfied || !eval.PreferSatisfied {
		t.Fatalf("expected must and prefer satisfied, got %#v", eval)
	}
	if SelectionOutcomeFromEvaluation(eval) != NarrativeSelectionOutcomeComplete {
		t.Fatalf("expected complete outcome, got %q", SelectionOutcomeFromEvaluation(eval))
	}
}

func TestEvaluateNarrativeQueryMatchAllRequiresConjunction(t *testing.T) {
	query := NarrativeQuery{SchemaVersion: NarrativeQuerySchemaVersion, Coverage: NarrativeQueryCoverageBestSubset, Clauses: []NarrativeQueryClause{
		{ID: "c1", Importance: NarrativeQueryMust, Predicate: NarrativeQueryVisibleEntity, Terms: []string{"robot", "dancing"}, MatchMode: NarrativeQueryMatchAll, Relation: NarrativeQueryRelationOverlap},
	}}
	partial := []NarrativeEvidence{
		{ID: "e1", SourceAssetID: "asset-1", Kind: "label", Text: "robot", StartMs: 0, EndMs: 1000},
	}
	if EvaluateNarrativeQuery(partial, &query).MustSatisfied {
		t.Fatal("expected conjunction to fail with only one term")
	}
	full := []NarrativeEvidence{
		{ID: "e1", SourceAssetID: "asset-1", Kind: "label", Text: "robot", StartMs: 0, EndMs: 1000},
		{ID: "e2", SourceAssetID: "asset-1", Kind: "label", Text: "dancing", StartMs: 0, EndMs: 1000},
	}
	if !EvaluateNarrativeQuery(full, &query).MustSatisfied {
		t.Fatal("expected conjunction to succeed with both terms")
	}
}

func TestEvaluateNarrativeQuerySequenceRequiresOrder(t *testing.T) {
	query := NarrativeQuery{SchemaVersion: NarrativeQuerySchemaVersion, Coverage: NarrativeQueryCoverageBestSubset, Clauses: []NarrativeQueryClause{
		{ID: "c1", Importance: NarrativeQueryMust, Predicate: NarrativeQuerySpokenText, Terms: []string{"hello", "world"}, MatchMode: NarrativeQueryMatchAll, Relation: NarrativeQueryRelationSequence},
	}}
	ordered := []NarrativeEvidence{
		{ID: "e1", SourceAssetID: "asset-1", Kind: "transcript", Text: "hello", StartMs: 0, EndMs: 1000},
		{ID: "e2", SourceAssetID: "asset-1", Kind: "transcript", Text: "world", StartMs: 1000, EndMs: 2000},
	}
	if !EvaluateNarrativeQuery(ordered, &query).MustSatisfied {
		t.Fatal("expected ordered sequence to satisfy")
	}
	reversed := []NarrativeEvidence{
		{ID: "e1", SourceAssetID: "asset-1", Kind: "transcript", Text: "world", StartMs: 0, EndMs: 1000},
		{ID: "e2", SourceAssetID: "asset-1", Kind: "transcript", Text: "hello", StartMs: 1000, EndMs: 2000},
	}
	if EvaluateNarrativeQuery(reversed, &query).MustSatisfied {
		t.Fatal("expected reversed sequence to fail")
	}
}

func TestEvaluateNarrativeQueryUnsupportedClauseMarksOutcome(t *testing.T) {
	query := NarrativeQuery{SchemaVersion: NarrativeQuerySchemaVersion, Coverage: NarrativeQueryCoverageBestSubset, Clauses: []NarrativeQueryClause{
		{ID: "c1", Importance: NarrativeQueryMust, Predicate: NarrativeQueryUnsupported, Terms: []string{}, MatchMode: NarrativeQueryMatchAny, Relation: NarrativeQueryRelationOverlap},
	}}
	eval := EvaluateNarrativeQuery(nil, &query)
	if !eval.HasUnsupported {
		t.Fatal("expected unsupported clause to be flagged")
	}
	if SelectionOutcomeFromEvaluation(eval) != NarrativeSelectionOutcomeUnavailable {
		t.Fatalf("expected unavailable outcome, got %q", SelectionOutcomeFromEvaluation(eval))
	}
}

func TestEvaluateNarrativeQueryTreatsNonDetectionAsUnknown(t *testing.T) {
	query := NarrativeQuery{SchemaVersion: NarrativeQuerySchemaVersion, Coverage: NarrativeQueryCoverageBestSubset, Clauses: []NarrativeQueryClause{
		{ID: "c1", Importance: NarrativeQueryMust, Predicate: NarrativeQueryVisibleEntity, Terms: []string{"robot"}, MatchMode: NarrativeQueryMatchAny, Relation: NarrativeQueryRelationOverlap},
	}}
	evidence := []NarrativeEvidence{
		{ID: "e1", SourceAssetID: "asset-1", Kind: "label", Text: "tree", StartMs: 0, EndMs: 1000},
	}
	eval := EvaluateNarrativeQuery(evidence, &query)
	if eval.MustSatisfied || eval.ClauseEvaluations["c1"] != NarrativeClauseUnknown {
		t.Fatalf("expected unknown for non-detection, got %#v", eval)
	}
}

func TestNarrativeQueryActionableIgnoresUnsupportedOnly(t *testing.T) {
	unsupportedOnly := NarrativeQuery{SchemaVersion: NarrativeQuerySchemaVersion, Coverage: NarrativeQueryCoverageBestSubset, Clauses: []NarrativeQueryClause{
		{ID: "c1", Importance: NarrativeQueryMust, Predicate: NarrativeQueryUnsupported, Terms: []string{}, MatchMode: NarrativeQueryMatchAny, Relation: NarrativeQueryRelationOverlap},
	}}
	if unsupportedOnly.Actionable() {
		t.Fatal("query with only unsupported clause should not be actionable")
	}
	actionable := NarrativeQuery{SchemaVersion: NarrativeQuerySchemaVersion, Coverage: NarrativeQueryCoverageBestSubset, Clauses: []NarrativeQueryClause{
		{ID: "c1", Importance: NarrativeQueryMust, Predicate: NarrativeQueryVisibleEntity, Terms: []string{"robot"}, MatchMode: NarrativeQueryMatchAny, Relation: NarrativeQueryRelationOverlap},
	}}
	if !actionable.Actionable() {
		t.Fatal("query with visible_entity clause should be actionable")
	}
}
