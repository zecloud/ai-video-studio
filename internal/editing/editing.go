package editing

import "context"

type EditProject struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	AssetIDs     []string `json:"assetIds"`
	Timeline     Timeline `json:"timeline"`
	RenderPreset string   `json:"renderPreset,omitempty"`
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
	ID            string      `json:"id"`
	SourceAssetID string      `json:"sourceAssetId"`
	InMS          int64       `json:"inMs"`
	OutMS         int64       `json:"outMs"`
	Transition    *Transition `json:"transition,omitempty"`
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
