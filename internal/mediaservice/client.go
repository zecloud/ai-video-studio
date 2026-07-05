// Package mediaservice provides a Go client for the remote Azure Media
// Staging service. The service runs in an Azure Container App and copies
// OneDrive-hosted video assets into short-lived Azure Blob staging so that
// Azure Content Understanding (or other consumers) can read them over HTTPS.
//
// This client performs no local disk I/O; it is a thin HTTP wrapper intended
// for use from internal/library.AnalysisEngine and other orchestration code.
package mediaservice

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// ErrInvalidConfig is returned when the client configuration is missing
// required fields (endpoint or API key).
var ErrInvalidConfig = errors.New("invalid media staging service configuration")

// ErrUnexpectedStatus is returned when the remote service responds with a
// status code that does not indicate success.
var ErrUnexpectedStatus = errors.New("unexpected media staging service response status")

// Config holds the connection details for the remote media staging
// Container App.
type Config struct {
	// Endpoint is the base URL of the Container App, e.g.
	// "https://media-staging.azurecontainerapps.io". No trailing slash is
	// required; it is trimmed automatically.
	Endpoint string
	// APIKey is the shared secret sent as a Bearer token on every request.
	APIKey string
}

func (c Config) normalized() Config {
	c.Endpoint = strings.TrimRight(strings.TrimSpace(c.Endpoint), "/")
	c.APIKey = strings.TrimSpace(c.APIKey)
	return c
}

func (c Config) validate() error {
	if c.Endpoint == "" {
		return fmt.Errorf("%w: endpoint is required", ErrInvalidConfig)
	}
	if _, err := url.ParseRequestURI(c.Endpoint); err != nil {
		return fmt.Errorf("%w: endpoint must be an absolute URL", ErrInvalidConfig)
	}
	if c.APIKey == "" {
		return fmt.Errorf("%w: API key is required", ErrInvalidConfig)
	}
	return nil
}

func (c Config) authorize(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
}

// CopyResult is the outcome of a successful CopyToBlob call.
type CopyResult struct {
	BlobURL string `json:"blobUrl"`
	SASURL  string `json:"sasUrl"`
}

type copyRequest struct {
	OneDriveItemID string `json:"oneDriveItemID"`
	OneDriveToken  string `json:"oneDriveToken"`
	BlobName       string `json:"blobName"`
	BlobContainer  string `json:"blobContainer,omitempty"`
}

type deleteRequest struct {
	BlobName      string `json:"blobName"`
	BlobContainer string `json:"blobContainer,omitempty"`
}

type deleteResponse struct {
	Status string `json:"status"`
}

type healthResponse struct {
	Status string `json:"status"`
}

// HTTPClient is the minimal interface the client depends on, satisfied by
// *http.Client and allowing tests to substitute a mock transport.
type HTTPClient interface {
	Do(*http.Request) (*http.Response, error)
}

// Client is a Go client for the remote Azure Media Staging Container App.
type Client struct {
	cfg  Config
	http HTTPClient
}

// NewClient creates a Client for the media staging service. httpClient may
// be nil, in which case http.DefaultClient is used. NewClient does not
// validate cfg; validation happens per-call so a Client can be constructed
// before configuration is finalized (e.g. from settings loaded at startup).
func NewClient(cfg Config, httpClient HTTPClient) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{cfg: cfg.normalized(), http: httpClient}
}

// CopyToBlob asks the remote service to copy a OneDrive item into Azure Blob
// staging, returning the resulting blob URL and a short-lived SAS URL.
func (c *Client) CopyToBlob(ctx context.Context, oneDriveItemID, oneDriveToken, blobName, blobContainer string) (*CopyResult, error) {
	if c == nil {
		return nil, fmt.Errorf("%w: nil client", ErrInvalidConfig)
	}
	if err := c.cfg.validate(); err != nil {
		return nil, err
	}
	oneDriveItemID = strings.TrimSpace(oneDriveItemID)
	if oneDriveItemID == "" {
		return nil, fmt.Errorf("%w: oneDriveItemID is required", ErrInvalidConfig)
	}
	oneDriveToken = strings.TrimSpace(oneDriveToken)
	if oneDriveToken == "" {
		return nil, fmt.Errorf("%w: oneDriveToken is required", ErrInvalidConfig)
	}
	blobName = strings.TrimSpace(blobName)
	if blobName == "" {
		return nil, fmt.Errorf("%w: blobName is required", ErrInvalidConfig)
	}

	reqBody := copyRequest{
		OneDriveItemID: oneDriveItemID,
		OneDriveToken:  oneDriveToken,
		BlobName:       blobName,
		BlobContainer:  strings.TrimSpace(blobContainer),
	}

	var result CopyResult
	if err := c.doJSON(ctx, http.MethodPost, "/api/v1/copy", reqBody, &result); err != nil {
		return nil, fmt.Errorf("mediaservice: copy to blob: %w", err)
	}
	return &result, nil
}

// DeleteBlob asks the remote service to delete a previously staged blob.
// DeleteBlob asks the remote service to delete a staged blob via the RESTful
// DELETE /api/v1/blobs/{name} endpoint. The container name is passed as a
// query parameter; when empty the server uses its default container.
// DeleteBlob asks the remote service to delete a staged blob via the RESTful
// DELETE /api/v1/blobs/{name} endpoint. The container name is passed as a
// query parameter; when empty the server uses its default container.
func (c *Client) DeleteBlob(ctx context.Context, blobName, blobContainer string) error {
	if c == nil {
		return fmt.Errorf("%w: nil client", ErrInvalidConfig)
	}
	if err := c.cfg.validate(); err != nil {
		return err
	}
	blobName = strings.TrimSpace(blobName)
	if blobName == "" {
		return fmt.Errorf("%w: blobName is required", ErrInvalidConfig)
	}

	container := strings.TrimSpace(blobContainer)
	if container == "" {
		container = "media-staging"
	}
	// Use DELETE /api/v1/blobs/{name}?container=... with no JSON body.
	path := "/api/v1/blobs/" + blobName
	if container != "" {
		path += "?container=" + url.QueryEscape(container)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.cfg.Endpoint+path, nil)
	if err != nil {
		return fmt.Errorf("mediaservice: delete blob: %w", err)
	}
	c.cfg.authorize(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("mediaservice: delete blob: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("%w: %d %s: %s", ErrUnexpectedStatus, resp.StatusCode, resp.Status, strings.TrimSpace(string(body)))
	}

	var result deleteResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("mediaservice: delete blob: decode response: %w", err)
	}
	if result.Status != "deleted" {
		return fmt.Errorf("mediaservice: delete blob: unexpected status %q", result.Status)
	}
	return nil
}
func (c *Client) Health(ctx context.Context) error {
	if c == nil {
		return fmt.Errorf("%w: nil client", ErrInvalidConfig)
	}
	cfg := c.cfg
	if cfg.Endpoint == "" {
		return fmt.Errorf("%w: endpoint is required", ErrInvalidConfig)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.Endpoint+"/health", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: health returned %s", ErrUnexpectedStatus, resp.Status)
	}

	var health healthResponse
	if err := json.NewDecoder(resp.Body).Decode(&health); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

// doJSON performs a JSON POST/GET request and decodes the JSON response body
// into out. It centralizes request construction, auth, and error handling.
func (c *Client) doJSON(ctx context.Context, method, path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.cfg.Endpoint+path, reader)
	if err != nil {
		return err
	}
	c.cfg.authorize(req)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		message, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if len(message) > 0 {
			return fmt.Errorf("%w: %s: %s", ErrUnexpectedStatus, resp.Status, strings.TrimSpace(string(message)))
		}
		return fmt.Errorf("%w: %s", ErrUnexpectedStatus, resp.Status)
	}

	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

// ---- Analyze ----

// AnalyzeRequest is the payload for POST /api/v1/analyze.
type AnalyzeRequest struct {
	OneDriveItemID string `json:"oneDriveItemID"`
	OneDriveToken  string `json:"oneDriveToken"`
	AssetID        string `json:"assetID"`
	AssetName      string `json:"assetName"`
}

// AnalyzeResult is the normalized analysis result returned by the media service.
type AnalyzeResult struct {
	JobID       string                `json:"jobId"`
	Status      string                `json:"status"`
	Scenes      []AnalyzeScene        `json:"scenes"`
	Transcript  []AnalyzeTranscript   `json:"transcript"`
	Highlights  []AnalyzeHighlight    `json:"highlights"`
	Suggestions []AnalyzeSuggestion   `json:"suggestions"`
}

// AnalyzeScene describes a detected scene.
type AnalyzeScene struct {
	ID        string   `json:"id"`
	StartMS   int64    `json:"startMs"`
	EndMS     int64    `json:"endMs"`
	Labels    []string `json:"labels"`
	Summary   string   `json:"summary,omitempty"`
	Highlight bool     `json:"highlight"`
}

// AnalyzeTranscript describes a transcript segment.
type AnalyzeTranscript struct {
	StartMS int64   `json:"startMs"`
	EndMS   int64   `json:"endMs"`
	Text    string  `json:"text"`
	Speaker string  `json:"speaker,omitempty"`
	Score   float64 `json:"score,omitempty"`
}

// AnalyzeHighlight describes a highlight candidate.
type AnalyzeHighlight struct {
	ID      string  `json:"id"`
	StartMS int64   `json:"startMs"`
	EndMS   int64   `json:"endMs"`
	Reason  string  `json:"reason"`
	Score   float64 `json:"score"`
}

// AnalyzeSuggestion describes an edit suggestion.
type AnalyzeSuggestion struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	SceneIDs    []string `json:"sceneIds"`
}

// Analyze submits a video for full analysis (OneDrive → Blob → CU → result).
// The media service handles the entire pipeline server-side.
func (c *Client) Analyze(ctx context.Context, req AnalyzeRequest) (*AnalyzeResult, error) {
	if c == nil {
		return nil, fmt.Errorf("%w: nil client", ErrInvalidConfig)
	}
	if err := c.cfg.validate(); err != nil {
		return nil, err
	}
	req.OneDriveItemID = strings.TrimSpace(req.OneDriveItemID)
	if req.OneDriveItemID == "" {
		return nil, fmt.Errorf("%w: oneDriveItemID is required", ErrInvalidConfig)
	}
	req.OneDriveToken = strings.TrimSpace(req.OneDriveToken)
	if req.OneDriveToken == "" {
		return nil, fmt.Errorf("%w: oneDriveToken is required", ErrInvalidConfig)
	}
	req.AssetID = strings.TrimSpace(req.AssetID)
	if req.AssetID == "" {
		return nil, fmt.Errorf("%w: assetID is required", ErrInvalidConfig)
	}
	req.AssetName = strings.TrimSpace(req.AssetName)
	if req.AssetName == "" {
		return nil, fmt.Errorf("%w: assetName is required", ErrInvalidConfig)
	}

	var result AnalyzeResult
	if err := c.doJSON(ctx, http.MethodPost, "/api/v1/analyze", req, &result); err != nil {
		return nil, fmt.Errorf("mediaservice: analyze: %w", err)
	}
	return &result, nil
}

	// ---- Render ----

	// RenderClip describes a single clip segment in a render request.
	type RenderClip struct {
		ID    string `json:"id"`
		Input string `json:"input"` // OneDrive item ID of the source asset
		InMS  int64  `json:"inMs"`
		OutMS int64  `json:"outMs"`
		Muted bool   `json:"muted,omitempty"`
	}

	// RenderTransition describes a transition between two clips.
	type RenderTransition struct {
		Kind       string `json:"kind"`       // "cut", "crossfade"
		DurationMS int64  `json:"durationMs"` // ignored for "cut"
	}

	// RenderRequest is the payload for POST /api/v1/render.
	type RenderRequest struct {
		ProjectID     string             `json:"projectId"`
		OneDriveToken string             `json:"oneDriveToken"`
		Clips         []RenderClip       `json:"clips"`
		Transitions   []RenderTransition `json:"transitions,omitempty"`
		Preset        string             `json:"preset"`     // e.g. "h264-1080p"
		OutputName    string             `json:"outputName"` // destination filename
	}

	// RenderResult is returned by the media service after a successful render.
	type RenderResult struct {
		Status    string `json:"status"`
		OutputURL string `json:"outputUrl"`
		Log       string `json:"log,omitempty"`
	}

	// Render submits a render job to the Azure Container App. The media service
	// downloads source assets from OneDrive, runs FFmpeg, and uploads the result
	// back to OneDrive. This method blocks until rendering is complete.
	func (c *Client) Render(ctx context.Context, req RenderRequest) (*RenderResult, error) {
		if c == nil {
			return nil, fmt.Errorf("%w: nil client", ErrInvalidConfig)
		}
		if err := c.cfg.validate(); err != nil {
			return nil, err
		}
		req.OneDriveToken = strings.TrimSpace(req.OneDriveToken)
		if req.OneDriveToken == "" {
			return nil, fmt.Errorf("%w: oneDriveToken is required", ErrInvalidConfig)
		}
		if len(req.Clips) == 0 {
			return nil, fmt.Errorf("%w: at least one clip is required", ErrInvalidConfig)
		}
		req.ProjectID = strings.TrimSpace(req.ProjectID)
		if req.ProjectID == "" {
			return nil, fmt.Errorf("%w: projectId is required", ErrInvalidConfig)
		}
		req.Preset = strings.TrimSpace(req.Preset)
		if req.Preset == "" {
			req.Preset = "h264-1080p"
		}
		req.OutputName = strings.TrimSpace(req.OutputName)
		if req.OutputName == "" {
			req.OutputName = "render-output.mp4"
		}

		var result RenderResult
		if err := c.doJSON(ctx, http.MethodPost, "/api/v1/render", req, &result); err != nil {
			return nil, fmt.Errorf("mediaservice: render: %w", err)
		}
		return &result, nil
	}
