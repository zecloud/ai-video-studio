package backend

import (
	"context"
	"fmt"
	"strings"

	"github.com/zecloud/ai-video-studio/internal/editing"
)

// DeleteClip removes one existing Smart Edit clip and persists the canonical project.
func (s *EditingService) DeleteClip(ctx context.Context, projectID, clipID string) (editing.EditProject, error) {
	return s.mutateVideoClip(ctx, projectID, clipID, func(clips []editing.ClipSegment, index int) ([]editing.ClipSegment, error) {
		if len(clips) == 1 {
			return nil, fmt.Errorf("editing: cannot remove the only video clip")
		}
		return append(clips[:index:index], clips[index+1:]...), nil
	})
}

// MoveClipEarlier moves one existing Smart Edit clip by one position and persists the canonical project.
func (s *EditingService) MoveClipEarlier(ctx context.Context, projectID, clipID string) (editing.EditProject, error) {
	return s.mutateVideoClip(ctx, projectID, clipID, func(clips []editing.ClipSegment, index int) ([]editing.ClipSegment, error) {
		if index == 0 {
			return nil, fmt.Errorf("editing: clip %q is already first", clipID)
		}
		clips[index-1], clips[index] = clips[index], clips[index-1]
		return clips, nil
	})
}

// MoveClipLater moves one existing Smart Edit clip by one position and persists the canonical project.
func (s *EditingService) MoveClipLater(ctx context.Context, projectID, clipID string) (editing.EditProject, error) {
	return s.mutateVideoClip(ctx, projectID, clipID, func(clips []editing.ClipSegment, index int) ([]editing.ClipSegment, error) {
		if index == len(clips)-1 {
			return nil, fmt.Errorf("editing: clip %q is already last", clipID)
		}
		clips[index], clips[index+1] = clips[index+1], clips[index]
		return clips, nil
	})
}

type videoClipMutation func([]editing.ClipSegment, int) ([]editing.ClipSegment, error)

func (s *EditingService) mutateVideoClip(ctx context.Context, projectID, clipID string, mutate videoClipMutation) (editing.EditProject, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(projectID) == "" || strings.TrimSpace(clipID) == "" {
		return editing.EditProject{}, fmt.Errorf("editing: project and clip IDs are required")
	}
	if err := s.ensureProjectsLoaded(ctx); err != nil {
		return editing.EditProject{}, err
	}
	if s.projectStore == nil {
		return editing.EditProject{}, fmt.Errorf("editing: persisted project storage is unavailable")
	}
	assets, err := s.loadEditingAssetRefs(ctx)
	if err != nil {
		return editing.EditProject{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	storedProject, found := s.projects[projectID]
	if !found {
		return editing.EditProject{}, fmt.Errorf("editing: project %q not found", projectID)
	}
	project := storedProject
	trackIndex, clipIndex, err := findEditableVideoClip(project, clipID)
	if err != nil {
		return editing.EditProject{}, err
	}
	clips := append([]editing.ClipSegment(nil), project.Timeline.Tracks[trackIndex].Clips...)
	clips, err = mutate(clips, clipIndex)
	if err != nil {
		return editing.EditProject{}, err
	}
	project.Timeline.Tracks = append([]editing.Track(nil), project.Timeline.Tracks...)
	project.Timeline.Tracks[trackIndex].Clips = normalizeOrderedVideoClips(clips)
	if err := validateEditableProject(project, assets); err != nil {
		return editing.EditProject{}, err
	}
	s.projects[project.ID] = project
	if err := s.persistProjectsLocked(ctx); err != nil {
		s.projects[project.ID] = storedProject
		return editing.EditProject{}, err
	}
	return project, nil
}

type editingAssetRef struct {
	cloudAssetID string
}

func (s *EditingService) loadEditingAssetRefs(ctx context.Context) (map[string]editingAssetRef, error) {
	if s == nil || s.store == nil {
		return nil, fmt.Errorf("editing: project library is unavailable")
	}
	assets, err := s.store.LoadAssets(ctx)
	if err != nil {
		return nil, fmt.Errorf("editing: load project assets: %w", err)
	}
	byID := make(map[string]editingAssetRef, len(assets))
	for _, asset := range assets {
		byID[asset.ID] = editingAssetRef{cloudAssetID: asset.CloudAssetID}
	}
	return byID, nil
}

func findEditableVideoClip(project editing.EditProject, clipID string) (int, int, error) {
	if len(project.Timeline.Tracks) != 1 || project.Timeline.Tracks[0].Kind != "video" {
		return 0, 0, fmt.Errorf("editing: project %q must have one ordered video track", project.ID)
	}
	for index, clip := range project.Timeline.Tracks[0].Clips {
		if clip.ID == clipID {
			return 0, index, nil
		}
	}
	return 0, 0, fmt.Errorf("editing: clip %q not found in project %q", clipID, project.ID)
}

func validateEditableProject(project editing.EditProject, assets map[string]editingAssetRef) error {
	if len(project.Timeline.Tracks) != 1 || project.Timeline.Tracks[0].Kind != "video" {
		return fmt.Errorf("editing: project %q must have one ordered video track", project.ID)
	}
	track := project.Timeline.Tracks[0]
	if len(track.Clips) == 0 {
		return fmt.Errorf("editing: project %q must retain at least one video clip", project.ID)
	}
	if len(track.Clips) > maxEditingRenderClips {
		return fmt.Errorf("editing: project %q exceeds the %d clip limit", project.ID, maxEditingRenderClips)
	}
	seen := make(map[string]struct{}, len(track.Clips))
	var expectedStart int64
	for _, clip := range track.Clips {
		if strings.TrimSpace(clip.ID) == "" {
			return fmt.Errorf("editing: clip ID is required")
		}
		if _, duplicate := seen[clip.ID]; duplicate {
			return fmt.Errorf("editing: clip %q is duplicated", clip.ID)
		}
		seen[clip.ID] = struct{}{}
		asset, exists := assets[clip.SourceAssetID]
		if !exists || strings.TrimSpace(asset.cloudAssetID) == "" {
			return fmt.Errorf("editing: source asset %q has no OneDrive item", clip.SourceAssetID)
		}
		if clip.InMS < 0 || clip.OutMS <= clip.InMS {
			return fmt.Errorf("editing: clip %q has an invalid trim range", clip.ID)
		}
		if clip.Transition != nil && (strings.ToLower(strings.TrimSpace(clip.Transition.Kind)) != "cut" || clip.Transition.DurationMS != 0) {
			return fmt.Errorf("editing: clip %q has an unsupported transition", clip.ID)
		}
		if clip.TimelineStartMS != expectedStart || clip.DurationMS != clip.OutMS-clip.InMS {
			return fmt.Errorf("editing: clip %q is not contiguous", clip.ID)
		}
		expectedStart += clip.DurationMS
	}
	return nil
}

func normalizeOrderedVideoClips(clips []editing.ClipSegment) []editing.ClipSegment {
	nextStart := int64(0)
	for index := range clips {
		clips[index].TimelineStartMS = nextStart
		clips[index].DurationMS = clips[index].OutMS - clips[index].InMS
		if clips[index].Transition == nil {
			clips[index].Transition = &editing.Transition{Kind: "cut"}
		}
		nextStart += clips[index].DurationMS
	}
	return clips
}
