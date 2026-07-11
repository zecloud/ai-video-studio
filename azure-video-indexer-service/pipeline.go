package main

import (
	"context"
	"fmt"
)

type Pipeline interface {
	Process(ctx context.Context, job JobDocument, asset StagedAsset, readURL string, progress JobProgressRecorder) (PipelineOutcome, error)
}

type NoopPipeline struct{}

func (NoopPipeline) Process(context.Context, JobDocument, StagedAsset, string, JobProgressRecorder) (PipelineOutcome, error) {
	return PipelineOutcome{Kind: PipelineOutcomePendingNormalization}, nil
}

type PipelineFunc func(ctx context.Context, job JobDocument, asset StagedAsset, readURL string, progress JobProgressRecorder) (PipelineOutcome, error)

func (f PipelineFunc) Process(ctx context.Context, job JobDocument, asset StagedAsset, readURL string, progress JobProgressRecorder) (PipelineOutcome, error) {
	if f == nil {
		return PipelineOutcome{}, fmt.Errorf("pipeline function is nil")
	}
	return f(ctx, job, asset, readURL, progress)
}

type JobProgressRecorder interface {
	UpdateProgress(ctx context.Context, jobID string, desired JobStatus, mutate func(*JobDocument)) (StoredJob, error)
}

type PipelineOutcomeKind string

const (
	PipelineOutcomePendingNormalization PipelineOutcomeKind = "pending_normalization"
	PipelineOutcomePendingEditing       PipelineOutcomeKind = "pending_editing"
	PipelineOutcomeCompleted            PipelineOutcomeKind = "completed"
)

type PipelineOutcome struct {
	Kind       PipelineOutcomeKind
	VideoIndex VideoIndexData
}
