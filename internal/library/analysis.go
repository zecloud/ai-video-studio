package library

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	cu "github.com/zecloud/ai-video-studio/internal/contentunderstanding"
	"github.com/zecloud/ai-video-studio/internal/mediaservice"
	"github.com/zecloud/ai-video-studio/internal/onedrive"
)

// mediaStagingContainer is the Azure Blob container used to stage OneDrive
// assets for Content Understanding analysis.
const mediaStagingContainer = "media-staging"

// AnalysisStatus values
const (
	AnalysisPending   = "pending"
	AnalysisSubmitted = "submitted"
	AnalysisPolling   = "polling"
	AnalysisSucceeded = "succeeded"
	AnalysisFailed    = "failed"
)

// AnalysisJob tracks a single analysis job from submission through result retrieval.
type AnalysisJob struct {
	ID                string             `json:"id"`
	AssetID           string             `json:"assetId"`
	AssetName         string             `json:"assetName"`
	JobID             string             `json:"jobId,omitempty"`             // CU operation ID
	OperationLocation string             `json:"operationLocation,omitempty"` // CU polling URL
	Status            string             `json:"status"`
	ErrorMessage      string             `json:"errorMessage,omitempty"`
	Result            *cu.AnalysisResult `json:"result,omitempty"`
	BlobName          string             `json:"blobName,omitempty"`
	BlobURL           string             `json:"blobUrl,omitempty"`
	CreatedAt         time.Time          `json:"createdAt"`
	UpdatedAt         time.Time          `json:"updatedAt"`
}

// AnalysisEngine orchestrates the analysis pipeline: Azure Media Staging copy
// → CU submit → poll → store → blob cleanup.
type AnalysisEngine struct {
	mu          sync.Mutex
	store       Store
	cuClient    cu.Service
	odClient    *onedrive.Client
	mediaClient *mediaservice.Client
	now         func() time.Time
}

// NewAnalysisEngine creates a configured engine. Only store is mandatory;
// cuClient, odClient, and mediaClient may be nil and can be set later.
func NewAnalysisEngine(store Store, cuClient cu.Service, odClient *onedrive.Client, mediaClient *mediaservice.Client) *AnalysisEngine {
	return &AnalysisEngine{
		store:       store,
		cuClient:    cuClient,
		odClient:    odClient,
		mediaClient: mediaClient,
		now:         func() time.Time { return time.Now().UTC() },
	}
}

// SubmitAsset copies the asset from OneDrive into Azure Blob staging via the
// Azure Media Staging Service, submits the resulting SAS URL to Azure Content
// Understanding, stores the job, and starts background polling. It returns
// the job ID and any pre-submission error.
func (e *AnalysisEngine) SubmitAsset(ctx context.Context, asset ProjectAsset, onProgress func(job AnalysisJob), onComplete func(job AnalysisJob)) (*AnalysisJob, error) {
	if e.store == nil {
		return nil, fmt.Errorf("analysis engine: store is nil")
	}
	if e.cuClient == nil {
		return nil, fmt.Errorf("analysis engine: CU client is nil")
	}
	if e.odClient == nil {
		return nil, fmt.Errorf("analysis engine: OneDrive client is nil")
	}
	if e.mediaClient == nil {
		return nil, fmt.Errorf("analysis engine: media staging service is not configured")
	}

	cloudID := strings.TrimSpace(asset.CloudAssetID)
	if cloudID == "" {
		return nil, fmt.Errorf("analysis engine: asset %q has no CloudAssetID", asset.ID)
	}

	if existingJobs, err := e.store.LoadJobs(ctx); err == nil {
		for _, existing := range existingJobs {
			if existing.AssetID != asset.ID {
				continue
			}
			switch existing.Status {
			case AnalysisPending, AnalysisSubmitted, AnalysisPolling:
				return nil, fmt.Errorf("analysis engine: an analysis job is already in progress for asset %q (job %s, status %s)", asset.ID, existing.ID, existing.Status)
			}
		}
	}

	now := e.now()
	job := AnalysisJob{
		ID:        fmt.Sprintf("analysis-%d", now.UnixNano()),
		AssetID:   asset.ID,
		AssetName: asset.Name,
		Status:    AnalysisPending,
		CreatedAt: now,
		UpdatedAt: now,
	}

	// Step 1: Obtain a OneDrive access token for the media staging service to
	// use when reading the source item on our behalf.
	if e.odClient.TokenProvider == nil {
		job.Status = AnalysisFailed
		job.ErrorMessage = "OneDrive token provider is not configured"
		e.storeJob(&job)
		return &job, fmt.Errorf("analysis engine: %s", job.ErrorMessage)
	}
	token, err := e.odClient.TokenProvider.AccessToken(ctx, e.odClient.Scopes)
	if err != nil {
		job.Status = AnalysisFailed
		job.ErrorMessage = fmt.Sprintf("OneDrive token: %v", err)
		e.storeJob(&job)
		return &job, fmt.Errorf("analysis engine: %w", err)
	}

	// Step 2: Copy the OneDrive item into Azure Blob staging.
	blobName := fmt.Sprintf("analysis/%s/%s", asset.ID, asset.Name)
	copyResult, err := e.mediaClient.CopyToBlob(ctx, cloudID, token, blobName, mediaStagingContainer)
	if err != nil {
		job.Status = AnalysisFailed
		job.ErrorMessage = fmt.Sprintf("media staging copy: %v", err)
		e.storeJob(&job)
		return &job, fmt.Errorf("analysis engine: %w", err)
	}
	job.BlobName = blobName
	job.BlobURL = copyResult.BlobURL

	// Step 3: Submit to CU using the staged SAS URL.
	videoAsset := cu.VideoAsset{
		ID:           asset.ID,
		CloudAssetID: cloudID,
		Name:         asset.Name,
		SourceURL:    copyResult.SASURL,
	}
	submitted, err := e.cuClient.Submit(ctx, videoAsset)
	if err != nil {
		job.Status = AnalysisFailed
		job.ErrorMessage = fmt.Sprintf("CU submit: %v", err)
		e.storeJob(&job)
		e.cleanupBlob(job)
		return &job, fmt.Errorf("analysis engine: CU submit: %w", err)
	}

	job.JobID = submitted
	job.Status = AnalysisSubmitted
	job.UpdatedAt = e.now()
	e.storeJob(&job)

	// Mark the asset
	e.updateAssetStatus(ctx, asset, job.JobID, AnalysisSubmitted, 0)

	// Step 4: Start background polling
	go e.pollBackground(ctx, job, onProgress, onComplete)

	return &job, nil
}

// cleanupBlob deletes the staged blob for a job, if any. Cleanup errors are
// swallowed (logged) since the SAS URL expires on its own and cleanup is
// best-effort. A fresh context is used since the caller's context may already
// be canceled or scoped to a request that has since completed.
func (e *AnalysisEngine) cleanupBlob(job AnalysisJob) {
	if e.mediaClient == nil || job.BlobName == "" {
		return
	}
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := e.mediaClient.DeleteBlob(cleanupCtx, job.BlobName, mediaStagingContainer); err != nil {
		fmt.Printf("analysis engine: cleanup blob %q failed: %v\n", job.BlobName, err)
	}
}

// pollBackground polls Azure CU until the job completes, updating the store and
// calling callbacks for UI events.
func (e *AnalysisEngine) pollBackground(ctx context.Context, job AnalysisJob, onProgress func(AnalysisJob), onComplete func(AnalysisJob)) {
	const maxRetries = 3
	var lastErr error

	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * 2 * time.Second)
		}

		e.mu.Lock()
		job.Status = AnalysisPolling
		job.UpdatedAt = e.now()
		e.storeJob(&job)
		e.mu.Unlock()

		if onProgress != nil {
			onProgress(job)
		}

		result, pollErr := e.doPoll(ctx, job.JobID)
		if pollErr != nil {
			lastErr = pollErr
			continue
		}

		if result.Status == "succeeded" || result.Status == "Succeeded" {
			e.mu.Lock()
			job.Status = AnalysisSucceeded
			job.Result = &result
			job.UpdatedAt = e.now()
			e.storeJob(&job)
			sceneCount := len(result.Scenes)
			e.updateAssetStatus(ctx, ProjectAsset{ID: job.AssetID}, job.JobID, AnalysisSucceeded, sceneCount)
			e.mu.Unlock()

			e.cleanupBlob(job)

			if onComplete != nil {
				onComplete(job)
			}
			return
		}

		if result.Status == "failed" || result.Status == "Failed" || result.Status == "canceled" || result.Status == "cancelled" {
			e.mu.Lock()
			job.Status = AnalysisFailed
			job.ErrorMessage = fmt.Sprintf("CU returned %s", result.Status)
			job.UpdatedAt = e.now()
			e.storeJob(&job)
			e.mu.Unlock()

			e.cleanupBlob(job)

			if onComplete != nil {
				onComplete(job)
			}
			return
		}
	}

	// All retries exhausted
	e.mu.Lock()
	job.Status = AnalysisFailed
	job.ErrorMessage = fmt.Sprintf("polling failed after %d attempts: %v", maxRetries, lastErr)
	job.UpdatedAt = e.now()
	e.storeJob(&job)
	e.updateAssetStatus(ctx, ProjectAsset{ID: job.AssetID}, job.JobID, AnalysisFailed, 0)
	e.mu.Unlock()

	e.cleanupBlob(job)

	if onComplete != nil {
		onComplete(job)
	}
}

// doPoll fetches the result from CU and returns it. It calls the CU Poll method.
func (e *AnalysisEngine) doPoll(ctx context.Context, jobID string) (cu.AnalysisResult, error) {
	return e.cuClient.GetResult(ctx, jobID)
}

// GetJob returns a single analysis job by ID.
func (e *AnalysisEngine) GetJob(ctx context.Context, jobID string) (*AnalysisJob, error) {
	jobs, err := e.store.LoadJobs(ctx)
	if err != nil {
		return nil, err
	}
	for i := range jobs {
		if jobs[i].ID == jobID {
			return &jobs[i], nil
		}
	}
	return nil, fmt.Errorf("analysis job %q not found", jobID)
}

// ListJobs returns all analysis jobs.
func (e *AnalysisEngine) ListJobs(ctx context.Context) ([]AnalysisJob, error) {
	jobs, err := e.store.LoadJobs(ctx)
	if err != nil {
		return nil, err
	}
	return jobs, nil
}

// GetAssetAnalysis returns the analysis for a given asset, if any.
func (e *AnalysisEngine) GetAssetAnalysis(ctx context.Context, assetID string) (*AnalysisJob, error) {
	jobs, err := e.store.LoadJobs(ctx)
	if err != nil {
		return nil, err
	}
	for i := range jobs {
		if jobs[i].AssetID == assetID {
			return &jobs[i], nil
		}
	}
	return nil, fmt.Errorf("no analysis job found for asset %q", assetID)
}

func (e *AnalysisEngine) storeJob(job *AnalysisJob) {
	if job == nil {
		return
	}
	job.UpdatedAt = e.now()
	// Best-effort store; errors logged at the service level
	_ = e.store.SaveJob(context.Background(), *job)
}

func (e *AnalysisEngine) updateAssetStatus(ctx context.Context, asset ProjectAsset, jobID, status string, scenes int) {
	assets, err := e.store.LoadAssets(ctx)
	if err != nil {
		return
	}
	for i := range assets {
		if assets[i].ID == asset.ID {
			assets[i].AnalysisJobID = jobID
			assets[i].AnalysisStatus = status
			if scenes > 0 {
				assets[i].AnalysisScenes = scenes
			}
			_ = e.store.SaveAssets(ctx, assets)
			return
		}
	}
}
