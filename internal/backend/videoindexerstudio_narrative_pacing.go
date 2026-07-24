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
	narrativeCinematicMaximumMS      = 10000
	narrativeTutorialMaximumMS       = 14000
	narrativeHighlightMaximumMS      = 7000
	narrativeStoryMaximumMS          = 11000
	narrativeInterviewMaximumMS      = 15000
	narrativeProductMaximumMS        = 8000
)

// applyNarrativePacing derives one reproducible, source-range-contained variant.
// Profile bounds deliberately trade pace against coverage without creating fragments.
func applyNarrativePacing(candidates []compositionCandidate, profile videoindexerstudio.NarrativePacingProfile) ([]compositionCandidate, int) {
	if profile == videoindexerstudio.NarrativePacingProfileStandard || profile == "" {
		return candidates, 0
	}
	paced := make([]compositionCandidate, 0, len(candidates))
	variants := 0
	for _, candidate := range candidates {
		variant, changed := pacedCompositionCandidate(candidate, profile)
		if changed {
			variants++
		}
		paced = append(paced, variant)
	}
	return paced, variants
}

func narrativePacingMaximum(profile videoindexerstudio.NarrativePacingProfile) int64 {
	switch profile {
	case videoindexerstudio.NarrativePacingProfileEnergeticShortForm, videoindexerstudio.NarrativePacingProfileSocialShortForm:
		return narrativeEnergeticMaximumMS
	case videoindexerstudio.NarrativePacingProfileCalmRecap, videoindexerstudio.NarrativePacingProfileRecap, videoindexerstudio.NarrativePacingProfileTravel:
		return narrativeCalmMaximumMS
	case videoindexerstudio.NarrativePacingProfileChronologicalContinuity:
		return narrativeContinuityMaximumMS
	case videoindexerstudio.NarrativePacingProfileCinematic:
		return narrativeCinematicMaximumMS
	case videoindexerstudio.NarrativePacingProfileTutorial:
		return narrativeTutorialMaximumMS
	case videoindexerstudio.NarrativePacingProfileHighlightReel:
		return narrativeHighlightMaximumMS
	case videoindexerstudio.NarrativePacingProfileStorytelling:
		return narrativeStoryMaximumMS
	case videoindexerstudio.NarrativePacingProfileInterview:
		return narrativeInterviewMaximumMS
	case videoindexerstudio.NarrativePacingProfileProductShowcase:
		return narrativeProductMaximumMS
	default:
		return 0
	}
}

func pacedCompositionCandidate(candidate compositionCandidate, profile videoindexerstudio.NarrativePacingProfile) (compositionCandidate, bool) {
	clip := candidate.clip
	duration := clip.EndMs - clip.StartMs
	maximum := narrativePacingMaximum(profile)
	if maximum == 0 || duration < narrativePacingMinimumDurationMS || duration <= maximum {
		return candidate, false
	}
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
		continuity := profile == videoindexerstudio.NarrativePacingProfileChronologicalContinuity || profile == videoindexerstudio.NarrativePacingProfileTutorial
		if continuity && candidates[i].sourceStartMS != candidates[j].sourceStartMS {
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
