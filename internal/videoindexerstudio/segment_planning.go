package videoindexerstudio

import (
	"fmt"
	"strings"
)

const NarrativeSegmentPlanningSchemaVersion = 1

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

type NarrativeSegmentCatalogItem struct {
	SegmentID      string   `json:"segmentId"`
	CandidateID    string   `json:"candidateId"`
	SourceAssetID  string   `json:"sourceAssetId"`
	AllowedStartMs int64    `json:"allowedStartMs"`
	AllowedEndMs   int64    `json:"allowedEndMs"`
	EvidenceIDs    []string `json:"evidenceIds"`
	Descriptor     string   `json:"descriptor,omitempty"`
}

type NarrativeSegmentPlanningRequest struct {
	SchemaVersion   int                           `json:"schemaVersion"`
	CompositionID   string                        `json:"compositionId"`
	NarrativeIntent string                        `json:"narrativeIntent,omitempty"`
	Profile         NarrativeIntentProfile        `json:"profile"`
	Catalog         []NarrativeSegmentCatalogItem `json:"catalog"`
}

type NarrativeSegmentPlanItem struct {
	SegmentID   string               `json:"segmentId"`
	StartMs     int64                `json:"startMs,omitempty"`
	EndMs       int64                `json:"endMs,omitempty"`
	Role        NarrativeSegmentRole `json:"role"`
	EvidenceIDs []string             `json:"evidenceIds"`
}

type NarrativeSegmentPlanningResponse struct {
	SchemaVersion int                        `json:"schemaVersion"`
	Segments      []NarrativeSegmentPlanItem `json:"segments"`
}

func (r NarrativeSegmentPlanningRequest) Validate() error {
	if r.SchemaVersion != NarrativeSegmentPlanningSchemaVersion || strings.TrimSpace(r.CompositionID) == "" || !r.Profile.Valid() || len(r.Catalog) == 0 || len(r.Catalog) > 48 {
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
		if item.Descriptor != "" {
			normalized, err := NormalizeNarrativeIntent(item.Descriptor)
			if err != nil || normalized != item.Descriptor {
				return fmt.Errorf("%w: invalid narrative segment descriptor", ErrInvalidRequest)
			}
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
		seenSegments[item.SegmentID] = struct{}{}
		seenCandidates[item.CandidateID] = struct{}{}
	}
	return nil
}

func (r NarrativeSegmentPlanningResponse) Validate() error {
	if r.SchemaVersion != NarrativeSegmentPlanningSchemaVersion || len(r.Segments) == 0 || len(r.Segments) > 48 {
		return fmt.Errorf("%w: invalid narrative segment plan", ErrInvalidRequest)
	}
	for _, item := range r.Segments {
		if strings.TrimSpace(item.SegmentID) == "" || !item.Role.Valid() || len(item.EvidenceIDs) == 0 {
			return fmt.Errorf("%w: invalid narrative segment plan item", ErrInvalidRequest)
		}
	}
	return nil
}
