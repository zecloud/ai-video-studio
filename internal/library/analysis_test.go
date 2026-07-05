package library

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/zecloud/ai-video-studio/internal/mediaservice"
	"github.com/zecloud/ai-video-studio/internal/onedrive"
)

// stubToken is a fake OneDrive token provider.
type stubToken struct{}

func (s stubToken) AccessToken(_ context.Context, _ []string) (string, error) {
	return "test-token", nil
}

// memStore is a minimal in-memory Store implementation.
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

// stubBackend is an AnalyzeBackend test double.
type stubBackend struct {
	lastRequest *mediaservice.AnalyzeRequest
	result      *mediaservice.AnalyzeResult
	shouldError bool
}

func (s *stubBackend) Analyze(_ context.Context, req mediaservice.AnalyzeRequest) (*mediaservice.AnalyzeResult, error) {
	s.lastRequest = &req
	if s.shouldError {
		return nil, fakeErr("remote failure")
	}
	if s.result != nil {
		return s.result, nil
	}
	return &mediaservice.AnalyzeResult{Status: "succeeded"}, nil
}

type fakeErr string

func (f fakeErr) Error() string { return string(f) }

func TestSubmitAssetRejectsWhenAnalysisAlreadyInProgress(t *testing.T) {
	store := &memStore{
		jobs: []AnalysisJob{
			{ID: "analysis-1", AssetID: "asset-1", AssetName: "clip.mp4", Status: AnalysisPolling},
		},
	}
	back := &stubBackend{}
	od := &onedrive.Client{TokenProvider: stubToken{}}

	engine := NewAnalysisEngine(store, back, od)

	asset := ProjectAsset{ID: "asset-1", Name: "clip.mp4", CloudAssetID: "cloud-1"}
	job, err := engine.SubmitAsset(context.Background(), asset)
	if err == nil {
		t.Fatalf("expected error, got nil (job=%+v)", job)
	}
	if !strings.Contains(err.Error(), "already in progress") {
		t.Fatalf("expected 'already in progress', got: %v", err)
	}
	if back.lastRequest != nil {
		t.Fatal("expected Analyze not called")
	}
}

func TestSubmitAssetAllowsResubmitAfterFailure(t *testing.T) {
	store := &memStore{
		jobs: []AnalysisJob{
			{ID: "analysis-1", AssetID: "asset-1", AssetName: "clip.mp4", Status: AnalysisFailed},
		},
	}
	back := &stubBackend{result: &mediaservice.AnalyzeResult{Status: "succeeded"}}
	od := &onedrive.Client{TokenProvider: stubToken{}}

	engine := NewAnalysisEngine(store, back, od)

	asset := ProjectAsset{ID: "asset-1", Name: "clip.mp4", CloudAssetID: "cloud-1"}
	job, err := engine.SubmitAsset(context.Background(), asset)
	if err != nil {
		t.Fatalf("expected success, got: %v", err)
	}
	if job == nil {
		t.Fatal("expected non-nil job")
	}
	if job.Status != AnalysisSucceeded {
		t.Fatalf("expected %s, got %s", AnalysisSucceeded, job.Status)
	}
	if back.lastRequest == nil {
		t.Fatal("expected Analyze to be called")
	}
	if back.lastRequest.AssetID != "asset-1" {
		t.Fatalf("expected asset-1, got %s", back.lastRequest.AssetID)
	}
}

func TestSubmitAssetNoCloudAssetID(t *testing.T) {
	store := &memStore{}
	back := &stubBackend{}
	od := &onedrive.Client{TokenProvider: stubToken{}}

	engine := NewAnalysisEngine(store, back, od)

	_, err := engine.SubmitAsset(context.Background(), ProjectAsset{ID: "asset-1", Name: "clip.mp4"})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestSubmitAssetMediaServiceError(t *testing.T) {
	store := &memStore{}
	back := &stubBackend{shouldError: true}
	od := &onedrive.Client{TokenProvider: stubToken{}}

	engine := NewAnalysisEngine(store, back, od)

	asset := ProjectAsset{ID: "asset-1", Name: "clip.mp4", CloudAssetID: "cloud-1"}
	job, err := engine.SubmitAsset(context.Background(), asset)
	if err == nil {
		t.Fatal("expected error")
	}
	if job == nil {
		t.Fatal("expected non-nil job on failure")
	}
	if job.Status != AnalysisFailed {
		t.Fatalf("expected %s, got %s", AnalysisFailed, job.Status)
	}
}
