package backend

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/zecloud/ai-video-studio/internal/videoindexerstudio"
)

const (
	narrativeMaxVariantsPerCandidate = 1
	narrativePacingMinimumDurationMS = 1000
	narrativeEnergeticMaximumMS      = 6000
	narrativeCalmMaximumMS           = 15000
	narrativeContinuityMaximumMS     = 12000
)

// applyNarrativePacing derives at most one local, grounded range variant per
// source candidate. It never widens a source range and retains its provenance.
func applyNarrativePacing(candidates []compositionCandidate, profile videoindexerstudio.NarrativePacingProfile) ([]compositionCandidate, int) {
	if profile == videoindexerstudio.NarrativePacingProfileStandard || profile == "" {
		return candidates, 0
	}
	paced := make([]compositionCandidate, 0, len(candidates))
	variantCount := 0
	for _, candidate := range candidates {
		variant, changed := pacedCompositionCandidate(candidate, profile)
		if changed {
			variantCount++
		}
		paced = append(paced, variant)
	}
	return paced, variantCount
}

func pacedCompositionCandidate(candidate compositionCandidate, profile videoindexerstudio.NarrativePacingProfile) (compositionCandidate, bool) {
	clip := candidate.clip
	duration := clip.EndMs - clip.StartMs
	maximum := int64(0)
	switch profile {
	case videoindexerstudio.NarrativePacingProfileEnergeticShortForm:
		maximum = narrativeEnergeticMaximumMS
	case videoindexerstudio.NarrativePacingProfileCalmRecap:
		maximum = narrativeCalmMaximumMS
	case videoindexerstudio.NarrativePacingProfileChronologicalContinuity:
		maximum = narrativeContinuityMaximumMS
	default:
		return candidate, false
	}
	if duration < narrativePacingMinimumDurationMS || duration <= maximum {
		return candidate, false
	}
	// The fixed, start-anchored window makes local pacing reproducible and
	// avoids inventing a timestamp outside the already grounded suggestion.
	clip.EndMs = clip.StartMs + maximum
	clip.ID = stablePacingVariantID(candidate.clip.ID, profile, clip.StartMs, clip.EndMs)
	candidate.clip = clip
	return candidate, true
}

func stablePacingVariantID(sourceID string, profile videoindexerstudio.NarrativePacingProfile, startMS, endMS int64) string {
	input := strings.Join([]string{sourceID, string(profile), fmt.Sprintf("%d", startMS), fmt.Sprintf("%d", endMS)}, "\x1f")
	sum := sha256.Sum256([]byte(input))
	return "clip-" + hex.EncodeToString(sum[:16])
}

func sortPacedCompositionCandidates(candidates []compositionCandidate, profile videoindexerstudio.NarrativePacingProfile) {
	sort.SliceStable(candidates, func(i, j int) bool {
		if profile == videoindexerstudio.NarrativePacingProfileChronologicalContinuity && candidates[i].sourceStartMS != candidates[j].sourceStartMS {
			return candidates[i].sourceStartMS < candidates[j].sourceStartMS
		}
		if candidates[i].clip.Score != candidates[j].clip.Score {
			return candidates[i].clip.Score > candidates[j].clip.Score
		}
		if candidates[i].clip.SourceAssetID != candidates[j].clip.SourceAssetID {
			return candidates[i].clip.SourceAssetID < candidates[j].clip.SourceAssetID
		}
		if candidates[i].sourceStartMS != candidates[j].sourceStartMS {
			return candidates[i].sourceStartMS < candidates[j].sourceStartMS
		}
		return candidates[i].clip.ID < candidates[j].clip.ID
	})
}
