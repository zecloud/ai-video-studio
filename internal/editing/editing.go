package editing

import (
	"context"
	"strings"

	"github.com/zecloud/ai-video-studio/internal/videoindexerstudio"
)

type EditProject struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	AssetIDs      []string `json:"assetIds"`
	Timeline      Timeline `json:"timeline"`
	RenderPreset  string   `json:"renderPreset,omitempty"`
	OriginJobID   string   `json:"originJobId,omitempty"`
	SuggestionID  string   `json:"suggestionId,omitempty"`
	PromptVersion string   `json:"promptVersion,omitempty"`
}

type Timeline struct {
	Tracks []Track `json:"tracks"`
}

type Track struct {
	ID       string        `json:"id"`
	Kind     string        `json:"kind"`
	Clips    []ClipSegment `json:"clips"`
	Overlays []TextOverlay `json:"overlays,omitempty"`
}

type ClipSegment struct {
	ID              string      `json:"id"`
	SourceAssetID   string      `json:"sourceAssetId"`
	InMS            int64       `json:"inMs"`
	OutMS           int64       `json:"outMs"`
	TimelineStartMS int64       `json:"timelineStartMs,omitempty"`
	DurationMS      int64       `json:"durationMs,omitempty"`
	Transition      *Transition `json:"transition,omitempty"`
}

type Transition struct {
	Kind       string `json:"kind"`
	DurationMS int64  `json:"durationMs"`
}

type TextOverlay struct {
	ID      string `json:"id"`
	Text    string `json:"text"`
	StartMS int64  `json:"startMs"`
	EndMS   int64  `json:"endMs"`
}

type AudioMix struct {
	MusicGainDB  float64 `json:"musicGainDb"`
	CameraGainDB float64 `json:"cameraGainDb"`
}

type RenderPreset struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Container  string `json:"container"`
	VideoCodec string `json:"videoCodec"`
	AudioCodec string `json:"audioCodec"`
	MaxBitrate string `json:"maxBitrate"`
}

// RenderJob tracks a single render job dispatched to the Azure Container App.
type RenderJob struct {
	ID          string  `json:"id"`
	ProjectID   string  `json:"projectId"`
	Status      string  `json:"status"`
	OutputURL   string  `json:"outputUrl,omitempty"`
	Percent     float64 `json:"percent"`
	CurrentMS   int64   `json:"currentMs"`
	TotalMS     int64   `json:"totalMs"`
	Message     string  `json:"message,omitempty"`
	ErrorDetail string  `json:"errorDetail,omitempty"`
}

// RenderResult is what the media service returns after a successful render.
type RenderResult struct {
	Status    string `json:"status"`
	OutputURL string `json:"outputUrl"`
	Log       string `json:"log,omitempty"`
}

type Service interface {
	ListProjects(context.Context) ([]EditProject, error)
	SaveProject(context.Context, EditProject) (EditProject, error)
}

func ProjectFromTimelineDraft(draft videoindexerstudio.TimelineDraft) (EditProject, error) {
	if err := draft.Validate(); err != nil {
		return EditProject{}, err
	}

	track := draft.PrimaryVideoTrack
	assets := make([]string, 0, len(track.Clips))
	seenAssets := make(map[string]struct{}, len(track.Clips))
	clips := make([]ClipSegment, 0, len(track.Clips))
	for _, clip := range track.Clips {
		assetID := strings.TrimSpace(clip.SourceAssetID)
		if _, exists := seenAssets[assetID]; !exists {
			seenAssets[assetID] = struct{}{}
			assets = append(assets, assetID)
		}
		transition := clip.Transition
		clipTransition := &Transition{
			Kind:       transition.Kind,
			DurationMS: transition.DurationMS,
		}
		clips = append(clips, ClipSegment{
			ID:              clip.ID,
			SourceAssetID:   clip.SourceAssetID,
			InMS:            clip.InMS,
			OutMS:           clip.OutMS,
			TimelineStartMS: clip.TimelineStartMS,
			DurationMS:      clip.DurationMS,
			Transition:      clipTransition,
		})
	}

	return EditProject{
		AssetIDs:      assets,
		Timeline:      Timeline{Tracks: []Track{{ID: track.ID, Kind: track.Kind, Clips: clips}}},
		OriginJobID:   strings.TrimSpace(draft.OriginJobID),
		SuggestionID:  strings.TrimSpace(draft.SuggestionID),
		PromptVersion: strings.TrimSpace(draft.PromptVersion),
	}, nil
}
