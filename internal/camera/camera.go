package camera

import (
	"context"
	"io"
	"time"
)

type CameraStorage string

const (
	CameraStorageInternal CameraStorage = "internal"
	CameraStorageSD       CameraStorage = "sd"
)

type CameraDevice struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Model        string    `json:"model"`
	IPAddress    string    `json:"ipAddress"`
	BatteryLevel int       `json:"batteryLevel,omitempty"`
	Connected    bool      `json:"connected"`
	LastSeen     time.Time `json:"lastSeen,omitempty"`
}

type CameraMediaItem struct {
	ID          string        `json:"id"`
	Name        string        `json:"name"`
	Path        string        `json:"path"`
	Storage     CameraStorage `json:"storage"`
	SizeBytes   int64         `json:"sizeBytes"`
	ContentType string        `json:"contentType"`
	CapturedAt  time.Time     `json:"capturedAt,omitempty"`
	DurationMS  int64         `json:"durationMs,omitempty"`
	RangeOK     bool          `json:"rangeOk"`
	EndpointURL string        `json:"endpointUrl,omitempty"`
}

type MediaStreamRequest struct {
	DeviceID string        `json:"deviceId"`
	Path     string        `json:"path"`
	Storage  CameraStorage `json:"storage"`
	Offset   int64         `json:"offset"`
	Length   int64         `json:"length"`
}

type EndpointProbeRequest struct {
	DeviceID  string        `json:"deviceId,omitempty"`
	IPAddress string        `json:"ipAddress,omitempty"`
	Port      int           `json:"port,omitempty"`
	Path      string        `json:"path,omitempty"`
	Storage   CameraStorage `json:"storage,omitempty"`
}

type EndpointProbeResult struct {
	BaseURL       string    `json:"baseUrl"`
	EndpointPath  string    `json:"endpointPath"`
	Reachable     bool      `json:"reachable"`
	V2EndpointOK  bool      `json:"v2EndpointOk"`
	HEADOK        bool      `json:"headOk"`
	RangeOK       bool      `json:"rangeOk"`
	ContentLength int64     `json:"contentLength,omitempty"`
	ContentType   string    `json:"contentType,omitempty"`
	StatusCode    int       `json:"statusCode,omitempty"`
	Message       string    `json:"message"`
	CheckedAt     time.Time `json:"checkedAt"`
}

type EndpointProbePlan struct {
	IPAddress string                `json:"ipAddress"`
	Path      string                `json:"path"`
	Storage   CameraStorage         `json:"storage"`
	Ports     []int                 `json:"ports"`
	Results   []EndpointProbeResult `json:"results"`
	Message   string                `json:"message"`
}

type ValidationStep struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Status      string `json:"status"`
	Description string `json:"description"`
}

type ConnectionStatus struct {
	Available bool   `json:"available"`
	Message   string `json:"message"`
}

type Service interface {
	GetConnectionStatus(context.Context) (ConnectionStatus, error)
	Discover(context.Context) ([]CameraDevice, error)
	ProbeEndpoint(context.Context, EndpointProbeRequest) (EndpointProbeResult, error)
	ProbeEndpointCandidates(context.Context, EndpointProbeRequest) (EndpointProbePlan, error)
	ListMedia(context.Context, string) ([]CameraMediaItem, error)
	ValidationChecklist(context.Context) ([]ValidationStep, error)
}

type MediaConnector interface {
	ListMedia(context.Context, CameraDevice) ([]CameraMediaItem, error)
	OpenMediaStream(context.Context, MediaStreamRequest) (io.ReadCloser, error)
}
