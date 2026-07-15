package backend

import (
	"context"
	"fmt"
	"strings"
)

const maxEditingRenderClips = 64

// EditingCapabilities describes the editing operations and media evidence the
// desktop can safely present. It intentionally exposes readiness, never secrets.
type EditingCapabilities struct {
	ProjectPersistence     bool     `json:"projectPersistence"`
	EligibleAssetCount     int      `json:"eligibleAssetCount"`
	RenderServiceReady     bool     `json:"renderServiceReady"`
	OneDriveReady          bool     `json:"oneDriveReady"`
	RenderReady            bool     `json:"renderReady"`
	RenderRecoveryMessage  string   `json:"renderRecoveryMessage,omitempty"`
	SupportedRenderPresets []string `json:"supportedRenderPresets"`
	MaximumRenderClips     int      `json:"maximumRenderClips"`
	OrderedVideoClips      bool     `json:"orderedVideoClips"`
	CutTransitions         bool     `json:"cutTransitions"`
	RenderProgress         bool     `json:"renderProgress"`
	RenderCancellation     bool     `json:"renderCancellation"`
	ManualClipAddition     bool     `json:"manualClipAddition"`
	ClipRemoval            bool     `json:"clipRemoval"`
	ClipReordering         bool     `json:"clipReordering"`
	Trimming               bool     `json:"trimming"`
	MediaMetadata          bool     `json:"mediaMetadata"`
	Thumbnails             bool     `json:"thumbnails"`
	Preview                bool     `json:"preview"`
	Titles                 bool     `json:"titles"`
	Audio                  bool     `json:"audio"`
	MultiTrack             bool     `json:"multiTrack"`
}

// Capabilities returns the safe, currently implemented editing surface.
func (s *EditingService) Capabilities(ctx context.Context) (EditingCapabilities, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	capabilities := EditingCapabilities{
		ProjectPersistence:     s != nil && s.projectStore != nil,
		SupportedRenderPresets: []string{"mpeg4-1080p", "mpeg4-720p"},
		MaximumRenderClips:     maxEditingRenderClips,
		OrderedVideoClips:      true,
		CutTransitions:         true,
		RenderProgress:         true,
		RenderCancellation:     true,
		ClipRemoval:            true,
		ClipReordering:         true,
	}
	if s == nil {
		capabilities.RenderRecoveryMessage = "Editing service is unavailable. Restart the app and try again."
		return capabilities, nil
	}

	if s.store == nil {
		capabilities.RenderRecoveryMessage = "The project library is unavailable. Open or create a project library before rendering."
		return capabilities, nil
	}
	assets, err := s.store.LoadAssets(ctx)
	if err != nil {
		return capabilities, fmt.Errorf("editing: load assets for capabilities: %w", err)
	}
	for _, asset := range assets {
		if strings.TrimSpace(asset.AnalysisJobID) != "" && asset.AnalysisStatus == "succeeded" {
			capabilities.EligibleAssetCount++
		}
	}

	s.mu.Lock()
	settingsStore := s.renderSettings
	odClient := s.odClient
	s.mu.Unlock()
	capabilities.OneDriveReady = odClient != nil && odClient.TokenProvider != nil
	if settingsStore != nil {
		loaded, loadErr := settingsStore.Load(ctx)
		if loadErr != nil {
			return capabilities, fmt.Errorf("editing: load render settings: %w", loadErr)
		}
		capabilities.RenderServiceReady = strings.TrimSpace(loaded.VideoIndexerServiceEndpoint) != "" && strings.TrimSpace(loaded.VideoIndexerServiceAPIKey) != ""
	}

	switch {
	case !capabilities.ProjectPersistence:
		capabilities.RenderRecoveryMessage = "Project storage is unavailable. Restart the app and try again."
	case !capabilities.RenderServiceReady:
		capabilities.RenderRecoveryMessage = "Configure the Video Indexer endpoint and API key in Settings to render."
	case !capabilities.OneDriveReady:
		capabilities.RenderRecoveryMessage = "Sign in to OneDrive in Settings to render and publish output."
	default:
		capabilities.RenderReady = true
	}
	return capabilities, nil
}
