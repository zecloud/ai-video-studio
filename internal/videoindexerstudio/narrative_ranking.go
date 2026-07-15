package videoindexerstudio

import (
	"context"
	"fmt"
	"net/http"
	"strings"
)

// RankNarrative submits a bounded, grounded ordering request to the configured
// Video Indexer service. Callers must still validate the response before use.
func (c *Client) RankNarrative(ctx context.Context, request NarrativeRankingRequest) (*NarrativeRankingResponse, error) {
	if c == nil {
		return nil, fmt.Errorf("%w: nil client", ErrInvalidConfig)
	}
	if err := c.cfg.validate(); err != nil {
		return nil, err
	}
	if err := request.Validate(); err != nil {
		return nil, err
	}
	var response NarrativeRankingResponse
	if err := c.doJSON(ctx, http.MethodPost, "/api/v1/narrative-rankings", request, &response); err != nil {
		return nil, fmt.Errorf("rank narrative: %w", err)
	}
	if response.SchemaVersion != NarrativeRankingSchemaVersion || len(response.OrderedClips) == 0 {
		return nil, fmt.Errorf("%w: invalid narrative ranking response", ErrInvalidRequest)
	}
	return &response, nil
}

func (r NarrativeRankingRequest) Validate() error {
	if r.SchemaVersion != NarrativeRankingSchemaVersion || strings.TrimSpace(r.CompositionID) == "" || len(r.Candidates) == 0 {
		return fmt.Errorf("%w: invalid narrative ranking request", ErrInvalidRequest)
	}
	knownEvidence := make(map[string]struct{}, len(r.Evidence))
	for _, evidence := range r.Evidence {
		if strings.TrimSpace(evidence.ID) == "" || strings.TrimSpace(evidence.SourceAssetID) == "" || evidence.StartMs < 0 || evidence.EndMs < evidence.StartMs {
			return fmt.Errorf("%w: invalid narrative evidence", ErrInvalidRequest)
		}
		if _, duplicate := knownEvidence[evidence.ID]; duplicate {
			return fmt.Errorf("%w: duplicate narrative evidence", ErrInvalidRequest)
		}
		knownEvidence[evidence.ID] = struct{}{}
	}
	knownCandidates := make(map[string]struct{}, len(r.Candidates))
	for _, candidate := range r.Candidates {
		if strings.TrimSpace(candidate.ID) == "" || strings.TrimSpace(candidate.SourceAssetID) == "" || candidate.StartMs < 0 || candidate.EndMs <= candidate.StartMs {
			return fmt.Errorf("%w: invalid narrative candidate", ErrInvalidRequest)
		}
		if _, duplicate := knownCandidates[candidate.ID]; duplicate {
			return fmt.Errorf("%w: duplicate narrative candidate", ErrInvalidRequest)
		}
		knownCandidates[candidate.ID] = struct{}{}
		for _, evidenceID := range candidate.EvidenceIDs {
			if _, ok := knownEvidence[evidenceID]; !ok {
				return fmt.Errorf("%w: candidate references unknown evidence", ErrInvalidRequest)
			}
		}
	}
	return nil
}
