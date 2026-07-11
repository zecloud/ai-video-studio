package main

import (
	"testing"
	"time"

	"github.com/zecloud/ai-video-studio/internal/videoindexerstudio"
)

func TestJobDocumentToJobPreservesTimelineDraftsAndResults(t *testing.T) {
	now := time.Date(2026, 7, 10, 13, 46, 9, 0, time.UTC)
	draft := videoindexerstudio.TimelineDraft{
		SchemaVersion: videoindexerstudio.TimelineDraftSchemaVersion,
		OriginJobID:   "job-1",
		SuggestionID:  "suggestion-1",
		PromptVersion: "v1",
		PrimaryVideoTrack: videoindexerstudio.TimelineTrack{
			ID:   "primary-video",
			Kind: videoindexerstudio.TimelineTrackKindVideo,
			Clips: []videoindexerstudio.TimelineClip{
				{
					ID:              "clip-1",
					SourceAssetID:   "item-001",
					InMS:            0,
					OutMS:           1000,
					TimelineStartMS: 0,
					DurationMS:      1000,
					Transition:      videoindexerstudio.TimelineTransition{Kind: videoindexerstudio.TimelineTransitionKindCut},
				},
			},
		},
	}

	doc := JobDocument{
		SchemaVersion:       schemaVersion,
		ID:                  "job-1",
		Status:              JobStatusSucceeded,
		OneDriveItemID:      "item-001",
		SourceName:          "clip.mp4",
		VideoIndexerVideoID: "video-123",
		VideoIndexResult:    &VideoIndexResult{VideoID: "video-123", State: "Processed"},
		EditPlan:            &EditPlan{SchemaVersion: 1, VideoID: "video-123", AssetID: "item-001", Title: "Title", Summary: "Summary"},
		TimelineDrafts:      []videoindexerstudio.TimelineDraft{draft},
		Checkpoints:         []JobCheckpoint{{Stage: "completed", At: now}},
		CreatedAt:           now,
		UpdatedAt:           now,
		StartedAt:           &now,
		CompletedAt:         &now,
		Error:               &APIErrorResponse{Code: "failed", Message: "boom", Retryable: true},
	}

	job := doc.ToJob()
	if job.VideoIndexerVideoID != "video-123" {
		t.Fatalf("video id = %q", job.VideoIndexerVideoID)
	}
	if job.VideoIndexResult == nil || job.VideoIndexResult.VideoID != "video-123" {
		t.Fatalf("video index result was not copied: %#v", job.VideoIndexResult)
	}
	if job.EditPlan == nil || job.EditPlan.VideoID != "video-123" {
		t.Fatalf("edit plan was not copied: %#v", job.EditPlan)
	}
	if len(job.TimelineDrafts) != 1 || job.TimelineDrafts[0].SuggestionID != "suggestion-1" {
		t.Fatalf("timeline drafts were not copied: %#v", job.TimelineDrafts)
	}
	if len(job.Checkpoints) != 1 || job.Checkpoints[0].Stage != "completed" {
		t.Fatalf("checkpoints were not copied: %#v", job.Checkpoints)
	}
	if job.Error == nil || job.Error.Code != "failed" || !job.Error.Retryable {
		t.Fatalf("error was not copied: %#v", job.Error)
	}
}
