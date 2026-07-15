package backend

import (
	"context"
	"strings"
	"testing"

	"github.com/zecloud/ai-video-studio/internal/editing"
	"github.com/zecloud/ai-video-studio/internal/library"
)

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
