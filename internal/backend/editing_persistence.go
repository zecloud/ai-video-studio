package backend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/zecloud/ai-video-studio/internal/editing"
	"github.com/zecloud/ai-video-studio/internal/settings"
)

type editingProjectStore interface {
	Load(context.Context) ([]editing.EditProject, error)
	Save(context.Context, []editing.EditProject) error
}

type fileEditingProjectStore struct {
	path string
}

func newDefaultEditingProjectStore() editingProjectStore {
	path, err := settings.DefaultPath()
	if err != nil {
		return nil
	}
	return &fileEditingProjectStore{path: filepath.Join(filepath.Dir(path), "edit-projects.json")}
}

func (s *fileEditingProjectStore) Load(_ context.Context) ([]editing.EditProject, error) {
	if s == nil || strings.TrimSpace(s.path) == "" {
		return []editing.EditProject{}, nil
	}
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return []editing.EditProject{}, nil
	}
	if err != nil {
		return nil, err
	}
	var projects []editing.EditProject
	if err := json.Unmarshal(data, &projects); err != nil {
		return nil, fmt.Errorf("read edit projects: %w", err)
	}
	if projects == nil {
		projects = []editing.EditProject{}
	}
	sort.Slice(projects, func(i, j int) bool {
		if projects[i].Name == projects[j].Name {
			return projects[i].ID < projects[j].ID
		}
		return projects[i].Name < projects[j].Name
	})
	return projects, nil
}

func (s *fileEditingProjectStore) Save(_ context.Context, projects []editing.EditProject) error {
	if s == nil || strings.TrimSpace(s.path) == "" {
		return nil
	}
	if projects == nil {
		projects = []editing.EditProject{}
	}
	data, err := json.MarshalIndent(projects, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(s.path, append(data, '\n'), 0o600)
}
