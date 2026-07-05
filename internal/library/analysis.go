package library

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/zecloud/ai-video-studio/internal/mediaservice"
	"github.com/zecloud/ai-video-studio/internal/onedrive"
)

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
	ID           string                       `json:"id"`
	AssetID      string                       `json:"assetId"`
	AssetName    string                       `json:"assetName"`
	JobID        string                       `json:"jobId,omitempty"`   // CU operation ID (from media service)
	Status       string                       `json:"status"`
	ErrorMessage string                       `json:"errorMessage,omitempty"`
	Result       *mediaservice.AnalyzeResult  `json:"result,omitempty"`
	CreatedAt    time.Time                    `json:"createdAt"`
	UpdatedAt    time.Time                    `json:"updatedAt"`
}

// AnalyzeBackend is the subset of mediaservice.Client that the analysis
// engine needs. Using an interface keeps the engine testable without
// importing the full mediaservice package in tests.
type AnalyzeBackend interface {
	Analyze(ctx context.Context, req mediaservice.AnalyzeRequest) (*mediaservice.AnalyzeResult, error)
}

// AnalysisEngine orchestrates the analysis pipeline by delegating the full
// flow (OneDrive → Blob → CU → result) to the remote Azure Media Service.
type AnalysisEngine struct {
	mu          sync.Mutex
	store       Store
	odClient    *onedrive.Client
	mediaBackend AnalyzeBackend
	now         func() time.Time
}

// NewAnalysisEngine creates a configured engine. store, mediaBackend and odClient
// are mandatory; odClient provides the OneDrive token needed for media service
// authorization.
func NewAnalysisEngine(store Store, mediaBackend AnalyzeBackend, odClient *onedrive.Client) *AnalysisEngine {
	return &AnalysisEngine{
		store:        store,
		mediaBackend: mediaBackend,
		odClient:     odClient,
		now:          func() time.Time { return time.Now().UTC() },
	}
}

// SubmitAsset delegates the full analysis pipeline to the remote Azure Media
// Service. The media service handles OneDrive→Blob→CU→result and returns
// the parsed analysis. This method blocks; callers may want to wrap it in a
// goroutine for UI responsiveness.
func (e *AnalysisEngine) SubmitAsset(ctx context.Context, asset ProjectAsset) (*AnalysisJob, error) {
	if e.store == nil {
		return nil, fmt.Errorf("analysis engine: store is nil")
	}
	if e.mediaBackend == nil {
		return nil, fmt.Errorf("analysis engine: media backend is not configured")
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
	job := &AnalysisJob{
		ID:        fmt.Sprintf("analysis-%d", now.UnixNano()),
		AssetID:   asset.ID,
		AssetName: asset.Name,
		Status:    AnalysisPending,
		CreatedAt: now,
		UpdatedAt: now,
	}
	e.storeJob(job)

	// Obtain a OneDrive access token for the media service to read the
	// source item on our behalf.
	var token string
	if e.odClient != nil && e.odClient.TokenProvider != nil {
		tok, err := e.odClient.TokenProvider.AccessToken(ctx, e.odClient.Scopes)
		if err != nil {
			job.Status = AnalysisFailed
			job.ErrorMessage = fmt.Sprintf("OneDrive token: %v", err)
			e.storeJob(job)
			return job, fmt.Errorf("analysis engine: %w", err)
		}
		token = tok
	}

	result, err := e.mediaBackend.Analyze(ctx, mediaservice.AnalyzeRequest{
		OneDriveItemID: cloudID,
		OneDriveToken:  token,
		AssetID:        asset.ID,
		AssetName:      asset.Name,
	})
	if err != nil {
		job.Status = AnalysisFailed
		job.ErrorMessage = fmt.Sprintf("media service analysis failed: %v", err)
		job.UpdatedAt = e.now()
		e.storeJob(job)
		return job, fmt.Errorf("analysis engine: submit to media service: %w", err)
	}

	job.Status = AnalysisSucceeded
	job.Result = result
	job.UpdatedAt = e.now()
	e.storeJob(job)

	e.updateAssetStatus(ctx, asset, job.ID, AnalysisSucceeded, len(result.Scenes))
	return job, nil
}

func (e *AnalysisEngine) storeJob(job *AnalysisJob) {
	if job == nil {
		return
	}
	job.UpdatedAt = e.now()
	_ = e.store.SaveJob(context.Background(), *job)
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

// GetJobs returns all analysis jobs.
func (e *AnalysisEngine) GetJobs(ctx context.Context) ([]AnalysisJob, error) {
	return e.store.LoadJobs(ctx)
}

// GetAssetAnalysis returns the most recent analysis job for an asset.
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
