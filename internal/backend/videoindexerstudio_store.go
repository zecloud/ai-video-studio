package backend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/zecloud/ai-video-studio/internal/settings"
)

const videoIndexerJobsFileName = "video-indexer-jobs.json"

type fileVideoIndexerJobStore struct {
	path string
}

func newDefaultVideoIndexerJobStore() videoIndexerJobStore {
	path, err := settings.DefaultPath()
	if err != nil {
		return nil
	}
	return &fileVideoIndexerJobStore{path: filepath.Join(filepath.Dir(path), videoIndexerJobsFileName)}
}

func (s *fileVideoIndexerJobStore) Load(_ context.Context) ([]VideoIndexerStudioJob, error) {
	if s == nil || strings.TrimSpace(s.path) == "" {
		return []VideoIndexerStudioJob{}, nil
	}
	for _, path := range s.paths() {
		data, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if jobs, ok, err := decodeVideoIndexerJobs(data); err != nil {
			return nil, err
		} else if ok {
			return jobs, nil
		}
		return nil, fmt.Errorf("read video indexer jobs: unrecognized format")
	}
	return []VideoIndexerStudioJob{}, nil
}

func (s *fileVideoIndexerJobStore) Save(_ context.Context, jobs []VideoIndexerStudioJob) error {
	if s == nil || strings.TrimSpace(s.path) == "" {
		return nil
	}
	if jobs == nil {
		jobs = []VideoIndexerStudioJob{}
	}
	data, err := json.MarshalIndent(videoIndexerJobDocument{Version: 1, Jobs: jobs}, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal video indexer jobs: %w", err)
	}
	return writeFileAtomic(s.path, append(data, '\n'), 0o600)
}

func (s *fileVideoIndexerJobStore) paths() []string {
	if s == nil || strings.TrimSpace(s.path) == "" {
		return nil
	}
	dir := filepath.Dir(s.path)
	return []string{
		s.path,
		filepath.Join(dir, "video-indexer-studio-jobs.json"),
		filepath.Join(dir, "videoindexerstudio-jobs.json"),
	}
}

func decodeVideoIndexerJobs(data []byte) ([]VideoIndexerStudioJob, bool, error) {
	if len(data) == 0 {
		return []VideoIndexerStudioJob{}, true, nil
	}
	var doc videoIndexerJobDocument
	if err := json.Unmarshal(data, &doc); err == nil && (doc.Version != 0 || doc.Jobs != nil || strings.Contains(string(data), `"jobs"`)) {
		if doc.Jobs == nil {
			doc.Jobs = []VideoIndexerStudioJob{}
		}
		return doc.Jobs, true, nil
	}
	var jobs []VideoIndexerStudioJob
	if err := json.Unmarshal(data, &jobs); err == nil {
		if jobs == nil {
			jobs = []VideoIndexerStudioJob{}
		}
		return jobs, true, nil
	}
	return nil, false, nil
}
