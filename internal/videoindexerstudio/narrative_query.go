package videoindexerstudio

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	NarrativeQuerySchemaVersion = 1
	NarrativeQueryMaxClauses     = 8
	NarrativeQueryMaxTerms       = 8
	NarrativeQueryTermMaxRunes   = 80
)

type NarrativeQueryImportance string

const (
	NarrativeQueryMust   NarrativeQueryImportance = "must"
	NarrativeQueryPrefer NarrativeQueryImportance = "prefer"
	NarrativeQueryAvoid  NarrativeQueryImportance = "avoid"
)

type NarrativeQueryPredicate string

const (
	NarrativeQueryVisibleEntity NarrativeQueryPredicate = "visible_entity"
	NarrativeQuerySpokenText    NarrativeQueryPredicate = "spoken_text"
	NarrativeQueryVisibleText   NarrativeQueryPredicate = "visible_text"
	NarrativeQueryUnsupported   NarrativeQueryPredicate = "unsupported"
)

type NarrativeQueryMatchMode string

const (
	NarrativeQueryMatchAny NarrativeQueryMatchMode = "any"
	NarrativeQueryMatchAll NarrativeQueryMatchMode = "all"
)

type NarrativeQueryRelation string

const (
	NarrativeQueryRelationOverlap  NarrativeQueryRelation = "overlap"
	NarrativeQueryRelationSequence NarrativeQueryRelation = "sequence"
)

type NarrativeQueryCoverage string

const (
	NarrativeQueryCoverageBestSubset        NarrativeQueryCoverage = "best_subset"
	NarrativeQueryCoveragePerMatchingSource NarrativeQueryCoverage = "per_matching_source"
	NarrativeQueryCoverageExhaustive        NarrativeQueryCoverage = "exhaustive"
)

type NarrativeQuery struct {
	SchemaVersion int                    `json:"schemaVersion"`
	Clauses       []NarrativeQueryClause `json:"clauses,omitempty"`
	Coverage      NarrativeQueryCoverage `json:"coverage"`
	Ambiguous     bool                   `json:"ambiguous,omitempty"`
}

type NarrativeQueryClause struct {
	ID         string                   `json:"id"`
	Importance NarrativeQueryImportance `json:"importance"`
	Predicate  NarrativeQueryPredicate  `json:"predicate"`
	Terms      []string                 `json:"terms,omitempty"`
	MatchMode  NarrativeQueryMatchMode  `json:"matchMode"`
	Relation   NarrativeQueryRelation   `json:"relation"`
}

type NarrativeSelectionOutcome string

const (
	NarrativeSelectionOutcomeComplete    NarrativeSelectionOutcome = "complete_match"
	NarrativeSelectionOutcomePartial     NarrativeSelectionOutcome = "partial_match"
	NarrativeSelectionOutcomeNoMatch     NarrativeSelectionOutcome = "no_verifiable_match"
	NarrativeSelectionOutcomeUnavailable NarrativeSelectionOutcome = "query_unavailable"
)

type NarrativeClauseEvaluationState string

const (
	NarrativeClauseSatisfied   NarrativeClauseEvaluationState = "satisfied"
	NarrativeClauseViolated    NarrativeClauseEvaluationState = "violated"
	NarrativeClauseUnknown     NarrativeClauseEvaluationState = "unknown"
	NarrativeClauseUnsupported NarrativeClauseEvaluationState = "unsupported"
)

// NarrativeQueryEvaluation is the deterministic verdict for a candidate's
// evidence against a grounded narrative query. It deliberately omits raw text.
type NarrativeQueryEvaluation struct {
	ClauseEvaluations map[string]NarrativeClauseEvaluationState
	MustSatisfied     bool
	AvoidViolated     bool
	PreferSatisfied   bool
	HasUnsupported    bool
}

// EvaluateNarrativeQuery scores a bounded set of temporal evidence against a
// closed narrative query. It never invents evidence and treats non-detection as
// unknown, not proof of absence.
func EvaluateNarrativeQuery(evidence []NarrativeEvidence, q *NarrativeQuery) NarrativeQueryEvaluation {
	result := NarrativeQueryEvaluation{ClauseEvaluations: make(map[string]NarrativeClauseEvaluationState, len(q.Clauses))}
	if q == nil || len(q.Clauses) == 0 {
		result.MustSatisfied = true
		return result
	}
	mustCount, avoidCount, preferCount := 0, 0, 0
	for _, clause := range q.Clauses {
		switch clause.Importance {
		case NarrativeQueryMust:
			mustCount++
		case NarrativeQueryAvoid:
			avoidCount++
		case NarrativeQueryPrefer:
			preferCount++
		}
		state := evaluateNarrativeClause(evidence, clause)
		result.ClauseEvaluations[clause.ID] = state
		switch state {
		case NarrativeClauseUnsupported:
			result.HasUnsupported = true
		case NarrativeClauseSatisfied:
			if clause.Importance == NarrativeQueryPrefer {
				result.PreferSatisfied = true
			}
			if clause.Importance == NarrativeQueryMust {
				result.MustSatisfied = true
			}
		case NarrativeClauseViolated:
			if clause.Importance == NarrativeQueryAvoid {
				result.AvoidViolated = true
			}
		}
	}
	// A candidate is relevant only when every must clause is satisfied and no
	// avoid clause is violated. Prefer clauses only influence ranking.
	if mustCount > 0 {
		result.MustSatisfied = true
		for _, clause := range q.Clauses {
			if clause.Importance == NarrativeQueryMust && result.ClauseEvaluations[clause.ID] != NarrativeClauseSatisfied {
				result.MustSatisfied = false
				break
			}
		}
	} else {
		result.MustSatisfied = true
	}
	if avoidCount > 0 {
		for _, clause := range q.Clauses {
			if clause.Importance == NarrativeQueryAvoid && result.ClauseEvaluations[clause.ID] == NarrativeClauseViolated {
				result.AvoidViolated = true
				break
			}
		}
	}
	return result
}

// SelectionOutcomeFromEvaluation maps a query evaluation to a stable outcome
// code safe for persistence and telemetry.
func SelectionOutcomeFromEvaluation(ev NarrativeQueryEvaluation) NarrativeSelectionOutcome {
	if ev.HasUnsupported {
		return NarrativeSelectionOutcomeUnavailable
	}
	if !ev.MustSatisfied || ev.AvoidViolated {
		return NarrativeSelectionOutcomeNoMatch
	}
	if ev.PreferSatisfied {
		return NarrativeSelectionOutcomeComplete
	}
	if len(ev.ClauseEvaluations) == 0 {
		return NarrativeSelectionOutcomeUnavailable
	}
	return NarrativeSelectionOutcomePartial
}

func evaluateNarrativeClause(evidence []NarrativeEvidence, clause NarrativeQueryClause) NarrativeClauseEvaluationState {
	if clause.Predicate == NarrativeQueryUnsupported {
		return NarrativeClauseUnsupported
	}
	if len(clause.Terms) == 0 {
		return NarrativeClauseUnknown
	}
	// Avoid clauses are violated by any positive detection; absence remains unknown.
	if clause.Importance == NarrativeQueryAvoid {
		if anyTermMatched(evidence, clause) {
			return NarrativeClauseViolated
		}
		return NarrativeClauseUnknown
	}
	if clause.Relation == NarrativeQueryRelationSequence {
		if sequenceSatisfied(evidence, clause) {
			return NarrativeClauseSatisfied
		}
		return NarrativeClauseUnknown
	}
	// overlap relation (default): coexistence inside the candidate window.
	if clause.MatchMode == NarrativeQueryMatchAll {
		if allTermsMatched(evidence, clause) {
			return NarrativeClauseSatisfied
		}
		return NarrativeClauseUnknown
	}
	if anyTermMatched(evidence, clause) {
		return NarrativeClauseSatisfied
	}
	return NarrativeClauseUnknown
}

func allTermsMatched(evidence []NarrativeEvidence, clause NarrativeQueryClause) bool {
	matched := make(map[string]struct{}, len(clause.Terms))
	for _, term := range clause.Terms {
		for _, item := range evidence {
			if evidenceMatchesPredicate(item, clause.Predicate) && evidenceContainsTerm(item, term) {
				matched[term] = struct{}{}
				break
			}
		}
	}
	return len(matched) == len(clause.Terms)
}

func anyTermMatched(evidence []NarrativeEvidence, clause NarrativeQueryClause) bool {
	for _, item := range evidence {
		if !evidenceMatchesPredicate(item, clause.Predicate) {
			continue
		}
		for _, term := range clause.Terms {
			if evidenceContainsTerm(item, term) {
				return true
			}
		}
	}
	return false
}

func sequenceSatisfied(evidence []NarrativeEvidence, clause NarrativeQueryClause) bool {
	if len(clause.Terms) == 0 {
		return true
	}
	termIndex := 0
	var lastEnd int64 = -1
	for _, item := range evidence {
		if !evidenceMatchesPredicate(item, clause.Predicate) {
			continue
		}
		term := clause.Terms[termIndex]
		if !evidenceContainsTerm(item, term) {
			continue
		}
		if lastEnd >= 0 && item.StartMs < lastEnd {
			// For sequence alternatives, allow co-occurring terms to count as the same step.
			if termIndex > 0 {
				continue
			}
		}
		termIndex++
		lastEnd = item.EndMs
		if termIndex == len(clause.Terms) {
			return true
		}
	}
	return false
}

func evidenceMatchesPredicate(evidence NarrativeEvidence, predicate NarrativeQueryPredicate) bool {
	switch predicate {
	case NarrativeQueryVisibleEntity:
		return evidence.Kind == "label" || evidence.Kind == "object"
	case NarrativeQuerySpokenText:
		return evidence.Kind == "transcript"
	case NarrativeQueryVisibleText:
		return evidence.Kind == "ocr"
	default:
		return false
	}
}

func evidenceContainsTerm(evidence NarrativeEvidence, term string) bool {
	return strings.Contains(strings.ToLower(evidence.Text), strings.ToLower(term))
}

// Actionable reports whether the query contains at least one clause the backend
// can evaluate against temporal evidence. A query with only unsupported clauses
// should fall back to suggestion-based selection.
func (q NarrativeQuery) Actionable() bool {
	for _, clause := range q.Clauses {
		if clause.Predicate != NarrativeQueryUnsupported && len(clause.Terms) > 0 {
			return true
		}
	}
	return false
}

func (q NarrativeQuery) Validate() error {
	if q.SchemaVersion != NarrativeQuerySchemaVersion || len(q.Clauses) > NarrativeQueryMaxClauses || !q.Coverage.valid() {
		return fmt.Errorf("%w: invalid narrative query", ErrInvalidRequest)
	}
	seen := make(map[string]struct{}, len(q.Clauses))
	for _, clause := range q.Clauses {
		if strings.TrimSpace(clause.ID) == "" || len(clause.ID) > 40 || !clause.Importance.valid() || !clause.Predicate.valid() || !clause.MatchMode.valid() || !clause.Relation.valid() || len(clause.Terms) > NarrativeQueryMaxTerms {
			return fmt.Errorf("%w: invalid narrative query clause", ErrInvalidRequest)
		}
		if _, duplicate := seen[clause.ID]; duplicate {
			return fmt.Errorf("%w: duplicate narrative query clause", ErrInvalidRequest)
		}
		seen[clause.ID] = struct{}{}
		if clause.Predicate != NarrativeQueryUnsupported && len(clause.Terms) == 0 {
			return fmt.Errorf("%w: narrative query clause requires terms", ErrInvalidRequest)
		}
		for _, term := range clause.Terms {
			normalized, err := NormalizeNarrativeQueryTerm(term)
			if err != nil || normalized == "" || normalized != term {
				return fmt.Errorf("%w: invalid narrative query term", ErrInvalidRequest)
			}
		}
	}
	return nil
}

func NormalizeNarrativeQueryTerm(value string) (string, error) {
	if !utf8.ValidString(value) {
		return "", fmt.Errorf("%w: narrative query term must be valid UTF-8", ErrInvalidRequest)
	}
	value = strings.ToLower(strings.Join(strings.Fields(value), " "))
	if utf8.RuneCountInString(value) > NarrativeQueryTermMaxRunes {
		return "", fmt.Errorf("%w: narrative query term exceeds %d characters", ErrInvalidRequest, NarrativeQueryTermMaxRunes)
	}
	return value, nil
}

func (v NarrativeQueryImportance) valid() bool { return v == NarrativeQueryMust || v == NarrativeQueryPrefer || v == NarrativeQueryAvoid }
func (v NarrativeQueryPredicate) valid() bool { return v == NarrativeQueryVisibleEntity || v == NarrativeQuerySpokenText || v == NarrativeQueryVisibleText || v == NarrativeQueryUnsupported }
func (v NarrativeQueryMatchMode) valid() bool { return v == NarrativeQueryMatchAny || v == NarrativeQueryMatchAll }
func (v NarrativeQueryRelation) valid() bool { return v == NarrativeQueryRelationOverlap || v == NarrativeQueryRelationSequence }
func (v NarrativeQueryCoverage) valid() bool { return v == NarrativeQueryCoverageBestSubset || v == NarrativeQueryCoveragePerMatchingSource || v == NarrativeQueryCoverageExhaustive }

