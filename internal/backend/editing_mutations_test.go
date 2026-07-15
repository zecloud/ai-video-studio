package backend

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/zecloud/ai-video-studio/internal/editing"
	"github.com/zecloud/ai-video-studio/internal/library"
)

type failingEditingProjectStore struct{ memoryEditingProjectStore }

func (f *failingEditingProjectStore) Save(context.Context, []editing.EditProject) error {
	return errors.New("save failed")
}

func TestEditingServiceDeleteClipPersistsCanonicalProject(t *testing.T) {
	store := &memoryEditingProjectStore{}
	service := NewEditingService(&fakeLibraryStore{assets: []library.ProjectAsset{
		{ID: "asset-a", CloudAssetID: "drive-a"},
		{ID: "asset-b", CloudAssetID: "drive-b"},
		{ID: "asset-c", CloudAssetID: "drive-c"},
	}}, nil, nil, store)
	project := mutationTestProject()
	if _, err := service.SaveProject(context.Background(), project); err != nil {
		t.Fatal(err)
	}

	updated, err := service.DeleteClip(context.Background(), project.ID, "clip-b")
	if err != nil {
		t.Fatalf("DeleteClip() error = %v", err)
	}
	assertMutationProject(t, updated, []string{"clip-a", "clip-c"}, []int64{0, 1000})
	if updated.OriginJobID != project.OriginJobID || updated.SuggestionID != project.SuggestionID || updated.PromptVersion != project.PromptVersion {
		t.Fatalf("project provenance changed: %#v", updated)
	}

	reloaded := NewEditingService(&fakeLibraryStore{}, nil, nil, store)
	projects, err := reloaded.ListProjects(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 1 {
		t.Fatalf("persisted project count = %d, want 1", len(projects))
	}
	assertMutationProject(t, projects[0], []string{"clip-a", "clip-c"}, []int64{0, 1000})
}

func TestEditingServiceMoveClipNormalizesOrderWithoutChangingClipIdentity(t *testing.T) {
	service := mutationTestService(t)
	project := mutationTestProject()
	if _, err := service.SaveProject(context.Background(), project); err != nil {
		t.Fatal(err)
	}

	updated, err := service.MoveClipEarlier(context.Background(), project.ID, "clip-c")
	if err != nil {
		t.Fatalf("MoveClipEarlier() error = %v", err)
	}
	assertMutationProject(t, updated, []string{"clip-a", "clip-c", "clip-b"}, []int64{0, 1000, 2000})
	if updated.Timeline.Tracks[0].Clips[1].SourceAssetID != "asset-c" || updated.Timeline.Tracks[0].Clips[1].InMS != 50 || updated.Timeline.Tracks[0].Clips[1].OutMS != 1050 {
		t.Fatalf("move changed the clip source range: %#v", updated.Timeline.Tracks[0].Clips[1])
	}

	_, err = service.MoveClipEarlier(context.Background(), project.ID, "clip-a")
	if err == nil || !strings.Contains(err.Error(), "already first") {
		t.Fatalf("MoveClipEarlier() boundary error = %v", err)
	}
	_, err = service.MoveClipLater(context.Background(), project.ID, "clip-b")
	if err == nil || !strings.Contains(err.Error(), "already last") {
		t.Fatalf("MoveClipLater() boundary error = %v", err)
	}
}

func TestEditingServiceDeleteClipRejectsOnlyRemainingClip(t *testing.T) {
	service := mutationTestService(t)
	project := mutationTestProject()
	project.Timeline.Tracks[0].Clips = project.Timeline.Tracks[0].Clips[:1]
	if _, err := service.SaveProject(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	_, err := service.DeleteClip(context.Background(), project.ID, "clip-a")
	if err == nil || !strings.Contains(err.Error(), "only video clip") {
		t.Fatalf("DeleteClip() error = %v", err)
	}
}

func TestEditingServiceMutationRejectsUnavailableProjectStorage(t *testing.T) {
	service := NewEditingService(&fakeLibraryStore{}, nil, nil, nil)
	_, err := service.DeleteClip(context.Background(), "project", "clip")
	if err == nil || !strings.Contains(err.Error(), "persisted project storage") {
		t.Fatalf("DeleteClip() error = %v", err)
	}
}

func TestEditingServiceMutationFailureDoesNotChangeStoredProject(t *testing.T) {
	store := &failingEditingProjectStore{}
	service := NewEditingService(&fakeLibraryStore{assets: []library.ProjectAsset{
		{ID: "asset-a", CloudAssetID: "drive-a"},
		{ID: "asset-b", CloudAssetID: "drive-b"},
		{ID: "asset-c", CloudAssetID: "drive-c"},
	}}, nil, nil, store)
	project := mutationTestProject()
	service.projects[project.ID] = project
	service.loaded = true

	if _, err := service.DeleteClip(context.Background(), project.ID, "clip-b"); err == nil || !strings.Contains(err.Error(), "save failed") {
		t.Fatalf("DeleteClip() error = %v", err)
	}
	assertMutationProject(t, service.projects[project.ID], []string{"clip-a", "clip-b", "clip-c"}, []int64{0, 1000, 2000})
}

func TestEditingServiceMutationPreservesOpaqueIdentifiers(t *testing.T) {
	service := mutationTestService(t)
	project := mutationTestProject()
	project.ID = " project "
	project.Timeline.Tracks[0].Clips[1].ID = " clip-b "
	if _, err := service.SaveProject(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	updated, err := service.MoveClipEarlier(context.Background(), project.ID, " clip-b ")
	if err != nil {
		t.Fatal(err)
	}
	assertMutationProject(t, updated, []string{" clip-b ", "clip-a", "clip-c"}, []int64{0, 1000, 2000})
}
func TestEditingServiceSaveProjectFailureRestoresPreviousProject(t *testing.T) {
	store := &failingEditingProjectStore{}
	service := NewEditingService(&fakeLibraryStore{}, nil, nil, store)
	previous := mutationTestProject()
	service.projects[previous.ID] = previous
	service.loaded = true
	updated := previous
	updated.Name = "Updated composition"

	if _, err := service.SaveProject(context.Background(), updated); err == nil || !strings.Contains(err.Error(), "save failed") {
		t.Fatalf("SaveProject() error = %v", err)
	}
	if got := service.projects[previous.ID]; got.Name != previous.Name {
		t.Fatalf("stored project was changed after persistence failure: %#v", got)
	}
}

func TestEditingServiceSaveProjectRejectsMoreThanRenderClipLimit(t *testing.T) {
	service := mutationTestService(t)
	project := mutationTestProject()
	clips := make([]editing.ClipSegment, maxEditingRenderClips+1)
	for index := range clips {
		clips[index] = editing.ClipSegment{ID: fmt.Sprintf("clip-%d", index), SourceAssetID: "asset-a", InMS: 0, OutMS: 1000}
	}
	project.Timeline.Tracks[0].Clips = clips

	if _, err := service.SaveProject(context.Background(), project); err == nil || !strings.Contains(err.Error(), "clip limit") {
		t.Fatalf("SaveProject() error = %v", err)
	}
}
func mutationTestService(t *testing.T) *EditingService {
	t.Helper()
	return NewEditingService(&fakeLibraryStore{assets: []library.ProjectAsset{
		{ID: "asset-a", CloudAssetID: "drive-a"},
		{ID: "asset-b", CloudAssetID: "drive-b"},
		{ID: "asset-c", CloudAssetID: "drive-c"},
	}}, nil, nil, &memoryEditingProjectStore{})
}

func mutationTestProject() editing.EditProject {
	return editing.EditProject{
		ID: "project-mutation", Name: "Composition", AssetIDs: []string{"asset-a", "asset-b", "asset-c"},
		OriginJobID: "job-composition", SuggestionID: "multi-video-narrative", PromptVersion: "multi-video-composition-v2",
		Timeline: editing.Timeline{Tracks: []editing.Track{{ID: "video-1", Kind: "video", Clips: []editing.ClipSegment{
			{ID: "clip-a", SourceAssetID: "asset-a", InMS: 0, OutMS: 1000, TimelineStartMS: 0, DurationMS: 1000, Transition: &editing.Transition{Kind: "cut"}},
			{ID: "clip-b", SourceAssetID: "asset-b", InMS: 100, OutMS: 1100, TimelineStartMS: 1000, DurationMS: 1000, Transition: &editing.Transition{Kind: "cut"}},
			{ID: "clip-c", SourceAssetID: "asset-c", InMS: 50, OutMS: 1050, TimelineStartMS: 2000, DurationMS: 1000, Transition: &editing.Transition{Kind: "cut"}},
		}}}},
	}
}

func assertMutationProject(t *testing.T, project editing.EditProject, ids []string, starts []int64) {
	t.Helper()
	clips := project.Timeline.Tracks[0].Clips
	if len(clips) != len(ids) {
		t.Fatalf("clip count = %d, want %d", len(clips), len(ids))
	}
	for index, clip := range clips {
		if clip.ID != ids[index] || clip.TimelineStartMS != starts[index] || clip.DurationMS != clip.OutMS-clip.InMS || clip.Transition == nil || clip.Transition.Kind != "cut" || clip.Transition.DurationMS != 0 {
			t.Fatalf("unexpected clip %d: %#v", index, clip)
		}
	}
}
