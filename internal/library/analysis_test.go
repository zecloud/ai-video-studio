package library

import (
	"context"
	"strings"
	"sync"
	"testing"

	cu "github.com/zecloud/ai-video-studio/internal/contentunderstanding"
	"github.com/zecloud/ai-video-studio/internal/mediaservice"
	"github.com/zecloud/ai-video-studio/internal/onedrive"
)

// memStore is a minimal in-memory Store implementation for testing the
// analysis engine without touching disk.
type memStore struct {
	mu     sync.Mutex
	assets []ProjectAsset
	jobs   []AnalysisJob
}

func (m *memStore) LoadAssets(context.Context) ([]ProjectAsset, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]ProjectAsset, len(m.assets))
	copy(out, m.assets)
	return out, nil
}

func (m *memStore) SaveAssets(_ context.Context, assets []ProjectAsset) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.assets = assets
	return nil
}

func (m *memStore) AddAsset(_ context.Context, asset ProjectAsset) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.assets = append(m.assets, asset)
	return nil
}

func (m *memStore) SaveJob(_ context.Context, job AnalysisJob) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.jobs {
		if m.jobs[i].ID == job.ID {
			m.jobs[i] = job
			return nil
		}
	}
	m.jobs = append(m.jobs, job)
	return nil
}

func (m *memStore) LoadJobs(context.Context) ([]AnalysisJob, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]AnalysisJob, len(m.jobs))
	copy(out, m.jobs)
	return out, nil
}

func (m *memStore) Path() string { return "" }

// stubCUService is a no-op cu.Service used to detect unwanted calls.
type stubCUService struct {
	submitCalled bool
}

func (s *stubCUService) Status(context.Context) (cu.ServiceStatus, error) {
	return cu.ServiceStatus{Configured: true}, nil
}

func (s *stubCUService) Submit(context.Context, cu.VideoAsset) (string, error) {
	s.submitCalled = true
	return "job-should-not-be-called", nil
}

func (s *stubCUService) GetResult(context.Context, string) (cu.AnalysisResult, error) {
	return cu.AnalysisResult{}, nil
}

func (s *stubCUService) PollResult(context.Context, string) (cu.AnalysisResult, error) {
	return cu.AnalysisResult{}, nil
}

func TestSubmitAssetRejectsWhenAnalysisAlreadyInProgress(t *testing.T) {
	store := &memStore{
		jobs: []AnalysisJob{
			{ID: "analysis-1", AssetID: "asset-1", AssetName: "clip.mp4", Status: AnalysisPolling},
		},
	}
	cuClient := &stubCUService{}
	odClient := &onedrive.Client{TokenProvider: nil}
	mediaClient := mediaservice.NewClient(mediaservice.Config{}, nil)

	engine := NewAnalysisEngine(store, cuClient, odClient, mediaClient)

	asset := ProjectAsset{ID: "asset-1", Name: "clip.mp4", CloudAssetID: "cloud-1"}
	job, err := engine.SubmitAsset(context.Background(), asset, nil, nil)
	if err == nil {
		t.Fatalf("expected error for asset with in-flight analysis, got nil (job=%+v)", job)
	}
	if !strings.Contains(err.Error(), "already in progress") {
		t.Fatalf("expected 'already in progress' error, got: %v", err)
	}
	if job != nil {
		t.Fatalf("expected nil job when rejecting duplicate submission, got %+v", job)
	}
	if cuClient.submitCalled {
		t.Fatalf("expected CU Submit to not be called when analysis already in progress")
	}
}

func TestSubmitAssetAllowsResubmitAfterFailure(t *testing.T) {
	store := &memStore{
		jobs: []AnalysisJob{
			{ID: "analysis-1", AssetID: "asset-1", AssetName: "clip.mp4", Status: AnalysisFailed},
		},
	}
	cuClient := &stubCUService{}
	// No TokenProvider configured, so SubmitAsset will fail after the
	// duplicate-submission check but before actually calling CU — this test
	// only verifies the guard doesn't reject a previously-failed job.
	odClient := &onedrive.Client{}
	mediaClient := mediaservice.NewClient(mediaservice.Config{}, nil)

	engine := NewAnalysisEngine(store, cuClient, odClient, mediaClient)

	asset := ProjectAsset{ID: "asset-1", Name: "clip.mp4", CloudAssetID: "cloud-1"}
	_, err := engine.SubmitAsset(context.Background(), asset, nil, nil)
	if err == nil {
		t.Fatalf("expected an error due to missing token provider")
	}
	if strings.Contains(err.Error(), "already in progress") {
		t.Fatalf("resubmission after failure should not be blocked by the in-progress guard, got: %v", err)
	}
}
