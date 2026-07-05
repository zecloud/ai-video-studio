package library

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const DefaultLibraryFileName = "library.json"

var ErrLibraryPathUnavailable = errors.New("library path is unavailable")

type Store interface {
	LoadAssets(context.Context) ([]ProjectAsset, error)
	SaveAssets(context.Context, []ProjectAsset) error
	AddAsset(context.Context, ProjectAsset) error
	SaveJob(context.Context, AnalysisJob) error
	LoadJobs(context.Context) ([]AnalysisJob, error)
	Path() string
}

type FileStore struct {
	path    string
	appDir  string
	now     func() time.Time
}

func NewFileStore(appDir string) *FileStore {
	if strings.TrimSpace(appDir) == "" {
		appDir = defaultAppDir()
	}
	return &FileStore{
		path:   filepath.Join(appDir, DefaultLibraryFileName),
		appDir: appDir,
		now:    func() time.Time { return time.Now().UTC() },
	}
}

func DefaultLibraryPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrLibraryPathUnavailable, err)
	}
	if strings.TrimSpace(dir) == "" {
		return "", ErrLibraryPathUnavailable
	}
	return filepath.Join(dir, defaultAppDir(), DefaultLibraryFileName), nil
}

func (s *FileStore) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

func (s *FileStore) LoadAssets(_ context.Context) ([]ProjectAsset, error) {
	if s == nil || s.path == "" {
		return nil, ErrLibraryPathUnavailable
	}
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return []ProjectAsset{}, nil
	}
	if err != nil {
		return nil, err
	}
	var assets []ProjectAsset
	if err := json.Unmarshal(data, &assets); err != nil {
		return nil, fmt.Errorf("read library: %w", err)
	}
	return assets, nil
}

func (s *FileStore) SaveAssets(_ context.Context, assets []ProjectAsset) error {
	if s == nil || s.path == "" {
		return ErrLibraryPathUnavailable
	}
	if assets == nil {
		assets = []ProjectAsset{}
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(assets, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, append(data, '\n'), 0o600)
}

func (s *FileStore) AddAsset(ctx context.Context, asset ProjectAsset) error {
	if s == nil {
		return ErrLibraryPathUnavailable
	}
	assets, err := s.LoadAssets(ctx)
	if err != nil {
		return err
	}
	now := s.now()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if asset.ID == "" {
		asset.ID = fmt.Sprintf("asset-%d", now.UnixNano())
	}
	if asset.CreatedAt.IsZero() {
		asset.CreatedAt = now
	}
	assets = append(assets, asset)
	return s.SaveAssets(ctx, assets)
}

const DefaultAnalysisJobsFileName = "analysis-jobs.json"

func (s *FileStore) jobsPath() string {
	return filepath.Join(filepath.Dir(s.path), DefaultAnalysisJobsFileName)
}

// SaveJob persists an analysis job. If a job with the same ID already exists,
// it is replaced (upsert).
func (s *FileStore) SaveJob(ctx context.Context, job AnalysisJob) error {
	if s == nil || s.path == "" {
		return ErrLibraryPathUnavailable
	}
	jobs, err := s.LoadJobs(ctx)
	if err != nil {
		return err
	}
	found := false
	for i := range jobs {
		if jobs[i].ID == job.ID {
			jobs[i] = job
			found = true
			break
		}
	}
	if !found {
		jobs = append(jobs, job)
	}
	return s.writeJobs(jobs)
}

// LoadJobs returns all analysis jobs from the persistent store.
func (s *FileStore) LoadJobs(_ context.Context) ([]AnalysisJob, error) {
	if s == nil || s.path == "" {
		return nil, ErrLibraryPathUnavailable
	}
	jp := s.jobsPath()
	data, err := os.ReadFile(jp)
	if errors.Is(err, os.ErrNotExist) {
		return []AnalysisJob{}, nil
	}
	if err != nil {
		return nil, err
	}
	var jobs []AnalysisJob
	if err := json.Unmarshal(data, &jobs); err != nil {
		return nil, fmt.Errorf("read analysis jobs: %w", err)
	}
	if jobs == nil {
		jobs = []AnalysisJob{}
	}
	return jobs, nil
}

func (s *FileStore) writeJobs(jobs []AnalysisJob) error {
	if jobs == nil {
		jobs = []AnalysisJob{}
	}
	jp := s.jobsPath()
	if err := os.MkdirAll(filepath.Dir(jp), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(jobs, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(jp, append(data, '\n'), 0o600)
}

func defaultAppDir() string {
	return "AI Video Studio"
}