package backend

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/zecloud/ai-video-studio/internal/editing"
	"github.com/zecloud/ai-video-studio/internal/library"
	"github.com/zecloud/ai-video-studio/internal/onedrive"
	"github.com/zecloud/ai-video-studio/internal/settings"
	"github.com/zecloud/ai-video-studio/internal/videoindexerstudio"
)

const (
	videoIndexerJobStatusPending   = "pending"
	videoIndexerJobStatusSubmitted = "submitted"
	videoIndexerJobStatusPolling   = "polling"
	videoIndexerJobStatusSucceeded = "succeeded"
	videoIndexerJobStatusFailed    = "failed"
	videoIndexerJobStatusCanceled  = "canceled"
)

type videoIndexerClient interface {
	CreateJob(context.Context, videoindexerstudio.CreateJobRequest) (*videoindexerstudio.JobResponse, error)
	GetJob(context.Context, string) (*videoindexerstudio.JobResponse, error)
	CancelJob(context.Context, string) (*videoindexerstudio.JobResponse, error)
}

type narrativeRankingClient interface {
	RankNarrative(context.Context, videoindexerstudio.NarrativeRankingRequest) (*videoindexerstudio.NarrativeRankingResponse, error)
}

type narrativeIntentClassificationClient interface {
	ClassifyNarrativeIntent(context.Context, videoindexerstudio.NarrativeIntentClassificationRequest) (*videoindexerstudio.NarrativeIntentClassificationResponse, error)
}

type narrativeSegmentPlanningClient interface {
	PlanNarrativeSegments(context.Context, videoindexerstudio.NarrativeSegmentPlanningRequest) (*videoindexerstudio.NarrativeSegmentPlanningResponse, error)
}

type narrativePacingResolution struct {
	profile        videoindexerstudio.NarrativePacingProfile
	mode           videoindexerstudio.NarrativePacingClassifierMode
	fallbackReason videoindexerstudio.NarrativePacingClassifierFallbackReason
}

type videoIndexerJobStore interface {
	Load(context.Context) ([]VideoIndexerStudioJob, error)
	Save(context.Context, []VideoIndexerStudioJob) error
}

type videoIndexerProjectSaver interface {
	SaveProject(context.Context, editing.EditProject) (editing.EditProject, error)
}

type videoIndexerOneDriveSource interface {
	DriveClient() *onedrive.Client
}

type VideoIndexerStudioJob struct {
	ID                  string                                  `json:"id"`
	AssetID             string                                  `json:"assetId"`
	AssetIDs            []string                                `json:"assetIds,omitempty"`
	AssetName           string                                  `json:"assetName"`
	Composition         bool                                    `json:"composition,omitempty"`
	NarrativeIntent     string                                  `json:"narrativeIntent,omitempty"`
	DependencyJobIDs    []string                                `json:"dependencyJobIds,omitempty"`
	RemoteJobID         string                                  `json:"remoteJobId,omitempty"`
	RemoteStatus        string                                  `json:"remoteStatus,omitempty"`
	Stage               string                                  `json:"stage,omitempty"`
	Status              string                                  `json:"status"`
	ErrorMessage        string                                  `json:"errorMessage,omitempty"`
	Retryable           bool                                    `json:"retryable,omitempty"`
	SuggestionID        string                                  `json:"suggestionId,omitempty"`
	ProjectID           string                                  `json:"projectId,omitempty"`
	VideoIndexerVideoID string                                  `json:"videoIndexerVideoId,omitempty"`
	VideoIndexResult    *videoindexerstudio.VideoIndexResult    `json:"videoIndexResult,omitempty"`
	EditPlan            *videoindexerstudio.EditPlan            `json:"editPlan,omitempty"`
	CompositionPlan     *videoindexerstudio.CompositionEditPlan `json:"compositionPlan,omitempty"`
	TimelineDrafts      []videoindexerstudio.TimelineDraft      `json:"timelineDrafts,omitempty"`
	Checkpoints         []videoindexerstudio.JobCheckpoint      `json:"checkpoints,omitempty"`
	CreatedAt           time.Time                               `json:"createdAt"`
	UpdatedAt           time.Time                               `json:"updatedAt"`
	StartedAt           *time.Time                              `json:"startedAt,omitempty"`
	CompletedAt         *time.Time                              `json:"completedAt,omitempty"`
	ClaimedBy           string                                  `json:"claimedBy,omitempty"`
}

type videoIndexerJobDocument struct {
	Version int                     `json:"version"`
	Jobs    []VideoIndexerStudioJob `json:"jobs"`
}

type VideoIndexerStudioService struct {
	mu            sync.Mutex
	library       library.Store
	oneDrive      videoIndexerOneDriveSource
	editing       videoIndexerProjectSaver
	client        videoIndexerClient
	clientFactory func(context.Context) (videoIndexerClient, error)
	store         videoIndexerJobStore
	jobs          map[string]VideoIndexerStudioJob
	loaded        bool
	now           func() time.Time
	submissionSeq uint64
}

func NewVideoIndexerStudioService(libraryStore library.Store, oneDrive videoIndexerOneDriveSource, editingSvc videoIndexerProjectSaver, client videoIndexerClient, jobStore ...videoIndexerJobStore) *VideoIndexerStudioService {
	return newVideoIndexerStudioService(libraryStore, oneDrive, editingSvc, nil, client, jobStore...)
}

func NewVideoIndexerStudioServiceFromSettings(libraryStore library.Store, oneDrive videoIndexerOneDriveSource, editingSvc videoIndexerProjectSaver, settingsStore settings.Store, jobStore ...videoIndexerJobStore) *VideoIndexerStudioService {
	return newVideoIndexerStudioService(libraryStore, oneDrive, editingSvc, settingsStore, nil, jobStore...)
}

func newVideoIndexerStudioService(libraryStore library.Store, oneDrive videoIndexerOneDriveSource, editingSvc videoIndexerProjectSaver, settingsStore settings.Store, client videoIndexerClient, jobStore ...videoIndexerJobStore) *VideoIndexerStudioService {
	service := &VideoIndexerStudioService{
		library:  libraryStore,
		oneDrive: oneDrive,
		editing:  editingSvc,
		client:   client,
		now:      func() time.Time { return time.Now().UTC() },
		jobs:     map[string]VideoIndexerStudioJob{},
	}
	if settingsStore != nil {
		service.clientFactory = func(ctx context.Context) (videoIndexerClient, error) {
			return newVideoIndexerClientFromSettings(ctx, settingsStore)
		}
	}
	if len(jobStore) > 0 {
		service.store = jobStore[0]
	} else {
		service.store = newDefaultVideoIndexerJobStore()
	}
	if service.store == nil {
		service.loaded = true
	}
	return service
}

func (s *VideoIndexerStudioService) SubmitForIndexing(ctx context.Context, assetID string) (*VideoIndexerStudioJob, error) {
	client, err := s.clientFor(ctx)
	if err != nil {
		return nil, err
	}

	asset, err := s.resolveAsset(ctx, assetID)
	if err != nil {
		return nil, err
	}
	clientOneDrive := s.oneDriveClient()
	if clientOneDrive == nil || clientOneDrive.TokenProvider == nil {
		return nil, errors.New("OneDrive client is not configured")
	}
	oneDriveToken, err := clientOneDrive.TokenProvider.AccessToken(ctx, clientOneDrive.Scopes)
	if err != nil {
		return nil, fmt.Errorf("resolve OneDrive access token: %w", err)
	}
	if strings.TrimSpace(oneDriveToken) == "" {
		return nil, errors.New("OneDrive access token is empty")
	}

	now := s.nowTime()
	jobID := s.nextJobID(now)
	localJob := VideoIndexerStudioJob{
		ID:        jobID,
		AssetID:   asset.ID,
		AssetName: asset.Name,
		Status:    videoIndexerJobStatusSubmitted,
		CreatedAt: now,
		UpdatedAt: now,
	}

	resp, err := client.CreateJob(ctx, videoindexerstudio.CreateJobRequest{
		OneDriveItemID:      asset.CloudAssetID,
		OneDriveAccessToken: oneDriveToken,
		SourceName:          asset.Name,
		CorrelationID:       localJob.ID,
	})
	if err != nil {
		localJob.Status = videoIndexerJobStatusFailed
		localJob.ErrorMessage, localJob.Retryable = videoIndexerErrorDetails(err, false)
		localJob.UpdatedAt = s.nowTime()
		_ = s.saveJob(ctx, localJob)
		emitVideoIndexerEvent("video-indexer:failed", localJob)
		return nil, fmt.Errorf("submit video indexer job: %w", err)
	}
	if resp != nil {
		localJob = s.mergeRemoteJob(localJob, resp, false)
	}
	if err := s.saveJob(ctx, localJob); err != nil {
		return nil, err
	}
	if localJob.Status == videoIndexerJobStatusSucceeded {
		emitVideoIndexerEvent("video-indexer:completed", localJob)
	} else if localJob.Status == videoIndexerJobStatusFailed || localJob.Status == videoIndexerJobStatusCanceled {
		emitVideoIndexerEvent("video-indexer:failed", localJob)
	} else {
		emitVideoIndexerEvent("video-indexer:progress", localJob)
	}
	return &localJob, nil
}

// GenerateMultiVideoEdit creates one persistent composition that waits for or reuses source analyses.
func (s *VideoIndexerStudioService) GenerateMultiVideoEdit(ctx context.Context, assetIDs []string) (*VideoIndexerStudioJob, error) {
	return s.GenerateMultiVideoEditWithIntent(ctx, assetIDs, "")
}

// GenerateMultiVideoEditWithIntent creates one persistent composition with an
// optional editorial preference for grounded Azure narrative ranking.
func (s *VideoIndexerStudioService) GenerateMultiVideoEditWithIntent(ctx context.Context, assetIDs []string, narrativeIntent string) (*VideoIndexerStudioJob, error) {
	assetIDs = uniqueNonEmptyStrings(assetIDs)
	if len(assetIDs) < 2 {
		return nil, errors.New("select at least two assets for a multi-video edit")
	}
	narrativeIntent, err := videoindexerstudio.NormalizeNarrativeIntent(narrativeIntent)
	if err != nil {
		return nil, err
	}
	assets := make([]library.ProjectAsset, 0, len(assetIDs))
	for _, assetID := range assetIDs {
		asset, err := s.resolveAsset(ctx, assetID)
		if err != nil {
			return nil, err
		}
		assets = append(assets, asset)
	}
	if err := s.ensureJobsLoaded(ctx); err != nil {
		return nil, err
	}

	dependencies := make([]string, 0, len(assets))
	for _, asset := range assets {
		dependency, ok := s.reusableAnalysisJob(asset.ID)
		if !ok {
			created, err := s.SubmitForIndexing(ctx, asset.ID)
			if err != nil {
				return nil, fmt.Errorf("prepare analysis for %q: %w", asset.Name, err)
			}
			dependency = *created
		}
		dependencies = append(dependencies, dependency.ID)
	}

	now := s.nowTime()
	job := VideoIndexerStudioJob{
		ID: s.nextJobID(now), AssetID: assetIDs[0], AssetIDs: append([]string(nil), assetIDs...),
		AssetName: fmt.Sprintf("%d selected videos", len(assetIDs)), Composition: true, DependencyJobIDs: dependencies,
		NarrativeIntent: narrativeIntent,
		Status:          videoIndexerJobStatusPending, Stage: "waiting_for_analyses", CreatedAt: now, UpdatedAt: now,
	}
	if err := s.saveJob(ctx, job); err != nil {
		return nil, err
	}
	job = s.evaluateComposition(ctx, job)
	if err := s.saveJob(ctx, job); err != nil {
		return nil, err
	}
	emitVideoIndexerEvent(videoIndexerEventName(job.Status), job)
	return &job, nil
}

func (s *VideoIndexerStudioService) nextJobID(now time.Time) string {
	seq := atomic.AddUint64(&s.submissionSeq, 1)
	return fmt.Sprintf("video-indexer-%d-%d", now.UTC().UnixNano(), seq)
}

func (s *VideoIndexerStudioService) IndexingJobs(ctx context.Context) ([]VideoIndexerStudioJob, error) {
	if err := s.ensureJobsLoaded(ctx); err != nil {
		return nil, err
	}
	s.mu.Lock()
	jobs := make([]VideoIndexerStudioJob, 0, len(s.jobs))
	for _, job := range s.jobs {
		jobs = append(jobs, job)
	}
	s.mu.Unlock()

	for i := range jobs {
		if jobs[i].Composition {
			continue
		}
		if isTerminalVideoIndexerStatus(jobs[i].Status) {
			continue
		}
		updated, err := s.refreshJob(ctx, jobs[i].ID)
		if err != nil {
			return nil, err
		}
		jobs[i] = updated
	}
	for i := range jobs {
		if !jobs[i].Composition || isTerminalVideoIndexerStatus(jobs[i].Status) {
			continue
		}
		previous := jobs[i]
		jobs[i] = s.evaluateComposition(ctx, jobs[i])
		if reflect.DeepEqual(previous, jobs[i]) {
			continue
		}
		if err := s.saveJob(ctx, jobs[i]); err != nil {
			return nil, err
		}
		emitVideoIndexerEvent(videoIndexerEventName(jobs[i].Status), jobs[i])
	}
	sort.Slice(jobs, func(i, j int) bool {
		if jobs[i].CreatedAt.Equal(jobs[j].CreatedAt) {
			return jobs[i].ID > jobs[j].ID
		}
		return jobs[i].CreatedAt.After(jobs[j].CreatedAt)
	})
	return jobs, nil
}

func (s *VideoIndexerStudioService) IndexingJob(ctx context.Context, jobID string) (*VideoIndexerStudioJob, error) {
	job, err := s.loadJob(ctx, jobID)
	if err != nil {
		return nil, err
	}
	if isTerminalVideoIndexerStatus(job.Status) {
		return &job, nil
	}
	if job.Composition {
		jobs, err := s.IndexingJobs(ctx)
		if err != nil {
			return nil, err
		}
		for i := range jobs {
			if jobs[i].ID == job.ID {
				return &jobs[i], nil
			}
		}
		return nil, fmt.Errorf("video indexer job %q not found", jobID)
	}
	updated, err := s.refreshJob(ctx, jobID)
	if err != nil {
		return nil, err
	}
	return &updated, nil
}

func (s *VideoIndexerStudioService) CancelIndexing(ctx context.Context, jobID string) (*VideoIndexerStudioJob, error) {
	job, err := s.loadJob(ctx, jobID)
	if err != nil {
		return nil, err
	}
	if job.Composition {
		if !isTerminalVideoIndexerStatus(job.Status) {
			job.Status = videoIndexerJobStatusCanceled
			job.Stage = "canceled"
			job.UpdatedAt = s.nowTime()
			completed := job.UpdatedAt
			job.CompletedAt = &completed
			if err := s.saveJob(ctx, job); err != nil {
				return nil, err
			}
		}
		emitVideoIndexerEvent(videoIndexerEventName(job.Status), job)
		return &job, nil
	}
	client, err := s.clientFor(ctx)
	if err != nil {
		return nil, err
	}
	remoteID := strings.TrimSpace(job.RemoteJobID)
	if remoteID == "" {
		remoteID = strings.TrimSpace(job.ID)
	}

	resp, err := client.CancelJob(ctx, remoteID)
	if err != nil {
		return nil, fmt.Errorf("cancel video indexer job: %w", err)
	}
	if resp != nil {
		job = s.mergeRemoteJob(job, resp, true)
		if job.Status == videoIndexerJobStatusSubmitted {
			job.Status = videoIndexerJobStatusCanceled
		}
		if resp.Job.Status == videoindexerstudio.JobStatusCanceled {
			job.Status = videoIndexerJobStatusCanceled
		}
	} else if !isTerminalVideoIndexerStatus(job.Status) {
		job.Status = videoIndexerJobStatusCanceled
	}
	if err := s.saveJob(ctx, job); err != nil {
		return nil, err
	}
	emitVideoIndexerEvent(videoIndexerEventName(job.Status), job)
	return &job, nil
}

func (s *VideoIndexerStudioService) reusableAnalysisJob(assetID string) (VideoIndexerStudioJob, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var succeeded, running *VideoIndexerStudioJob
	for _, candidate := range s.jobs {
		if candidate.Composition || candidate.AssetID != assetID {
			continue
		}
		candidate := candidate
		if candidate.Status == videoIndexerJobStatusSucceeded && candidate.VideoIndexResult != nil && candidate.EditPlan != nil {
			if succeeded == nil || candidate.CreatedAt.After(succeeded.CreatedAt) {
				succeeded = &candidate
			}
			continue
		}
		if !isTerminalVideoIndexerStatus(candidate.Status) && (running == nil || candidate.CreatedAt.After(running.CreatedAt)) {
			running = &candidate
		}
	}
	if succeeded != nil {
		return *succeeded, true
	}
	if running != nil {
		return *running, true
	}
	return VideoIndexerStudioJob{}, false
}

func (s *VideoIndexerStudioService) evaluateComposition(ctx context.Context, composition VideoIndexerStudioJob) VideoIndexerStudioJob {
	dependencies := make([]VideoIndexerStudioJob, 0, len(composition.DependencyJobIDs))
	for _, dependencyID := range composition.DependencyJobIDs {
		dependency, err := s.loadJob(ctx, dependencyID)
		if err != nil {
			return failComposition(composition, s.nowTime(), err.Error())
		}
		if dependency.Status == videoIndexerJobStatusFailed || dependency.Status == videoIndexerJobStatusCanceled {
			message := firstNonEmpty(dependency.ErrorMessage, fmt.Sprintf("analysis %s did not complete", dependency.ID))
			return failComposition(composition, s.nowTime(), message)
		}
		if dependency.Status != videoIndexerJobStatusSucceeded {
			composition.Status = videoIndexerJobStatusPending
			composition.Stage = "waiting_for_analyses"
			return composition
		}
		dependencies = append(dependencies, dependency)
	}
	resolution := s.resolveNarrativePacing(ctx, composition.NarrativeIntent)
	plan, compositionPlan, drafts, err := buildMultiVideoCompositionWithPacing(composition.ID, composition.AssetIDs, dependencies, composition.NarrativeIntent, resolution)
	if err != nil {
		return failComposition(composition, s.nowTime(), err.Error())
	}
	compositionPlan.RankingMode = "deterministic_grounded_fallback_v1"
	compositionPlan.EditorialProfile = narrativeIntentProfileForPacing(resolution.profile)
	compositionPlan.PlanningMode = videoindexerstudio.NarrativeSegmentPlanningModeDeterministic
	compositionPlan.PlanningFallbackReason = videoindexerstudio.NarrativeSegmentPlanningFallbackUnavailable
	if composition.NarrativeIntent != "" {
		planningPlan, planningComposition, _, planningErr := buildMultiVideoCompositionCore(composition.ID, composition.AssetIDs, dependencies, composition.NarrativeIntent, resolution, false)
		if planningErr == nil {
			planningComposition.RankingMode = compositionPlan.RankingMode
			planningComposition.EditorialProfile = compositionPlan.EditorialProfile
			planningComposition.PlanningMode = compositionPlan.PlanningMode
			planningComposition.PlanningFallbackReason = compositionPlan.PlanningFallbackReason
			if client, clientErr := s.clientFor(ctx); clientErr == nil {
				if planner, ok := client.(narrativeSegmentPlanningClient); ok {
					if plannedPlan, plannedComposition, plannedDrafts, planErr := planMultiVideoCompositionSegments(ctx, planner, planningPlan, planningComposition, dependencies); planErr == nil {
						plan, compositionPlan, drafts = plannedPlan, plannedComposition, plannedDrafts
					} else {
						compositionPlan.PlanningFallbackReason = narrativeSegmentPlanningFallbackReason(planErr)
					}
				}
			}
		} else {
			compositionPlan.PlanningFallbackReason = videoindexerstudio.NarrativeSegmentPlanningFallbackCatalogInvalid
		}
	}
	if client, clientErr := s.clientFor(ctx); clientErr == nil {
		if ranker, ok := client.(narrativeRankingClient); ok {
			if rankedPlan, rankedComposition, rankedDrafts, rankErr := rankMultiVideoComposition(ctx, ranker, plan, compositionPlan, dependencies); rankErr == nil {
				plan, compositionPlan, drafts = rankedPlan, rankedComposition, rankedDrafts
			}
		}
	}
	now := s.nowTime()
	composition.Status = videoIndexerJobStatusSucceeded
	composition.RemoteStatus = "succeeded"
	composition.Stage = "composition_complete"
	composition.EditPlan = &plan
	composition.CompositionPlan = &compositionPlan
	composition.TimelineDrafts = drafts
	composition.ErrorMessage = ""
	composition.UpdatedAt = now
	composition.CompletedAt = &now
	return composition
}

func failComposition(job VideoIndexerStudioJob, now time.Time, message string) VideoIndexerStudioJob {
	job.Status = videoIndexerJobStatusFailed
	job.Stage = "composition_failed"
	job.ErrorMessage = strings.TrimSpace(message)
	job.UpdatedAt = now
	job.CompletedAt = &now
	return job
}

func (s *VideoIndexerStudioService) resolveNarrativePacing(ctx context.Context, intent string) narrativePacingResolution {
	if intent == "" {
		return narrativePacingResolution{
			profile:        videoindexerstudio.NarrativePacingProfileStandard,
			mode:           videoindexerstudio.NarrativePacingClassifierModeStandardFallback,
			fallbackReason: videoindexerstudio.NarrativePacingClassifierFallbackNoIntent,
		}
	}
	if client, err := s.clientFor(ctx); err == nil {
		if classifier, ok := client.(narrativeIntentClassificationClient); ok {
			response, classifyErr := classifier.ClassifyNarrativeIntent(ctx, videoindexerstudio.NarrativeIntentClassificationRequest{SchemaVersion: videoindexerstudio.NarrativeRankingSchemaVersion, NarrativeIntent: intent})
			if classifyErr == nil {
				if response == nil || response.Validate() != nil {
					return deterministicNarrativePacingResolution(intent, videoindexerstudio.NarrativePacingClassifierFallbackInvalidResponse)
				}
				return narrativePacingResolution{profile: response.Profile.PacingProfile(), mode: videoindexerstudio.NarrativePacingClassifierModeFoundryStructured}
			}
			return deterministicNarrativePacingResolution(intent, narrativePacingFallbackReason(classifyErr))
		}
	}
	return deterministicNarrativePacingResolution(intent, videoindexerstudio.NarrativePacingClassifierFallbackUnavailable)
}

func deterministicNarrativePacingResolution(intent string, reason videoindexerstudio.NarrativePacingClassifierFallbackReason) narrativePacingResolution {
	profile := videoindexerstudio.NarrativePacingProfileForIntent(intent)
	mode := videoindexerstudio.NarrativePacingClassifierModeDeterministicKeywordFallback
	if profile == videoindexerstudio.NarrativePacingProfileStandard {
		mode = videoindexerstudio.NarrativePacingClassifierModeStandardFallback
	}
	return narrativePacingResolution{profile: profile, mode: mode, fallbackReason: reason}
}

func narrativePacingFallbackReason(err error) videoindexerstudio.NarrativePacingClassifierFallbackReason {
	if code := narrativeAPIErrorCode(err); code != "" {
		switch code {
		case "narrative_intent_classification_unavailable":
			return videoindexerstudio.NarrativePacingClassifierFallbackUnavailable
		case "narrative_intent_classification_timeout":
			return videoindexerstudio.NarrativePacingClassifierFallbackTimeout
		case "narrative_intent_classification_invalid_response", "narrative_intent_classification_invalid", "narrative_intent_classification_request_limit":
			return videoindexerstudio.NarrativePacingClassifierFallbackInvalidResponse
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return videoindexerstudio.NarrativePacingClassifierFallbackTimeout
	}
	return videoindexerstudio.NarrativePacingClassifierFallbackRequestFailed
}

func narrativeAPIErrorCode(err error) string {
	type apiErrorCarrier interface {
		APIError() videoindexerstudio.APIErrorResponse
	}
	var carrier apiErrorCarrier
	if errors.As(err, &carrier) {
		return strings.TrimSpace(carrier.APIError().Code)
	}
	return ""
}
func buildMultiVideoComposition(compositionID string, assetIDs []string, dependencies []VideoIndexerStudioJob) (videoindexerstudio.EditPlan, videoindexerstudio.CompositionEditPlan, []videoindexerstudio.TimelineDraft, error) {
	return buildMultiVideoCompositionWithIntent(compositionID, assetIDs, dependencies, "")
}

func buildMultiVideoCompositionWithIntent(compositionID string, assetIDs []string, dependencies []VideoIndexerStudioJob, narrativeIntent string) (videoindexerstudio.EditPlan, videoindexerstudio.CompositionEditPlan, []videoindexerstudio.TimelineDraft, error) {
	return buildMultiVideoCompositionWithPacing(compositionID, assetIDs, dependencies, narrativeIntent, narrativePacingResolution{
		profile: videoindexerstudio.NarrativePacingProfileForIntent(narrativeIntent),
		mode:    videoindexerstudio.NarrativePacingClassifierModeDeterministicKeywordFallback,
	})
}

func buildMultiVideoCompositionWithPacing(compositionID string, assetIDs []string, dependencies []VideoIndexerStudioJob, narrativeIntent string, resolution narrativePacingResolution) (videoindexerstudio.EditPlan, videoindexerstudio.CompositionEditPlan, []videoindexerstudio.TimelineDraft, error) {
	return buildMultiVideoCompositionCore(compositionID, assetIDs, dependencies, narrativeIntent, resolution, true)
}

func buildMultiVideoCompositionCore(compositionID string, assetIDs []string, dependencies []VideoIndexerStudioJob, narrativeIntent string, resolution narrativePacingResolution, applyPacing bool) (videoindexerstudio.EditPlan, videoindexerstudio.CompositionEditPlan, []videoindexerstudio.TimelineDraft, error) {
	profile := resolution.profile
	if len(assetIDs) < 2 || len(dependencies) != len(assetIDs) {
		return videoindexerstudio.EditPlan{}, videoindexerstudio.CompositionEditPlan{}, nil, errors.New("composition requires a completed analysis for every selected asset")
	}

	candidates := make([]compositionCandidate, 0, len(dependencies))
	sources := make([]videoindexerstudio.CompositionSourceStatus, 0, len(dependencies))
	for i, dependency := range dependencies {
		if dependency.VideoIndexResult == nil || dependency.VideoIndexResult.DurationMs <= 0 {
			return videoindexerstudio.EditPlan{}, videoindexerstudio.CompositionEditPlan{}, nil, fmt.Errorf("analysis %q has incomplete grounded evidence", dependency.ID)
		}
		if dependency.EditPlan == nil || len(dependency.EditPlan.Suggestions) == 0 {
			return videoindexerstudio.EditPlan{}, videoindexerstudio.CompositionEditPlan{}, nil, fmt.Errorf("analysis %q has no edit suggestions", dependency.ID)
		}
		for _, suggestion := range dependency.EditPlan.Suggestions {
			sourceClips := suggestion.Clips
			if len(sourceClips) == 0 {
				sourceClips = []videoindexerstudio.SuggestedClip{{ID: suggestion.ID, Title: suggestion.Title, Reason: suggestion.Reason, StartMs: suggestion.StartMs, EndMs: suggestion.EndMs, Score: suggestion.Score, SourceRefs: suggestion.SourceRefs}}
			}
			for clipIndex, clip := range sourceClips {
				if clip.StartMs < 0 || clip.EndMs <= clip.StartMs || clip.EndMs > dependency.VideoIndexResult.DurationMs {
					return videoindexerstudio.EditPlan{}, videoindexerstudio.CompositionEditPlan{}, nil, fmt.Errorf("analysis %q contains an invalid grounded clip range", dependency.ID)
				}
				clip.SourceRefs = append([]videoindexerstudio.SourceRef(nil), clip.SourceRefs...)
				clip.ID = stableCompositionClipID(compositionID, assetIDs[i], suggestion.ID, clipIndex, clip)
				clip.SourceAssetID = assetIDs[i]
				for refIndex := range clip.SourceRefs {
					clip.SourceRefs[refIndex].SourceAssetID = assetIDs[i]
					clip.SourceRefs[refIndex].RefID = fmt.Sprintf("%s:%s", sanitizeIdentifier(assetIDs[i]), clip.SourceRefs[refIndex].RefID)
				}
				candidates = append(candidates, compositionCandidate{clip: clip, suggestionID: suggestion.ID, sourceStartMS: clip.StartMs})
			}
		}
		sources = append(sources, videoindexerstudio.CompositionSourceStatus{AssetID: assetIDs[i], AnalysisJobID: dependency.ID, Status: "complete", DurationMs: dependency.VideoIndexResult.DurationMs})
	}
	if len(candidates) == 0 {
		return videoindexerstudio.EditPlan{}, videoindexerstudio.CompositionEditPlan{}, nil, errors.New("selected analyses did not contain usable clips")
	}
	sort.Slice(sources, func(i, j int) bool { return sources[i].AssetID < sources[j].AssetID })
	variantCount := 0
	if applyPacing {
		candidates, variantCount = applyNarrativePacing(candidates, profile)
	}
	sortPacedCompositionCandidates(candidates, profile)
	candidates = selectCompositionCandidates(candidates)
	if len(candidates) > narrativeMaxCandidates {
		candidates = candidates[:narrativeMaxCandidates]
	}
	clips := make([]videoindexerstudio.SuggestedClip, 0, len(candidates))
	compositionClips := make([]videoindexerstudio.CompositionClip, 0, len(candidates))
	refs := make([]videoindexerstudio.SourceRef, 0, len(candidates))
	for _, candidate := range candidates {
		clip := candidate.clip
		clips = append(clips, clip)
		compositionClips = append(compositionClips, videoindexerstudio.CompositionClip{ID: clip.ID, SourceAssetID: clip.SourceAssetID, SuggestionID: candidate.suggestionID, Title: clip.Title, Reason: clip.Reason, StartMs: clip.StartMs, EndMs: clip.EndMs, Score: clip.Score, SourceRefs: append([]videoindexerstudio.SourceRef(nil), clip.SourceRefs...)})
		refs = append(refs, clip.SourceRefs...)
	}
	var duration int64
	for _, clip := range clips {
		duration += clip.EndMs - clip.StartMs
	}
	canonicalAssetIDs := append([]string(nil), assetIDs...)
	sort.Strings(canonicalAssetIDs)
	suggestion := videoindexerstudio.EditSuggestion{ID: "multi-video-narrative", Title: "Multi-video narrative", Reason: "Orders grounded clips by their validated highlight score.", StartMs: 0, EndMs: duration, Score: averageClipScore(clips), SourceRefs: refs, Clips: clips}
	plan := videoindexerstudio.EditPlan{SchemaVersion: 1, AssetID: canonicalAssetIDs[0], SourceAssetIDs: canonicalAssetIDs, Title: "Multi-video smart edit", Summary: fmt.Sprintf("A grounded narrative edit using all %d selected videos.", len(assetIDs)), Suggestions: []videoindexerstudio.EditSuggestion{suggestion}, SourceRefs: refs}
	compositionPlan := videoindexerstudio.CompositionEditPlan{SchemaVersion: videoindexerstudio.CompositionEditPlanSchemaVersion, CompositionID: compositionID, NarrativeIntent: narrativeIntent, PacingProfile: profile, VariantCount: variantCount, PacingClassifierMode: resolution.mode, PacingFallbackReason: resolution.fallbackReason, Title: plan.Title, Summary: plan.Summary, RankingMode: "deterministic_grounded_v1", RecommendationVersion: "multi-video-composition-v2", EvidenceFingerprint: compositionEvidenceFingerprint(canonicalAssetIDs, sources, compositionClips), SourceAssetIDs: canonicalAssetIDs, Sources: sources, Clips: compositionClips, SourceRefs: refs}
	draft, err := timelineDraftFromSuggestion(compositionID, suggestion)
	if err != nil {
		return videoindexerstudio.EditPlan{}, videoindexerstudio.CompositionEditPlan{}, nil, err
	}
	return plan, compositionPlan, []videoindexerstudio.TimelineDraft{draft}, nil
}

type compositionCandidate struct {
	clip          videoindexerstudio.SuggestedClip
	suggestionID  string
	sourceStartMS int64
}

const compositionMaterialOverlapPercent int64 = 80

func selectCompositionCandidates(candidates []compositionCandidate) []compositionCandidate {
	selected := make([]compositionCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if compositionCandidateOverlapsSelected(candidate, selected) {
			continue
		}
		selected = append(selected, candidate)
	}
	return selected
}

func compositionCandidateOverlapsSelected(candidate compositionCandidate, selected []compositionCandidate) bool {
	for _, accepted := range selected {
		if accepted.clip.SourceAssetID != candidate.clip.SourceAssetID {
			continue
		}
		overlapStart := candidate.clip.StartMs
		if accepted.clip.StartMs > overlapStart {
			overlapStart = accepted.clip.StartMs
		}
		overlapEnd := candidate.clip.EndMs
		if accepted.clip.EndMs < overlapEnd {
			overlapEnd = accepted.clip.EndMs
		}
		if overlapEnd <= overlapStart {
			continue
		}
		shorterDuration := candidate.clip.EndMs - candidate.clip.StartMs
		if acceptedDuration := accepted.clip.EndMs - accepted.clip.StartMs; acceptedDuration < shorterDuration {
			shorterDuration = acceptedDuration
		}
		if (overlapEnd-overlapStart)*100 >= shorterDuration*compositionMaterialOverlapPercent {
			return true
		}
	}
	return false
}
func stableCompositionClipID(compositionID, assetID, suggestionID string, index int, clip videoindexerstudio.SuggestedClip) string {
	input := strings.Join([]string{strings.TrimSpace(compositionID), strings.TrimSpace(assetID), strings.TrimSpace(suggestionID), strings.TrimSpace(clip.ID), fmt.Sprintf("%d", index), fmt.Sprintf("%d", clip.StartMs), fmt.Sprintf("%d", clip.EndMs)}, "\x1f")
	sum := sha256.Sum256([]byte(input))
	return "clip-" + hex.EncodeToString(sum[:16])
}

func compositionEvidenceFingerprint(assetIDs []string, sources []videoindexerstudio.CompositionSourceStatus, clips []videoindexerstudio.CompositionClip) string {
	parts := append([]string(nil), assetIDs...)
	sort.Strings(parts)
	for _, source := range sources {
		parts = append(parts, source.AssetID, source.AnalysisJobID, source.Status, fmt.Sprintf("%d", source.DurationMs))
	}
	for _, clip := range clips {
		parts = append(parts, clip.ID, clip.SourceAssetID, clip.SuggestionID, fmt.Sprintf("%d", clip.StartMs), fmt.Sprintf("%d", clip.EndMs), fmt.Sprintf("%.8f", clip.Score))
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x1f")))
	return hex.EncodeToString(sum[:])
}

func bestEditSuggestion(suggestions []videoindexerstudio.EditSuggestion) videoindexerstudio.EditSuggestion {
	best := suggestions[0]
	for _, candidate := range suggestions[1:] {
		if candidate.Score > best.Score {
			best = candidate
		}
	}
	return best
}

func averageClipScore(clips []videoindexerstudio.SuggestedClip) float64 {
	var total float64
	for _, clip := range clips {
		total += clip.Score
	}
	return total / float64(len(clips))
}

func timelineDraftFromSuggestion(originJobID string, suggestion videoindexerstudio.EditSuggestion) (videoindexerstudio.TimelineDraft, error) {
	clips := make([]videoindexerstudio.TimelineClip, 0, len(suggestion.Clips))
	var timelineStart int64
	for _, source := range suggestion.Clips {
		duration := source.EndMs - source.StartMs
		clips = append(clips, videoindexerstudio.TimelineClip{ID: source.ID, SourceAssetID: source.SourceAssetID, InMS: source.StartMs, OutMS: source.EndMs, TimelineStartMS: timelineStart, DurationMS: duration, Transition: videoindexerstudio.TimelineTransition{Kind: videoindexerstudio.TimelineTransitionKindCut}})
		timelineStart += duration
	}
	draft := videoindexerstudio.TimelineDraft{SchemaVersion: videoindexerstudio.TimelineDraftSchemaVersion, OriginJobID: originJobID, SuggestionID: suggestion.ID, PromptVersion: "multi-video-composition-v2", PrimaryVideoTrack: videoindexerstudio.TimelineTrack{ID: "primary-video", Kind: videoindexerstudio.TimelineTrackKindVideo, Clips: clips}}
	return draft, draft.Validate()
}

func uniqueNonEmptyStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func (s *VideoIndexerStudioService) CreateEditProject(ctx context.Context, jobID, suggestionID string) (editing.EditProject, error) {
	if s.editing == nil {
		return editing.EditProject{}, errors.New("editing service is not configured")
	}
	job, err := s.loadJob(ctx, jobID)
	if err != nil {
		return editing.EditProject{}, err
	}
	draft, err := selectTimelineDraft(job, suggestionID)
	if err != nil {
		return editing.EditProject{}, err
	}
	if err := validateCompositionProjectDraft(job, draft); err != nil {
		return editing.EditProject{}, err
	}
	project, err := editing.ProjectFromTimelineDraft(draft)
	if err != nil {
		return editing.EditProject{}, err
	}

	selectedSuggestionID := strings.TrimSpace(suggestionID)
	if selectedSuggestionID == "" {
		selectedSuggestionID = strings.TrimSpace(draft.SuggestionID)
	}
	project.ID = deterministicProjectID(jobID, selectedSuggestionID)
	if project.Name == "" {
		if name := strings.TrimSpace(job.AssetName); name != "" {
			if selectedSuggestionID != "" {
				project.Name = fmt.Sprintf("%s - %s", name, selectedSuggestionID)
			} else {
				project.Name = name
			}
		} else {
			project.Name = fmt.Sprintf("Video Indexer %s", jobID)
		}
	}

	saved, err := s.editing.SaveProject(ctx, project)
	if err != nil {
		return editing.EditProject{}, err
	}
	job.SuggestionID = selectedSuggestionID
	job.ProjectID = saved.ID
	job.UpdatedAt = s.nowTime()
	if err := s.saveJob(ctx, job); err != nil {
		return editing.EditProject{}, err
	}
	return saved, nil
}

func validateCompositionProjectDraft(job VideoIndexerStudioJob, draft videoindexerstudio.TimelineDraft) error {
	if !job.Composition {
		return nil
	}
	if job.Status != videoIndexerJobStatusSucceeded {
		return fmt.Errorf("composition job %q must succeed before creating an edit project", job.ID)
	}
	if job.CompositionPlan == nil {
		if len(job.TimelineDrafts) != 1 {
			return fmt.Errorf("legacy composition job %q must contain exactly one timeline draft", job.ID)
		}
		return nil
	}
	plan := job.CompositionPlan
	if plan.CompositionID != job.ID || draft.OriginJobID != job.ID || len(draft.PrimaryVideoTrack.Clips) != len(plan.Clips) {
		return fmt.Errorf("composition job %q has a timeline draft that does not match its recommendation", job.ID)
	}
	for index, timelineClip := range draft.PrimaryVideoTrack.Clips {
		recommendation := plan.Clips[index]
		if timelineClip.ID != recommendation.ID ||
			timelineClip.SourceAssetID != recommendation.SourceAssetID ||
			timelineClip.InMS != recommendation.StartMs ||
			timelineClip.OutMS != recommendation.EndMs ||
			timelineClip.Transition.Kind != videoindexerstudio.TimelineTransitionKindCut ||
			timelineClip.Transition.DurationMS != 0 {
			return fmt.Errorf("composition job %q has a timeline draft that does not match recommendation clip %d", job.ID, index+1)
		}
	}
	return nil
}

func (s *VideoIndexerStudioService) loadJob(ctx context.Context, jobID string) (VideoIndexerStudioJob, error) {
	if err := s.ensureJobsLoaded(ctx); err != nil {
		return VideoIndexerStudioJob{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[strings.TrimSpace(jobID)]
	if !ok {
		return VideoIndexerStudioJob{}, fmt.Errorf("video indexer job %q not found", jobID)
	}
	return job, nil
}

func (s *VideoIndexerStudioService) saveJob(ctx context.Context, job VideoIndexerStudioJob) error {
	if err := s.ensureJobsLoaded(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.jobs == nil {
		s.jobs = map[string]VideoIndexerStudioJob{}
	}
	if job.ID == "" {
		return errors.New("video indexer job id is required")
	}
	s.jobs[job.ID] = job
	return s.persistJobsLocked(ctx)
}

func (s *VideoIndexerStudioService) ensureJobsLoaded(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	if s.loaded {
		s.mu.Unlock()
		return nil
	}
	store := s.store
	s.mu.Unlock()
	if store == nil {
		s.mu.Lock()
		s.loaded = true
		s.mu.Unlock()
		return nil
	}
	jobs, err := store.Load(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loaded {
		return nil
	}
	if s.jobs == nil {
		s.jobs = map[string]VideoIndexerStudioJob{}
	}
	for _, job := range jobs {
		if job.ID == "" {
			continue
		}
		s.jobs[job.ID] = job
	}
	s.loaded = true
	return nil
}

func (s *VideoIndexerStudioService) persistJobsLocked(ctx context.Context) error {
	if s == nil || s.store == nil {
		return nil
	}
	jobs := make([]VideoIndexerStudioJob, 0, len(s.jobs))
	for _, job := range s.jobs {
		jobs = append(jobs, job)
	}
	sort.Slice(jobs, func(i, j int) bool {
		if jobs[i].CreatedAt.Equal(jobs[j].CreatedAt) {
			return jobs[i].ID > jobs[j].ID
		}
		return jobs[i].CreatedAt.After(jobs[j].CreatedAt)
	})
	return s.store.Save(ctx, jobs)
}

func (s *VideoIndexerStudioService) refreshJob(ctx context.Context, jobID string) (VideoIndexerStudioJob, error) {
	client, err := s.clientFor(ctx)
	if err != nil {
		return VideoIndexerStudioJob{}, err
	}
	job, err := s.loadJob(ctx, jobID)
	if err != nil {
		return VideoIndexerStudioJob{}, err
	}
	if isTerminalVideoIndexerStatus(job.Status) || strings.TrimSpace(job.RemoteJobID) == "" {
		return job, nil
	}

	resp, err := client.GetJob(ctx, job.RemoteJobID)
	if err != nil {
		job.ErrorMessage, job.Retryable = videoIndexerErrorDetails(err, true)
		job.UpdatedAt = s.nowTime()
		if !job.Retryable {
			job.Status = videoIndexerJobStatusFailed
		}
		if saveErr := s.saveJob(ctx, job); saveErr != nil {
			return VideoIndexerStudioJob{}, saveErr
		}
		if job.Retryable {
			emitVideoIndexerEvent("video-indexer:progress", job)
		} else {
			emitVideoIndexerEvent("video-indexer:failed", job)
		}
		return job, nil
	}
	job = s.mergeRemoteJob(job, resp, true)
	if err := s.saveJob(ctx, job); err != nil {
		return VideoIndexerStudioJob{}, err
	}
	emitVideoIndexerEvent(videoIndexerEventName(job.Status), job)
	return job, nil
}

func (s *VideoIndexerStudioService) resolveAsset(ctx context.Context, assetID string) (library.ProjectAsset, error) {
	if s == nil || s.library == nil {
		return library.ProjectAsset{}, errors.New("library store is not configured")
	}
	assets, err := s.library.LoadAssets(ctx)
	if err != nil {
		return library.ProjectAsset{}, fmt.Errorf("load assets: %w", err)
	}
	for i := range assets {
		if strings.TrimSpace(assets[i].ID) == strings.TrimSpace(assetID) {
			if strings.TrimSpace(assets[i].CloudAssetID) == "" {
				return library.ProjectAsset{}, fmt.Errorf("asset %q has no cloud asset id", assetID)
			}
			return assets[i], nil
		}
	}
	return library.ProjectAsset{}, fmt.Errorf("asset %q not found", assetID)
}

func (s *VideoIndexerStudioService) oneDriveClient() *onedrive.Client {
	if s == nil || s.oneDrive == nil {
		return (&OneDriveService{}).DriveClient()
	}
	return s.oneDrive.DriveClient()
}

func (s *VideoIndexerStudioService) nowTime() time.Time {
	if s == nil || s.now == nil {
		return time.Now().UTC()
	}
	return s.now()
}

func (s *VideoIndexerStudioService) clientFor(ctx context.Context) (videoIndexerClient, error) {
	if s == nil {
		return nil, fmt.Errorf("%w: video indexer service is not configured", errNotConfigured)
	}
	s.mu.Lock()
	client := s.client
	factory := s.clientFactory
	s.mu.Unlock()
	if client != nil {
		return client, nil
	}
	if factory != nil {
		return factory(ctx)
	}
	return nil, fmt.Errorf("%w: video indexer service is not configured", errNotConfigured)
}

func newVideoIndexerClientFromSettings(ctx context.Context, store settings.Store) (videoIndexerClient, error) {
	if store == nil {
		return nil, fmt.Errorf("%w: video indexer service is not configured", errNotConfigured)
	}
	loaded, err := store.Load(ctx)
	if err != nil {
		return nil, err
	}
	cfg, err := videoIndexerConfigFromSettings(loaded)
	if err != nil {
		return nil, err
	}
	client, err := videoindexerstudio.NewClient(cfg, nil)
	if err != nil {
		return nil, err
	}
	return client, nil
}

func videoIndexerConfigFromSettings(cfg settings.AppSettings) (videoindexerstudio.Config, error) {
	endpoint, err := videoindexerstudio.NormalizeEndpoint(cfg.VideoIndexerServiceEndpoint)
	if err != nil {
		return videoindexerstudio.Config{}, err
	}
	apiKey := strings.TrimSpace(cfg.VideoIndexerServiceAPIKey)
	if endpoint == "" || apiKey == "" {
		return videoindexerstudio.Config{}, fmt.Errorf("%w: video indexer service endpoint and API key are not configured", errNotConfigured)
	}
	return videoindexerstudio.Config{Endpoint: endpoint, APIKey: apiKey}, nil
}

func isTerminalVideoIndexerStatus(status string) bool {
	switch status {
	case videoIndexerJobStatusSucceeded, videoIndexerJobStatusFailed, videoIndexerJobStatusCanceled:
		return true
	default:
		return false
	}
}

func (s *VideoIndexerStudioService) mergeRemoteJob(job VideoIndexerStudioJob, resp *videoindexerstudio.JobResponse, updateLifecycle bool) VideoIndexerStudioJob {
	if resp == nil {
		return job
	}
	remote := resp.Job
	if remote.ID != "" {
		job.RemoteJobID = remote.ID
	}
	job.RemoteStatus = string(remote.Status)
	job.Stage = firstNonEmpty(remote.VideoIndexerState, string(remote.Status))
	if remote.VideoIndexerVideoID != "" {
		job.VideoIndexerVideoID = remote.VideoIndexerVideoID
	}
	if remote.VideoIndexResult != nil {
		job.VideoIndexResult = remote.VideoIndexResult
	}
	if remote.EditPlan != nil {
		job.EditPlan = remote.EditPlan
	}
	if remote.TimelineDrafts != nil {
		job.TimelineDrafts = append([]videoindexerstudio.TimelineDraft(nil), remote.TimelineDrafts...)
	}
	if remote.Checkpoints != nil {
		job.Checkpoints = append([]videoindexerstudio.JobCheckpoint(nil), remote.Checkpoints...)
	}
	if !remote.CreatedAt.IsZero() {
		job.CreatedAt = remote.CreatedAt
	}
	if !remote.UpdatedAt.IsZero() {
		job.UpdatedAt = remote.UpdatedAt
	} else if updateLifecycle {
		job.UpdatedAt = s.nowTime()
	}
	if remote.StartedAt != nil {
		job.StartedAt = remote.StartedAt
	}
	if remote.CompletedAt != nil {
		job.CompletedAt = remote.CompletedAt
	}
	if strings.TrimSpace(remote.ClaimedBy) != "" {
		job.ClaimedBy = strings.TrimSpace(remote.ClaimedBy)
	}
	if remote.Error != nil {
		job.ErrorMessage = strings.TrimSpace(videoIndexerSanitizeErrorMessage(remote.Error.Message))
		job.Retryable = remote.Error.Retryable
	} else if updateLifecycle {
		job.ErrorMessage = ""
		job.Retryable = false
	}
	if updateLifecycle {
		job.Status = mapRemoteStatus(remote.Status, job.Status)
		return job
	}
	switch remote.Status {
	case videoindexerstudio.JobStatusSucceeded:
		job.Status = videoIndexerJobStatusSucceeded
	case videoindexerstudio.JobStatusFailed:
		job.Status = videoIndexerJobStatusFailed
	case videoindexerstudio.JobStatusCanceled:
		job.Status = videoIndexerJobStatusCanceled
	}
	return job
}

func videoIndexerErrorDetails(err error, defaultRetryable bool) (string, bool) {
	type apiErrorCarrier interface {
		APIError() videoindexerstudio.APIErrorResponse
	}
	var carrier apiErrorCarrier
	if errors.As(err, &carrier) {
		apiErr := carrier.APIError()
		if message := strings.TrimSpace(apiErr.Message); message != "" {
			return videoIndexerSanitizeErrorMessage(message), apiErr.Retryable
		}
		return videoIndexerSanitizeErrorMessage(err.Error()), apiErr.Retryable
	}
	return videoIndexerSanitizeErrorMessage(err.Error()), defaultRetryable
}

func videoIndexerSanitizeErrorMessage(message string) string {
	return strings.TrimSpace(message)
}

func videoIndexerEventName(status string) string {
	switch status {
	case videoIndexerJobStatusSucceeded:
		return "video-indexer:completed"
	case videoIndexerJobStatusFailed, videoIndexerJobStatusCanceled:
		return "video-indexer:failed"
	default:
		return "video-indexer:progress"
	}
}

func mapRemoteStatus(remote videoindexerstudio.JobStatus, current string) string {
	switch remote {
	case videoindexerstudio.JobStatusSucceeded:
		return videoIndexerJobStatusSucceeded
	case videoindexerstudio.JobStatusFailed:
		return videoIndexerJobStatusFailed
	case videoindexerstudio.JobStatusCanceled:
		return videoIndexerJobStatusCanceled
	case videoindexerstudio.JobStatusQueued,
		videoindexerstudio.JobStatusStaging,
		videoindexerstudio.JobStatusStaged,
		videoindexerstudio.JobStatusProcessing,
		videoindexerstudio.JobStatusSubmitting,
		videoindexerstudio.JobStatusIndexing,
		videoindexerstudio.JobStatusNormalizing,
		videoindexerstudio.JobStatusGenerating,
		videoindexerstudio.JobStatusBuildingTimeline,
		videoindexerstudio.JobStatusRunning:
		if current == videoIndexerJobStatusSubmitted {
			return videoIndexerJobStatusPolling
		}
		return videoIndexerJobStatusPolling
	default:
		return current
	}
}

func selectTimelineDraft(job VideoIndexerStudioJob, suggestionID string) (videoindexerstudio.TimelineDraft, error) {
	if len(job.TimelineDrafts) == 0 {
		return videoindexerstudio.TimelineDraft{}, fmt.Errorf("video indexer job %q has no timeline drafts", job.ID)
	}
	suggestionID = strings.TrimSpace(suggestionID)
	if suggestionID == "" {
		if len(job.TimelineDrafts) == 1 {
			return job.TimelineDrafts[0], nil
		}
		for _, draft := range job.TimelineDrafts {
			if strings.TrimSpace(draft.SuggestionID) == strings.TrimSpace(job.SuggestionID) && draft.SuggestionID != "" {
				return draft, nil
			}
		}
		return videoindexerstudio.TimelineDraft{}, fmt.Errorf("video indexer job %q requires an explicit suggestion id", job.ID)
	}
	for _, draft := range job.TimelineDrafts {
		if strings.TrimSpace(draft.SuggestionID) == suggestionID {
			return draft, nil
		}
	}
	return videoindexerstudio.TimelineDraft{}, fmt.Errorf("video indexer job %q does not contain suggestion %q", job.ID, suggestionID)
}

func deterministicProjectID(jobID, suggestionID string) string {
	jobID = sanitizeIdentifier(jobID)
	suggestionID = sanitizeIdentifier(suggestionID)
	if suggestionID == "" {
		return "video-indexer-" + jobID
	}
	return "video-indexer-" + jobID + "-" + suggestionID
}

func sanitizeIdentifier(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r
		case r >= '0' && r <= '9':
			return r
		case r == '-', r == '_':
			return r
		default:
			return '-'
		}
	}, value)
	value = strings.Trim(value, "-_")
	if value == "" {
		return "item"
	}
	return value
}

func emitVideoIndexerEvent(name string, job VideoIndexerStudioJob) {
	app := application.Get()
	if app == nil || app.Event == nil {
		return
	}
	app.Event.Emit(name, job)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
