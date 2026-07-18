package videoindexerstudio

import (
	"fmt"
	"strings"
)

const (
	NarrativeSegmentPlanningLegacySchemaVersion = 1
	NarrativeSegmentPlanningSchemaVersion       = 2
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
	NarrativeSegmentPlanningFallbackNone            NarrativeSegmentPlanningFallbackReason = ""
	NarrativeSegmentPlanningFallbackUnavailable     NarrativeSegmentPlanningFallbackReason = "planner_unavailable"
	NarrativeSegmentPlanningFallbackTimeout         NarrativeSegmentPlanningFallbackReason = "planner_timeout"
	NarrativeSegmentPlanningFallbackInvalidResponse NarrativeSegmentPlanningFallbackReason = "planner_invalid_response"
	NarrativeSegmentPlanningFallbackRequestFailed   NarrativeSegmentPlanningFallbackReason = "planner_request_failed"
	NarrativeSegmentPlanningFallbackCatalogInvalid  NarrativeSegmentPlanningFallbackReason = "planner_catalog_invalid"
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
