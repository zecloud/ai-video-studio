package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/zecloud/ai-video-studio/internal/videoindexerstudio"
	"go.opentelemetry.io/otel/attribute"
)

type VideoNormalizer interface {
	Normalize(ctx context.Context, job JobDocument, asset StagedAsset, readURL string, index VideoIndexData) (VideoIndexResult, error)
}

type AzureVideoIndexerPipeline struct {
	client             VideoIndexerAPI
	normalizer         VideoNormalizer
	planner            EditPlanner
	clock              Clock
	evidenceLimitBytes int
	obs                *Observability
}

func NewAzureVideoIndexerPipeline(client VideoIndexerAPI, normalizer VideoNormalizer, planner EditPlanner, clock Clock) *AzureVideoIndexerPipeline {
	if clock == nil {
		clock = realClock{}
	}
	return &AzureVideoIndexerPipeline{
		client:             client,
		normalizer:         normalizer,
		planner:            planner,
		clock:              clock,
		evidenceLimitBytes: defaultEditPlannerEvidenceLimit,
	}
}

func (p *AzureVideoIndexerPipeline) Process(ctx context.Context, job JobDocument, asset StagedAsset, readURL string, progress JobProgressRecorder) (PipelineOutcome, error) {
	if p == nil || p.client == nil {
		return PipelineOutcome{}, newServiceError(503, "pipeline_unavailable", "video indexer client is not configured", true)
	}
	if progress == nil {
		return PipelineOutcome{}, fmt.Errorf("job progress recorder is required")
	}
	if err := ctx.Err(); err != nil {
		return PipelineOutcome{}, err
	}

	current := job
	if current.VideoIndexerVideoID == "" {
		uploadCtx := ctx
		if p.obs != nil {
			uploadCtx = p.obs.ContextWithAttrs(ctx, attribute.String("job_id", job.ID), attribute.String("stage", "vi.submit"))
		}
		_, err := progress.UpdateProgress(ctx, job.ID, JobStatusSubmitting, func(doc *JobDocument) {
			now := p.clock.Now()
			doc.VideoIndexerState = "submitting"
			doc.Checkpoints = append(doc.Checkpoints, JobCheckpoint{
				Stage:  "submitting",
				At:     now,
				Detail: "video upload submitted",
			})
		})
		if err != nil {
			return PipelineOutcome{}, err
		}

		videoID, err := p.client.UploadVideoURL(uploadCtx, readURL, current.SourceName, job.ID)
		if err != nil {
			return PipelineOutcome{}, err
		}
		current.VideoIndexerVideoID = videoID
		_, err = progress.UpdateProgress(ctx, job.ID, JobStatusIndexing, func(doc *JobDocument) {
			now := p.clock.Now()
			doc.VideoIndexerVideoID = videoID
			doc.VideoIndexerState = "indexing"
			doc.Checkpoints = append(doc.Checkpoints, JobCheckpoint{
				Stage:   "indexing",
				At:      now,
				VideoID: videoID,
				Detail:  "video upload accepted",
			})
		})
		if err != nil {
			return PipelineOutcome{}, err
		}
	}

	pollCtx := ctx
	if p.obs != nil {
		pollCtx = p.obs.ContextWithAttrs(ctx, attribute.String("job_id", job.ID), attribute.String("video_id", current.VideoIndexerVideoID), attribute.String("stage", "vi.poll"))
	}
	index, err := p.client.PollVideoIndex(pollCtx, current.VideoIndexerVideoID, p.client.PollTimeout())
	if err != nil {
		return PipelineOutcome{}, err
	}

	_, err = progress.UpdateProgress(ctx, job.ID, JobStatusNormalizing, func(doc *JobDocument) {
		now := p.clock.Now()
		doc.VideoIndexerVideoID = current.VideoIndexerVideoID
		doc.VideoIndexerState = index.State()
		doc.Checkpoints = append(doc.Checkpoints, JobCheckpoint{
			Stage:   "normalizing",
			At:      now,
			VideoID: current.VideoIndexerVideoID,
			State:   index.State(),
			Detail:  "raw insights retrieved",
		})
	})
	if err != nil {
		return PipelineOutcome{}, err
	}

	if p.normalizer != nil {
		normalizeCtx := ctx
		if p.obs != nil {
			normalizeCtx = p.obs.ContextWithAttrs(ctx, attribute.String("job_id", job.ID), attribute.String("video_id", current.VideoIndexerVideoID), attribute.String("stage", "vi.normalize"))
		}
		result, err := p.normalizer.Normalize(normalizeCtx, current, asset, readURL, index)
		if err != nil {
			return PipelineOutcome{}, err
		}
		_, err = progress.UpdateProgress(ctx, job.ID, JobStatusNormalizing, func(doc *JobDocument) {
			now := p.clock.Now()
			doc.VideoIndexerVideoID = current.VideoIndexerVideoID
			doc.VideoIndexerState = index.State()
			doc.VideoIndexResult = &result
			doc.Checkpoints = append(doc.Checkpoints, JobCheckpoint{
				Stage:    "normalizing",
				At:       now,
				VideoID:  current.VideoIndexerVideoID,
				State:    index.State(),
				Detail:   "video index normalized",
				Metadata: mustJSON(videoIndexCheckpointSummaryFromResult(result)),
			})
		})
		if err != nil {
			return PipelineOutcome{}, err
		}
		if p.planner == nil {
			return PipelineOutcome{}, newServiceError(503, "edit_planner_unavailable", "edit planner is not configured", true)
		}
		promptCtx := ctx
		if p.obs != nil {
			promptCtx = p.obs.ContextWithAttrs(ctx, attribute.String("job_id", job.ID), attribute.String("video_id", current.VideoIndexerVideoID), attribute.String("asset_id", current.OneDriveItemID), attribute.String("prompt_version", editPlannerInstructionsVersion), attribute.String("stage", "agent.run"))
		}
		prompt, evidenceIndex, err := buildEditPlannerPrompt(promptCtx, current, asset, result, p.evidenceLimitBytes, p.obs)
		if err != nil {
			return PipelineOutcome{}, err
		}
		_, err = progress.UpdateProgress(ctx, job.ID, JobStatusGenerating, func(doc *JobDocument) {
			now := p.clock.Now()
			doc.VideoIndexerVideoID = current.VideoIndexerVideoID
			doc.VideoIndexerState = index.State()
			doc.VideoIndexResult = &result
			doc.EditPlan = nil
			doc.TimelineDrafts = nil
			doc.Checkpoints = append(doc.Checkpoints, JobCheckpoint{
				Stage:   "generating",
				At:      now,
				VideoID: current.VideoIndexerVideoID,
				State:   index.State(),
				Detail:  "edit plan generation started",
			})
		})
		if err != nil {
			return PipelineOutcome{}, err
		}
		plan, err := p.planner.Plan(promptCtx, prompt)
		if err != nil {
			return PipelineOutcome{}, err
		}
		validatedPlan, err := validateEditPlan(plan, evidenceIndex)
		if err != nil {
			return PipelineOutcome{}, err
		}
		_, err = progress.UpdateProgress(ctx, job.ID, JobStatusGenerating, func(doc *JobDocument) {
			now := p.clock.Now()
			doc.VideoIndexerVideoID = current.VideoIndexerVideoID
			doc.VideoIndexerState = index.State()
			doc.VideoIndexResult = &result
			doc.EditPlan = &validatedPlan
			doc.Checkpoints = append(doc.Checkpoints, JobCheckpoint{
				Stage:    "generating",
				At:       now,
				VideoID:  current.VideoIndexerVideoID,
				State:    index.State(),
				Detail:   "edit plan generated",
				Metadata: mustJSON(editPlanCheckpointSummaryFromPlan(validatedPlan)),
			})
		})
		if err != nil {
			return PipelineOutcome{}, err
		}
		_, err = progress.UpdateProgress(ctx, job.ID, JobStatusBuildingTimeline, func(doc *JobDocument) {
			now := p.clock.Now()
			doc.VideoIndexerVideoID = current.VideoIndexerVideoID
			doc.VideoIndexerState = index.State()
			doc.VideoIndexResult = &result
			doc.EditPlan = &validatedPlan
			doc.TimelineDrafts = nil
			doc.Checkpoints = append(doc.Checkpoints, JobCheckpoint{
				Stage:   "building_timeline",
				At:      now,
				VideoID: current.VideoIndexerVideoID,
				State:   index.State(),
				Detail:  "timeline draft generation started",
			})
		})
		if err != nil {
			return PipelineOutcome{}, err
		}
		timelineCtx := ctx
		if p.obs != nil {
			timelineCtx = p.obs.ContextWithAttrs(ctx, attribute.String("job_id", job.ID), attribute.String("video_id", current.VideoIndexerVideoID), attribute.String("asset_id", current.OneDriveItemID), attribute.String("prompt_version", editPlannerInstructionsVersion), attribute.String("stage", "timeline.build"))
		}
		drafts, err := buildTimelineDrafts(timelineCtx, current, validatedPlan, editPlannerInstructionsVersion, p.obs)
		if err != nil {
			return PipelineOutcome{}, err
		}
		_, err = progress.UpdateProgress(ctx, job.ID, JobStatusBuildingTimeline, func(doc *JobDocument) {
			now := p.clock.Now()
			doc.VideoIndexerVideoID = current.VideoIndexerVideoID
			doc.VideoIndexerState = index.State()
			doc.VideoIndexResult = &result
			doc.EditPlan = &validatedPlan
			doc.TimelineDrafts = append([]videoindexerstudio.TimelineDraft(nil), drafts...)
			doc.Checkpoints = append(doc.Checkpoints, JobCheckpoint{
				Stage:   "building_timeline",
				At:      now,
				VideoID: current.VideoIndexerVideoID,
				State:   index.State(),
				Detail:  "timeline drafts generated",
				Metadata: mustJSON(timelineDraftCheckpointSummaryFromDrafts(
					len(validatedPlan.Suggestions),
					len(drafts),
				)),
			})
		})
		if err != nil {
			return PipelineOutcome{}, err
		}
		return PipelineOutcome{Kind: PipelineOutcomeCompleted, VideoIndex: index}, nil
	}
	return PipelineOutcome{Kind: PipelineOutcomePendingNormalization, VideoIndex: index}, nil
}

type editPlanCheckpointSummary struct {
	Highlights  int `json:"highlights,omitempty"`
	Suggestions int `json:"suggestions,omitempty"`
	Clips       int `json:"clips,omitempty"`
}

type timelineDraftCheckpointSummary struct {
	Suggestions int `json:"suggestions,omitempty"`
	Drafts      int `json:"drafts,omitempty"`
	Skipped     int `json:"skipped,omitempty"`
}

func editPlanCheckpointSummaryFromPlan(plan EditPlan) editPlanCheckpointSummary {
	clipCount := 0
	for _, suggestion := range plan.Suggestions {
		clipCount += len(suggestion.Clips)
	}
	return editPlanCheckpointSummary{
		Highlights:  len(plan.Highlights),
		Suggestions: len(plan.Suggestions),
		Clips:       clipCount,
	}
}

func timelineDraftCheckpointSummaryFromDrafts(suggestions, drafts int) timelineDraftCheckpointSummary {
	skipped := suggestions - drafts
	if skipped < 0 {
		skipped = 0
	}
	return timelineDraftCheckpointSummary{
		Suggestions: suggestions,
		Drafts:      drafts,
		Skipped:     skipped,
	}
}

type videoIndexCheckpointSummary struct {
	VideoID          string `json:"videoId"`
	DurationMs       int64  `json:"durationMs,omitempty"`
	DetectedLanguage string `json:"detectedLanguage,omitempty"`
	SourceLanguage   string `json:"sourceLanguage,omitempty"`
	Scenes           int    `json:"scenes,omitempty"`
	Shots            int    `json:"shots,omitempty"`
	Keyframes        int    `json:"keyframes,omitempty"`
	Transcript       int    `json:"transcript,omitempty"`
	OCR              int    `json:"ocr,omitempty"`
	Labels           int    `json:"labels,omitempty"`
	Objects          int    `json:"objects,omitempty"`
}

func videoIndexCheckpointSummaryFromResult(result VideoIndexResult) videoIndexCheckpointSummary {
	return videoIndexCheckpointSummary{
		VideoID:          result.VideoID,
		DurationMs:       result.DurationMs,
		DetectedLanguage: result.DetectedLanguage,
		SourceLanguage:   result.SourceLanguage,
		Scenes:           len(result.Insights.Scenes),
		Shots:            len(result.Insights.Shots),
		Keyframes:        len(result.Insights.Keyframes),
		Transcript:       len(result.Insights.Transcript),
		OCR:              len(result.Insights.OCR),
		Labels:           len(result.Insights.Labels),
		Objects:          len(result.Insights.Objects),
	}
}

func mustJSON(value any) json.RawMessage {
	data, _ := json.Marshal(value)
	return data
}
