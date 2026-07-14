package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/zecloud/ai-video-studio/internal/videoindexerstudio"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

func buildTimelineDrafts(ctx context.Context, job JobDocument, plan EditPlan, promptVersion string, obs *Observability) (drafts []videoindexerstudio.TimelineDraft, err error) {
	sourceAssetID := strings.TrimSpace(job.OneDriveItemID)
	if sourceAssetID == "" {
		return nil, fmt.Errorf("oneDriveItemId is required to build timeline drafts")
	}
	if planAssetID := strings.TrimSpace(plan.AssetID); planAssetID != sourceAssetID {
		return nil, fmt.Errorf("plan asset id %q does not match job oneDrive item id %q", planAssetID, sourceAssetID)
	}
	promptVersion = strings.TrimSpace(promptVersion)
	if promptVersion == "" {
		return nil, fmt.Errorf("prompt version is required to build timeline drafts")
	}
	if len(plan.Suggestions) == 0 {
		return nil, newServiceError(http.StatusUnprocessableEntity, "no_valid_timeline_drafts", "no suggestions were available for timeline draft generation", false)
	}
	start := time.Now()
	var span trace.Span
	if obs != nil {
		ctx, span = obs.StartSpan(ctx, "timeline.build", attribute.String("stage", "timeline.build"))
		defer func() {
			obs.FinishSpan(ctx, span, "timeline.build", start, []attribute.KeyValue{attribute.String("stage", "timeline.build")}, err)
		}()
	}

	drafts = make([]videoindexerstudio.TimelineDraft, 0, len(plan.Suggestions))
	for _, suggestion := range plan.Suggestions {
		draft, draftErr := buildTimelineDraftFromSuggestion(job.ID, sourceAssetID, promptVersion, suggestion)
		if draftErr != nil {
			continue
		}
		drafts = append(drafts, draft)
	}
	if len(drafts) == 0 {
		return nil, newServiceError(http.StatusUnprocessableEntity, "no_valid_timeline_drafts", "no valid timeline drafts could be generated from the edit plan", false)
	}
	if obs != nil {
		clipCount := 0
		for _, draft := range drafts {
			clipCount += len(draft.PrimaryVideoTrack.Clips)
		}
		obs.RecordTimelineClips(ctx, clipCount, attribute.String("prompt_version", promptVersion))
	}
	return drafts, nil
}

func buildTimelineDraftFromSuggestion(originJobID, sourceAssetID, promptVersion string, suggestion EditSuggestion) (videoindexerstudio.TimelineDraft, error) {
	originJobID = strings.TrimSpace(originJobID)
	sourceAssetID = strings.TrimSpace(sourceAssetID)
	promptVersion = strings.TrimSpace(promptVersion)
	suggestionID := strings.TrimSpace(suggestion.ID)
	if originJobID == "" {
		return videoindexerstudio.TimelineDraft{}, fmt.Errorf("origin job id is required")
	}
	if sourceAssetID == "" {
		return videoindexerstudio.TimelineDraft{}, fmt.Errorf("source asset id is required")
	}
	if promptVersion == "" {
		return videoindexerstudio.TimelineDraft{}, fmt.Errorf("prompt version is required")
	}
	if suggestionID == "" {
		return videoindexerstudio.TimelineDraft{}, fmt.Errorf("suggestion id is required")
	}
	if len(suggestion.Clips) == 0 {
		return videoindexerstudio.TimelineDraft{}, fmt.Errorf("suggestion %q has no clips", suggestionID)
	}

	clips := make([]videoindexerstudio.TimelineClip, 0, len(suggestion.Clips))
	timelineStartMS := int64(0)
	for idx, clip := range suggestion.Clips {
		clipSourceAssetID := strings.TrimSpace(clip.SourceAssetID)
		if clipSourceAssetID == "" {
			clipSourceAssetID = sourceAssetID
		}
		inMS := clip.StartMs
		outMS := clip.EndMs
		if inMS < 0 {
			return videoindexerstudio.TimelineDraft{}, fmt.Errorf("suggestion %q clip %d inMs must be non-negative", suggestionID, idx)
		}
		if outMS <= inMS {
			return videoindexerstudio.TimelineDraft{}, fmt.Errorf("suggestion %q clip %d outMs must be greater than inMs", suggestionID, idx)
		}
		durationMS := outMS - inMS
		clips = append(clips, videoindexerstudio.TimelineClip{
			ID:              stableTimelineClipID(originJobID, suggestionID, idx, clip),
			SourceAssetID:   clipSourceAssetID,
			InMS:            inMS,
			OutMS:           outMS,
			TimelineStartMS: timelineStartMS,
			DurationMS:      durationMS,
			Transition: videoindexerstudio.TimelineTransition{
				Kind: videoindexerstudio.TimelineTransitionKindCut,
			},
		})
		timelineStartMS += durationMS
	}

	draft := videoindexerstudio.TimelineDraft{
		SchemaVersion: videoindexerstudio.TimelineDraftSchemaVersion,
		OriginJobID:   originJobID,
		SuggestionID:  suggestionID,
		PromptVersion: promptVersion,
		PrimaryVideoTrack: videoindexerstudio.TimelineTrack{
			ID:    "primary-video",
			Kind:  videoindexerstudio.TimelineTrackKindVideo,
			Clips: clips,
		},
	}
	if err := draft.Validate(); err != nil {
		return videoindexerstudio.TimelineDraft{}, err
	}
	return draft, nil
}

func stableTimelineClipID(originJobID, suggestionID string, index int, clip SuggestedClip) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		strings.TrimSpace(originJobID),
		strings.TrimSpace(suggestionID),
		strings.TrimSpace(clip.ID),
		strings.TrimSpace(clip.SourceAssetID),
		fmt.Sprintf("%d", index),
		fmt.Sprintf("%d", clip.StartMs),
		fmt.Sprintf("%d", clip.EndMs),
	}, "\x1f")))
	return "clip-" + hex.EncodeToString(sum[:8])
}
