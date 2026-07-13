package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/microsoft/durabletask-go/task"
	"github.com/zecloud/ai-video-studio/internal/videoindexerstudio"
)

const (
	dtsInitialPollInterval      = 15 * time.Second
	dtsMaximumPollInterval      = 2 * time.Minute
	dtsPollingTimeout           = 30 * time.Minute
	dtsDefaultCancellationGrace = 30 * time.Second
	durableRetryPrefix          = "durable-retryable: "
	activityPrepare             = "videoIndexerPrepare"
	activitySubmit              = "videoIndexerSubmit"
	activityPoll                = "videoIndexerPoll"
	activityNormalize           = "videoIndexerNormalize"
	activityPlan                = "videoIndexerPlan"
	activityTimeline            = "videoIndexerTimeline"
	activityComplete            = "videoIndexerComplete"
	activityCompensate          = "videoIndexerCompensate"
	activityFail                = "videoIndexerFail"
	activityForceCancel         = "videoIndexerForceCancel"
	cancellationWatchdogName    = "video-indexer-cancellation-watchdog"
)

type videoIndexerPollResult struct {
	State     string `json:"state"`
	Processed bool   `json:"processed"`
}

// VideoIndexerActivities keeps external work in short, idempotent activities.
type VideoIndexerActivities struct {
	store       JobStore
	blobs       BlobStager
	video       *VideoIndexerClient
	normalizer  VideoNormalizer
	planner     EditPlanner
	clock       Clock
	forceCancel func(context.Context, string) error
}

func NewVideoIndexerActivities(store JobStore, blobs BlobStager, video *VideoIndexerClient, normalizer VideoNormalizer, planner EditPlanner, clock Clock) *VideoIndexerActivities {
	if clock == nil {
		clock = realClock{}
	}
	return &VideoIndexerActivities{store: store, blobs: blobs, video: video, normalizer: normalizer, planner: planner, clock: clock}
}

func (a *VideoIndexerActivities) SetCancellationTerminator(terminator func(context.Context, string) error) {
	a.forceCancel = terminator
}

func (a *VideoIndexerActivities) update(ctx context.Context, jobID string, status JobStatus, mutate func(*JobDocument)) (StoredJob, error) {
	return (&DurableJobService{store: a.store, clock: a.clock}).transition(ctx, jobID, status, mutate)
}

func (a *VideoIndexerActivities) Prepare(ctx task.ActivityContext) (any, error) {
	input, err := activityInput(ctx)
	if err != nil {
		return nil, err
	}
	_, err = a.update(ctx.Context(), input.JobID, JobStatusProcessing, nil)
	return nil, err
}

func (a *VideoIndexerActivities) Submit(ctx task.ActivityContext) (any, error) {
	input, err := activityInput(ctx)
	if err != nil {
		return nil, err
	}
	job, err := a.store.Get(ctx.Context(), input.JobID)
	if err != nil || job.VideoIndexerVideoID != "" {
		return nil, err
	}
	videoID, err := a.video.FindVideoByExternalID(ctx.Context(), job.ID)
	if err != nil {
		return nil, err
	}
	if videoID == "" {
		readURL, err := a.blobs.ReadURL(ctx.Context(), StagedAsset{Container: job.StagingContainer, BlobName: job.StagedBlobName})
		if err != nil {
			return nil, err
		}
		videoID, err = a.video.UploadVideoURL(ctx.Context(), readURL, job.SourceName, job.ID)
		if err != nil {
			return nil, err
		}
	}
	_, err = a.update(ctx.Context(), job.ID, JobStatusIndexing, func(doc *JobDocument) {
		doc.VideoIndexerVideoID = videoID
		doc.VideoIndexerState = "indexing"
	})
	return nil, err
}

func (a *VideoIndexerActivities) Poll(ctx task.ActivityContext) (any, error) {
	input, err := activityInput(ctx)
	if err != nil {
		return nil, err
	}
	job, err := a.store.Get(ctx.Context(), input.JobID)
	if err != nil {
		return nil, err
	}
	if job.VideoIndexerVideoID == "" {
		return nil, fmt.Errorf("job %s has no Video Indexer video ID", job.ID)
	}
	index, err := a.video.GetVideoIndexOnce(ctx.Context(), job.VideoIndexerVideoID)
	if err != nil {
		return nil, err
	}
	state := normalizeVideoState(index.State())
	if state == "failed" || state == "canceled" || state == "cancelled" {
		failure := videoIndexerTerminalError(index)
		_, updateErr := a.update(ctx.Context(), job.ID, JobStatusFailed, func(doc *JobDocument) {
			doc.Error = (&ServiceError{Status: 422, Code: serviceErrorCode(failure), Message: serviceErrorMessage(failure), Retryable: false}).APIError()
			doc.VideoIndexerState = index.State()
		})
		if updateErr != nil {
			return nil, updateErr
		}
		return nil, failure
	}
	_, err = a.update(ctx.Context(), job.ID, JobStatusIndexing, func(doc *JobDocument) { doc.VideoIndexerState = index.State() })
	if err != nil {
		return nil, err
	}
	return videoIndexerPollResult{State: index.State(), Processed: state == "processed"}, nil
}

func (a *VideoIndexerActivities) Normalize(ctx task.ActivityContext) (any, error) {
	input, err := activityInput(ctx)
	if err != nil {
		return nil, err
	}
	job, err := a.store.Get(ctx.Context(), input.JobID)
	if err != nil || job.VideoIndexResult != nil {
		return nil, err
	}
	if a.normalizer == nil {
		return nil, newServiceError(503, "normalizer_unavailable", "video normalizer is not configured", true)
	}
	readURL, err := a.blobs.ReadURL(ctx.Context(), StagedAsset{Container: job.StagingContainer, BlobName: job.StagedBlobName})
	if err != nil {
		return nil, err
	}
	index, err := a.video.GetVideoIndexOnce(ctx.Context(), job.VideoIndexerVideoID)
	if err != nil {
		return nil, err
	}
	result, err := a.normalizer.Normalize(ctx.Context(), job.JobDocument, StagedAsset{Container: job.StagingContainer, BlobName: job.StagedBlobName}, readURL, index)
	if err != nil {
		return nil, err
	}
	_, err = a.update(ctx.Context(), job.ID, JobStatusNormalizing, func(doc *JobDocument) {
		doc.VideoIndexResult = &result
		doc.VideoIndexerState = index.State()
		doc.Checkpoints = append(doc.Checkpoints, JobCheckpoint{Stage: "normalizing", At: a.clock.Now(), VideoID: job.VideoIndexerVideoID, State: index.State(), Detail: "video index normalized", Metadata: mustJSON(videoIndexCheckpointSummaryFromResult(result))})
	})
	return nil, err
}

func (a *VideoIndexerActivities) Plan(ctx task.ActivityContext) (any, error) {
	input, err := activityInput(ctx)
	if err != nil {
		return nil, err
	}
	job, err := a.store.Get(ctx.Context(), input.JobID)
	if err != nil || job.EditPlan != nil {
		return nil, err
	}
	if a.planner == nil {
		return nil, newServiceError(503, "edit_planner_unavailable", "edit planner is not configured", true)
	}
	if job.VideoIndexResult == nil {
		return nil, fmt.Errorf("job %s has no normalized video index", job.ID)
	}
	prompt, evidence, err := buildEditPlannerPrompt(ctx.Context(), job.JobDocument, StagedAsset{Container: job.StagingContainer, BlobName: job.StagedBlobName}, *job.VideoIndexResult, defaultEditPlannerEvidenceLimit, nil)
	if err != nil {
		return nil, err
	}
	plan, err := a.planner.Plan(ctx.Context(), prompt)
	if err != nil {
		return nil, err
	}
	plan, err = validateEditPlan(plan, evidence)
	if err != nil {
		return nil, err
	}
	_, err = a.update(ctx.Context(), job.ID, JobStatusGenerating, func(doc *JobDocument) { doc.EditPlan = &plan })
	return nil, err
}

func (a *VideoIndexerActivities) Timeline(ctx task.ActivityContext) (any, error) {
	input, err := activityInput(ctx)
	if err != nil {
		return nil, err
	}
	job, err := a.store.Get(ctx.Context(), input.JobID)
	if err != nil || len(job.TimelineDrafts) != 0 {
		return nil, err
	}
	if job.EditPlan == nil {
		return nil, fmt.Errorf("job %s has no edit plan", job.ID)
	}
	drafts, err := buildTimelineDrafts(ctx.Context(), job.JobDocument, *job.EditPlan, editPlannerInstructionsVersion, nil)
	if err != nil {
		return nil, err
	}
	_, err = a.update(ctx.Context(), job.ID, JobStatusBuildingTimeline, func(doc *JobDocument) {
		doc.TimelineDrafts = append([]videoindexerstudio.TimelineDraft(nil), drafts...)
	})
	return nil, err
}
func (a *VideoIndexerActivities) Complete(ctx task.ActivityContext) (any, error) {
	input, err := activityInput(ctx)
	if err != nil {
		return nil, err
	}
	job, err := a.store.Get(ctx.Context(), input.JobID)
	if err != nil {
		return nil, err
	}
	if err := a.deleteStagedBlob(ctx.Context(), job); err != nil {
		return nil, err
	}
	_, err = a.update(ctx.Context(), input.JobID, JobStatusSucceeded, nil)
	return nil, err
}

func (a *VideoIndexerActivities) Fail(ctx task.ActivityContext) (any, error) {
	input, err := activityInput(ctx)
	if err != nil {
		return nil, err
	}
	job, err := a.store.Get(ctx.Context(), input.JobID)
	if err != nil {
		return nil, err
	}
	if err := a.deleteStagedBlob(ctx.Context(), job); err != nil {
		return nil, err
	}
	_, err = a.update(ctx.Context(), input.JobID, JobStatusFailed, func(doc *JobDocument) {
		if doc.Error == nil {
			doc.Error = &APIErrorResponse{Code: "durable_execution_failed", Message: "durable Video Indexer execution failed", Retryable: false}
		}
	})
	return nil, err
}

func (a *VideoIndexerActivities) Compensate(ctx task.ActivityContext) (any, error) {
	input, err := activityInput(ctx)
	if err != nil {
		return nil, err
	}
	job, err := a.store.Get(ctx.Context(), input.JobID)
	if err != nil {
		return nil, err
	}
	if err := a.deleteStagedBlob(ctx.Context(), job); err != nil {
		return nil, err
	}
	_, err = a.update(ctx.Context(), input.JobID, JobStatusCanceled, func(doc *JobDocument) {
		doc.Error = &APIErrorResponse{Code: "canceled", Message: "job canceled", Retryable: false}
	})
	return nil, err
}

func (a *VideoIndexerActivities) deleteStagedBlob(ctx context.Context, job StoredJob) error {
	if err := a.blobs.Delete(ctx, StagedAsset{Container: job.StagingContainer, BlobName: job.StagedBlobName}); err != nil {
		return newServiceError(503, "staging_cleanup_failed", redactURLsInText(err.Error()), durableServiceRetryable(err))
	}
	return nil
}
func (a *VideoIndexerActivities) ForceCancel(ctx task.ActivityContext) (any, error) {
	input, err := activityInput(ctx)
	if err != nil {
		return nil, err
	}
	job, err := a.store.Get(ctx.Context(), input.JobID)
	if err != nil || job.Status.Terminal() {
		return nil, err
	}
	if a.forceCancel == nil {
		return nil, newServiceError(503, "cancellation_terminator_unavailable", "durable cancellation terminator is not configured", true)
	}
	return nil, a.forceCancel(ctx.Context(), input.JobID)
}

func CancellationWatchdogOrchestrator(ctx *task.OrchestrationContext) (any, error) {
	var input VideoIndexerOrchestrationInput
	if err := ctx.GetInput(&input); err != nil {
		return nil, err
	}
	grace := input.CancellationGrace
	if grace <= 0 {
		grace = dtsDefaultCancellationGrace
	}
	if err := ctx.CreateTimer(grace).Await(nil); err != nil {
		return nil, err
	}
	if err := ctx.CallActivity(activityForceCancel, task.WithActivityInput(input), task.WithActivityRetryPolicy(durableRetryPolicy())).Await(nil); err != nil {
		return nil, err
	}
	return map[string]string{"jobId": input.JobID, "status": "cancellation_reconciled"}, nil
}

func activityInput(ctx task.ActivityContext) (VideoIndexerOrchestrationInput, error) {
	var input VideoIndexerOrchestrationInput
	if err := ctx.GetInput(&input); err != nil {
		return VideoIndexerOrchestrationInput{}, err
	}
	if input.JobID == "" {
		return VideoIndexerOrchestrationInput{}, fmt.Errorf("activity input job ID is required")
	}
	return input, nil
}

func cancellationRequested(ctx *task.OrchestrationContext) (bool, error) {
	if err := ctx.WaitForSingleEvent(cancellationEventName, 0).Await(nil); err == nil {
		return true, nil
	} else if errors.Is(err, task.ErrTaskCanceled) {
		return false, nil
	} else {
		return false, err
	}
}

func durableRetryPolicy() *task.RetryPolicy {
	return &task.RetryPolicy{
		MaxAttempts:          4,
		InitialRetryInterval: 5 * time.Second,
		BackoffCoefficient:   2,
		MaxRetryInterval:     time.Minute,
		RetryTimeout:         5 * time.Minute,
		Handle: func(err error) bool {
			return strings.Contains(err.Error(), durableRetryPrefix)
		},
	}
}

func callVideoIndexerActivity(ctx *task.OrchestrationContext, activity string, input VideoIndexerOrchestrationInput, result any, retry bool) error {
	canceled, err := cancellationRequested(ctx)
	if err != nil {
		return err
	}
	if canceled {
		return errCancellationRequested
	}
	if retry {
		return ctx.CallActivity(activity, task.WithActivityInput(input), task.WithActivityRetryPolicy(durableRetryPolicy())).Await(result)
	}
	return ctx.CallActivity(activity, task.WithActivityInput(input)).Await(result)
}

func retryableActivity(activity task.Activity) task.Activity {
	return func(ctx task.ActivityContext) (any, error) {
		result, err := activity(ctx)
		var serviceErr *ServiceError
		if err != nil && errors.As(err, &serviceErr) && serviceErr.Retryable {
			return nil, fmt.Errorf("%s%w", durableRetryPrefix, err)
		}
		return result, err
	}
}

var errCancellationRequested = errors.New("video indexer cancellation requested")

func VideoIndexerOrchestrator(ctx *task.OrchestrationContext) (any, error) {
	var input VideoIndexerOrchestrationInput
	if err := ctx.GetInput(&input); err != nil {
		return nil, err
	}
	for _, activity := range []string{activityPrepare, activitySubmit} {
		if err := callVideoIndexerActivity(ctx, activity, input, nil, true); err != nil {
			if errors.Is(err, errCancellationRequested) {
				return compensate(ctx, input)
			}
			return fail(ctx, input)
		}
	}

	deadline := ctx.CurrentTimeUtc.Add(dtsPollingTimeout)
	pollInterval := dtsInitialPollInterval
	for {
		if !ctx.CurrentTimeUtc.Before(deadline) {
			return fail(ctx, input)
		}
		var result videoIndexerPollResult
		if err := callVideoIndexerActivity(ctx, activityPoll, input, &result, true); err != nil {
			if errors.Is(err, errCancellationRequested) {
				return compensate(ctx, input)
			}
			return fail(ctx, input)
		}
		if result.Processed {
			break
		}
		if err := ctx.WaitForSingleEvent(cancellationEventName, pollInterval).Await(nil); err == nil {
			return compensate(ctx, input)
		} else if !errors.Is(err, task.ErrTaskCanceled) {
			return fail(ctx, input)
		}
		if pollInterval < dtsMaximumPollInterval {
			pollInterval *= 2
			if pollInterval > dtsMaximumPollInterval {
				pollInterval = dtsMaximumPollInterval
			}
		}
	}
	for _, activity := range []string{activityNormalize, activityPlan, activityTimeline, activityComplete} {
		if err := callVideoIndexerActivity(ctx, activity, input, nil, true); err != nil {
			if errors.Is(err, errCancellationRequested) {
				return compensate(ctx, input)
			}
			return fail(ctx, input)
		}
	}
	return map[string]string{"jobId": input.JobID, "status": string(JobStatusSucceeded)}, nil
}

func fail(ctx *task.OrchestrationContext, input VideoIndexerOrchestrationInput) (any, error) {
	if err := ctx.CallActivity(activityFail, task.WithActivityInput(input), task.WithActivityRetryPolicy(durableRetryPolicy())).Await(nil); err != nil {
		return nil, err
	}
	return map[string]string{"jobId": input.JobID, "status": string(JobStatusFailed)}, nil
}

func compensate(ctx *task.OrchestrationContext, input VideoIndexerOrchestrationInput) (any, error) {
	if err := ctx.CallActivity(activityCompensate, task.WithActivityInput(input), task.WithActivityRetryPolicy(durableRetryPolicy())).Await(nil); err != nil {
		return nil, err
	}
	return map[string]string{"jobId": input.JobID, "status": string(JobStatusCanceled)}, nil
}

func NewVideoIndexerTaskRegistry(activities *VideoIndexerActivities) (*task.TaskRegistry, error) {
	if activities == nil {
		return nil, fmt.Errorf("video indexer activities are required")
	}
	registry := task.NewTaskRegistry()
	if err := registry.AddOrchestratorN(videoIndexerOrchestrationName, VideoIndexerOrchestrator); err != nil {
		return nil, err
	}
	if err := registry.AddOrchestratorN(cancellationWatchdogName, CancellationWatchdogOrchestrator); err != nil {
		return nil, err
	}
	if err := registry.AddActivityN(activityPrepare, retryableActivity(activities.Prepare)); err != nil {
		return nil, err
	}
	if err := registry.AddActivityN(activitySubmit, retryableActivity(activities.Submit)); err != nil {
		return nil, err
	}
	if err := registry.AddActivityN(activityPoll, retryableActivity(activities.Poll)); err != nil {
		return nil, err
	}
	if err := registry.AddActivityN(activityNormalize, retryableActivity(activities.Normalize)); err != nil {
		return nil, err
	}
	if err := registry.AddActivityN(activityPlan, retryableActivity(activities.Plan)); err != nil {
		return nil, err
	}
	if err := registry.AddActivityN(activityTimeline, retryableActivity(activities.Timeline)); err != nil {
		return nil, err
	}
	if err := registry.AddActivityN(activityComplete, retryableActivity(activities.Complete)); err != nil {
		return nil, err
	}
	if err := registry.AddActivityN(activityCompensate, retryableActivity(activities.Compensate)); err != nil {
		return nil, err
	}
	if err := registry.AddActivityN(activityFail, retryableActivity(activities.Fail)); err != nil {
		return nil, err
	}
	if err := registry.AddActivityN(activityForceCancel, retryableActivity(activities.ForceCancel)); err != nil {
		return nil, err
	}
	return registry, nil
}
