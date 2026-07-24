package videoindexerstudio

import (
	"fmt"
	"strings"
)

const (
	NarrativeSegmentPlanningLegacySchemaVersion = 1
	NarrativeSegmentPlanningSchemaVersion       = 2
	NarrativeSegmentPlanningMaximumAnchorMS     = 15_000
)

type NarrativeSegmentRole string

const (
	NarrativeSegmentRoleHook        NarrativeSegmentRole = "hook"
	NarrativeSegmentRoleContext     NarrativeSegmentRole = "context"
	NarrativeSegmentRoleDevelopment NarrativeSegmentRole = "development"
	NarrativeSegmentRolePayoff      NarrativeSegmentRole = "payoff"
	NarrativeSegmentRoleOutro       NarrativeSegmentRole = "outro"
)

func (r NarrativeSegmentRole) Valid() bool {
	return r == NarrativeSegmentRoleHook || r == NarrativeSegmentRoleContext || r == NarrativeSegmentRoleDevelopment || r == NarrativeSegmentRolePayoff || r == NarrativeSegmentRoleOutro
}

type NarrativeSegmentAnchorMode string

const (
	NarrativeSegmentAnchorModeSimultaneous NarrativeSegmentAnchorMode = "simultaneous"
	NarrativeSegmentAnchorModeSequence     NarrativeSegmentAnchorMode = "sequence"
)

func (m NarrativeSegmentAnchorMode) Valid() bool {
	return m == NarrativeSegmentAnchorModeSimultaneous || m == NarrativeSegmentAnchorModeSequence
}

type NarrativeSegmentPlanningMode string

const (
	NarrativeSegmentPlanningModeFoundryStructured NarrativeSegmentPlanningMode = "foundry_structured"
	NarrativeSegmentPlanningModeDeterministic     NarrativeSegmentPlanningMode = "deterministic_local"
)

type NarrativeSegmentPlanningFallbackReason string

const (
	NarrativeSegmentPlanningFallbackNone              NarrativeSegmentPlanningFallbackReason = ""
	NarrativeSegmentPlanningFallbackUnavailable       NarrativeSegmentPlanningFallbackReason = "planner_unavailable"
	NarrativeSegmentPlanningFallbackTimeout           NarrativeSegmentPlanningFallbackReason = "planner_timeout"
	NarrativeSegmentPlanningFallbackInvalidResponse   NarrativeSegmentPlanningFallbackReason = "planner_invalid_response"
	NarrativeSegmentPlanningFallbackRequestFailed     NarrativeSegmentPlanningFallbackReason = "planner_request_failed"
	NarrativeSegmentPlanningFallbackCatalogInvalid    NarrativeSegmentPlanningFallbackReason = "planner_catalog_invalid"
	NarrativeSegmentPlanningFallbackNoVerifiableMatch NarrativeSegmentPlanningFallbackReason = "no_verifiable_match"
)

type NarrativeSegmentEvidence struct {
	EvidenceID string `json:"evidenceId"`
	Kind       string `json:"kind"`
	StartMs    int64  `json:"startMs"`
	EndMs      int64  `json:"endMs"`
	Descriptor string `json:"descriptor,omitempty"`
}

type NarrativeSegmentCatalogItem struct {
	SegmentID      string                     `json:"segmentId"`
	CandidateID    string                     `json:"candidateId"`
	SourceAssetID  string                     `json:"sourceAssetId"`
	AllowedStartMs int64                      `json:"allowedStartMs"`
	AllowedEndMs   int64                      `json:"allowedEndMs"`
	EvidenceIDs    []string                   `json:"evidenceIds"`
	Evidence       []NarrativeSegmentEvidence `json:"evidence,omitempty"`
	Descriptor     string                     `json:"descriptor,omitempty"`
}

type NarrativeSegmentPlanningRequest struct {
	SchemaVersion   int                           `json:"schemaVersion"`
	CompositionID   string                        `json:"compositionId"`
	NarrativeIntent string                        `json:"narrativeIntent,omitempty"`
	Profile         NarrativeIntentProfile        `json:"profile"`
	Catalog         []NarrativeSegmentCatalogItem `json:"catalog"`
}

type NarrativeSegmentPlanItem struct {
	SegmentID         string                     `json:"segmentId"`
	StartMs           int64                      `json:"startMs,omitempty"`
	EndMs             int64                      `json:"endMs,omitempty"`
	Role              NarrativeSegmentRole       `json:"role"`
	EvidenceIDs       []string                   `json:"evidenceIds,omitempty"`
	AnchorEvidenceIDs []string                   `json:"anchorEvidenceIds,omitempty"`
	AnchorMode        NarrativeSegmentAnchorMode `json:"anchorMode,omitempty"`
}

type NarrativeSegmentPlanningResponse struct {
	SchemaVersion int                        `json:"schemaVersion"`
	Segments      []NarrativeSegmentPlanItem `json:"segments"`
}

func (r NarrativeSegmentPlanningRequest) Validate() error {
	if (r.SchemaVersion != NarrativeSegmentPlanningLegacySchemaVersion && r.SchemaVersion != NarrativeSegmentPlanningSchemaVersion) || strings.TrimSpace(r.CompositionID) == "" || !r.Profile.Valid() || len(r.Catalog) == 0 || len(r.Catalog) > 48 {
		return fmt.Errorf("%w: invalid narrative segment planning request", ErrInvalidRequest)
	}
	if normalized, err := NormalizeNarrativeIntent(r.NarrativeIntent); err != nil || normalized != r.NarrativeIntent {
		return fmt.Errorf("%w: invalid narrative intent", ErrInvalidRequest)
	}
	seenSegments := map[string]struct{}{}
	seenCandidates := map[string]struct{}{}
	for _, item := range r.Catalog {
		if strings.TrimSpace(item.SegmentID) == "" || strings.TrimSpace(item.CandidateID) == "" || strings.TrimSpace(item.SourceAssetID) == "" || item.AllowedStartMs < 0 || item.AllowedEndMs <= item.AllowedStartMs || len(item.EvidenceIDs) == 0 {
			return fmt.Errorf("%w: invalid narrative segment catalog item", ErrInvalidRequest)
		}
		if _, exists := seenSegments[item.SegmentID]; exists {
			return fmt.Errorf("%w: duplicate segment catalog item", ErrInvalidRequest)
		}
		if _, exists := seenCandidates[item.CandidateID]; exists {
			return fmt.Errorf("%w: duplicate narrative segment candidate", ErrInvalidRequest)
		}
		if item.Descriptor != "" && !validNarrativeSegmentText(item.Descriptor) {
			return fmt.Errorf("%w: invalid narrative segment descriptor", ErrInvalidRequest)
		}
		evidenceIDs := map[string]struct{}{}
		for _, evidenceID := range item.EvidenceIDs {
			if strings.TrimSpace(evidenceID) == "" {
				return fmt.Errorf("%w: invalid narrative segment evidence", ErrInvalidRequest)
			}
			if _, exists := evidenceIDs[evidenceID]; exists {
				return fmt.Errorf("%w: duplicate narrative segment evidence", ErrInvalidRequest)
			}
			evidenceIDs[evidenceID] = struct{}{}
		}
		if r.SchemaVersion == NarrativeSegmentPlanningSchemaVersion {
			if len(item.Evidence) != len(item.EvidenceIDs) {
				return fmt.Errorf("%w: incomplete temporal narrative evidence", ErrInvalidRequest)
			}
			seenTemporal := map[string]struct{}{}
			for _, evidence := range item.Evidence {
				_, known := evidenceIDs[evidence.EvidenceID]
				if !known || strings.TrimSpace(evidence.Kind) == "" || evidence.StartMs < item.AllowedStartMs || evidence.EndMs > item.AllowedEndMs || evidence.EndMs <= evidence.StartMs || (evidence.Descriptor != "" && !validNarrativeSegmentText(evidence.Descriptor)) {
					return fmt.Errorf("%w: invalid temporal narrative evidence", ErrInvalidRequest)
				}
				if _, duplicate := seenTemporal[evidence.EvidenceID]; duplicate {
					return fmt.Errorf("%w: duplicate temporal narrative evidence", ErrInvalidRequest)
				}
				seenTemporal[evidence.EvidenceID] = struct{}{}
			}
		}
		seenSegments[item.SegmentID] = struct{}{}
		seenCandidates[item.CandidateID] = struct{}{}
	}
	return nil
}

func validNarrativeSegmentText(value string) bool {
	normalized, err := NormalizeNarrativeIntent(value)
	return err == nil && normalized == value
}

func (r NarrativeSegmentPlanningResponse) Validate() error {
	if (r.SchemaVersion != NarrativeSegmentPlanningLegacySchemaVersion && r.SchemaVersion != NarrativeSegmentPlanningSchemaVersion) || len(r.Segments) == 0 || len(r.Segments) > 48 {
		return fmt.Errorf("%w: invalid narrative segment plan", ErrInvalidRequest)
	}
	for _, item := range r.Segments {
		if strings.TrimSpace(item.SegmentID) == "" || !item.Role.Valid() {
			return fmt.Errorf("%w: invalid narrative segment plan item", ErrInvalidRequest)
		}
		if r.SchemaVersion == NarrativeSegmentPlanningSchemaVersion {
			if len(item.AnchorEvidenceIDs) == 0 || !item.AnchorMode.Valid() || item.StartMs != 0 || item.EndMs != 0 || len(item.EvidenceIDs) != 0 {
				return fmt.Errorf("%w: invalid anchored narrative segment plan item", ErrInvalidRequest)
			}
		} else if len(item.EvidenceIDs) == 0 {
			return fmt.Errorf("%w: invalid legacy narrative segment plan item", ErrInvalidRequest)
		}
	}
	return nil
}

// ValidateAgainst verifies the response against the exact bounded catalog that
// was supplied to the planner. It intentionally does not infer or repair IDs.
func (r NarrativeSegmentPlanningResponse) ValidateAgainst(request NarrativeSegmentPlanningRequest) error {
	if err := request.Validate(); err != nil {
		return err
	}
	if err := r.Validate(); err != nil {
		return err
	}
	if r.SchemaVersion != request.SchemaVersion {
		return fmt.Errorf("%w: narrative segment plan schema mismatch", ErrInvalidRequest)
	}
	catalog := make(map[string]NarrativeSegmentCatalogItem, len(request.Catalog))
	for _, item := range request.Catalog {
		catalog[item.SegmentID] = item
	}
	seenSegments := make(map[string]struct{}, len(r.Segments))
	for _, planned := range r.Segments {
		item, known := catalog[planned.SegmentID]
		if !known {
			return fmt.Errorf("%w: narrative segment plan references unknown segment", ErrInvalidRequest)
		}
		if _, duplicate := seenSegments[planned.SegmentID]; duplicate {
			return fmt.Errorf("%w: narrative segment plan duplicates segment", ErrInvalidRequest)
		}
		seenSegments[planned.SegmentID] = struct{}{}
		if request.SchemaVersion == NarrativeSegmentPlanningLegacySchemaVersion {
			if !knownNarrativeSegmentEvidence(planned.EvidenceIDs, item.EvidenceIDs) {
				return fmt.Errorf("%w: narrative segment plan references unknown evidence", ErrInvalidRequest)
			}
			continue
		}
		start, end, err := narrativeSegmentProtectedAnchor(item, planned.AnchorEvidenceIDs, planned.AnchorMode)
		if err != nil || start < item.AllowedStartMs || end > item.AllowedEndMs || end-start > NarrativeSegmentPlanningMaximumAnchorMS {
			return fmt.Errorf("%w: narrative segment plan has invalid protected anchor", ErrInvalidRequest)
		}
	}
	return nil
}

func knownNarrativeSegmentEvidence(actual, allowed []string) bool {
	if len(actual) == 0 {
		return false
	}
	known := make(map[string]struct{}, len(allowed))
	for _, id := range allowed {
		known[id] = struct{}{}
	}
	seen := make(map[string]struct{}, len(actual))
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

func narrativeSegmentProtectedAnchor(item NarrativeSegmentCatalogItem, ids []string, mode NarrativeSegmentAnchorMode) (int64, int64, error) {
	if !mode.Valid() || !knownNarrativeSegmentEvidence(ids, item.EvidenceIDs) {
		return 0, 0, fmt.Errorf("%w: invalid narrative segment anchor evidence", ErrInvalidRequest)
	}
	byID := make(map[string]NarrativeSegmentEvidence, len(item.Evidence))
	for _, evidence := range item.Evidence {
		byID[evidence.EvidenceID] = evidence
	}
	first, ok := byID[ids[0]]
	if !ok {
		return 0, 0, fmt.Errorf("%w: unknown narrative segment anchor evidence", ErrInvalidRequest)
	}
	start, end := first.StartMs, first.EndMs
	for _, id := range ids[1:] {
		evidence, ok := byID[id]
		if !ok {
			return 0, 0, fmt.Errorf("%w: unknown narrative segment anchor evidence", ErrInvalidRequest)
		}
		if mode == NarrativeSegmentAnchorModeSimultaneous {
			if evidence.StartMs > start {
				start = evidence.StartMs
			}
			if evidence.EndMs < end {
				end = evidence.EndMs
			}
		} else {
			if evidence.StartMs < start {
				start = evidence.StartMs
			}
			if evidence.EndMs > end {
				end = evidence.EndMs
			}
		}
	}
	if end <= start {
		return 0, 0, fmt.Errorf("%w: empty narrative segment anchor", ErrInvalidRequest)
	}
	return start, end, nil
}
