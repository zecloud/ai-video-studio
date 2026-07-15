package backend

import (
	"context"
	"testing"

	"github.com/zecloud/ai-video-studio/internal/library"
	"github.com/zecloud/ai-video-studio/internal/onedrive"
	"github.com/zecloud/ai-video-studio/internal/settings"
)

type capabilitySettingsStore struct{ settings.AppSettings }

func (s capabilitySettingsStore) Load(context.Context) (settings.AppSettings, error) {
	return s.AppSettings, nil
}
func (capabilitySettingsStore) Save(context.Context, settings.AppSettings) (settings.AppSettings, error) {
	return settings.AppSettings{}, nil
}
func (capabilitySettingsStore) Path() string { return "memory" }

func TestEditingCapabilitiesReportsConfiguredRenderContract(t *testing.T) {
	service := NewEditingService(&fakeLibraryStore{assets: []library.ProjectAsset{
		{ID: "ready", AnalysisJobID: "analysis-1", AnalysisStatus: "succeeded"},
		{ID: "pending", AnalysisJobID: "analysis-2", AnalysisStatus: "running"},
	}}, nil, &onedrive.Client{TokenProvider: renderTestTokenProvider{}}, &memoryEditingProjectStore{})
	service.configureRenderJobs(capabilitySettingsStore{AppSettings: settings.AppSettings{
		VideoIndexerServiceEndpoint: "https://render.example.test",
		VideoIndexerServiceAPIKey:   "configured-secret",
	}})

	capabilities, err := service.Capabilities(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !capabilities.RenderReady || capabilities.EligibleAssetCount != 1 {
		t.Fatalf("unexpected capabilities: %#v", capabilities)
	}
	if !capabilities.ClipRemoval || !capabilities.ClipReordering {
		t.Fatalf("implemented clip mutations were not exposed: %#v", capabilities)
	}
	if capabilities.ManualClipAddition || capabilities.Trimming || capabilities.Preview || capabilities.Thumbnails {
		t.Fatalf("unsupported features were exposed: %#v", capabilities)
	}
}

func TestEditingCapabilitiesExplainsMissingRenderConfiguration(t *testing.T) {
	service := NewEditingService(&fakeLibraryStore{}, nil, &onedrive.Client{TokenProvider: renderTestTokenProvider{}}, &memoryEditingProjectStore{})
	service.configureRenderJobs(capabilitySettingsStore{})

	capabilities, err := service.Capabilities(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if capabilities.RenderReady || capabilities.RenderServiceReady || capabilities.RenderRecoveryMessage == "" {
		t.Fatalf("unexpected capabilities: %#v", capabilities)
	}
}

func TestEditingCapabilitiesExplainsMissingOneDriveSignIn(t *testing.T) {
	service := NewEditingService(&fakeLibraryStore{}, nil, &onedrive.Client{}, &memoryEditingProjectStore{})
	service.configureRenderJobs(capabilitySettingsStore{AppSettings: settings.AppSettings{
		VideoIndexerServiceEndpoint: "https://render.example.test",
		VideoIndexerServiceAPIKey:   "configured-secret",
	}})

	capabilities, err := service.Capabilities(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if capabilities.RenderReady || !capabilities.RenderServiceReady || capabilities.OneDriveReady || capabilities.RenderRecoveryMessage == "" {
		t.Fatalf("unexpected capabilities: %#v", capabilities)
	}
}
