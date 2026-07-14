package videoindexerstudio

import (
	"context"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type RenderJobStatus string

const (
	RenderJobStatusStaging   RenderJobStatus = "staging"
	RenderJobStatusQueued    RenderJobStatus = "queued"
	RenderJobStatusRendering RenderJobStatus = "rendering"
	RenderJobStatusUploading RenderJobStatus = "uploading"
	RenderJobStatusSucceeded RenderJobStatus = "succeeded"
	RenderJobStatusFailed    RenderJobStatus = "failed"
	RenderJobStatusCanceled  RenderJobStatus = "canceled"
)

func (s RenderJobStatus) Terminal() bool {
	return s == RenderJobStatusSucceeded || s == RenderJobStatusFailed || s == RenderJobStatusCanceled
}

type RenderClipRequest struct {
	ID             string `json:"id"`
	OneDriveItemID string `json:"oneDriveItemId"`
	SourceName     string `json:"sourceName,omitempty"`
	InMS           int64  `json:"inMs"`
	OutMS          int64  `json:"outMs"`
	Muted          bool   `json:"muted,omitempty"`
}

type RenderTransitionRequest struct {
	Kind       string `json:"kind"`
	DurationMS int64  `json:"durationMs,omitempty"`
}

type CreateRenderJobRequest struct {
	ProjectID           string                    `json:"projectId"`
	OneDriveAccessToken string                    `json:"oneDriveAccessToken"`
	Clips               []RenderClipRequest       `json:"clips"`
	Transitions         []RenderTransitionRequest `json:"transitions,omitempty"`
	Preset              string                    `json:"preset"`
	OutputName          string                    `json:"outputName"`
	CorrelationID       string                    `json:"correlationId,omitempty"`
}

type RenderOutput struct {
	Container string `json:"container"`
	BlobName  string `json:"blobName"`
	Size      int64  `json:"size,omitempty"`
	MediaType string `json:"mediaType,omitempty"`
}

type RenderJob struct {
	ID                      string            `json:"id"`
	ProjectID               string            `json:"projectId"`
	Status                  RenderJobStatus   `json:"status"`
	Preset                  string            `json:"preset"`
	OutputName              string            `json:"outputName"`
	Output                  *RenderOutput     `json:"output,omitempty"`
	CreatedAt               time.Time         `json:"createdAt"`
	UpdatedAt               time.Time         `json:"updatedAt"`
	StartedAt               *time.Time        `json:"startedAt,omitempty"`
	CompletedAt             *time.Time        `json:"completedAt,omitempty"`
	Error                   *APIErrorResponse `json:"error,omitempty"`
	CancellationRequestedAt *time.Time        `json:"cancellationRequestedAt,omitempty"`
	InputsCleanupPending    bool              `json:"inputsCleanupPending,omitempty"`
	CleanupError            *APIErrorResponse `json:"cleanupError,omitempty"`
}

type RenderJobResponse struct {
	Job RenderJob `json:"job"`
}

type RenderOutputStream struct {
	Body          io.ReadCloser
	ContentLength int64
	ContentType   string
	FileName      string
}

func (r CreateRenderJobRequest) Validate() error {
	if strings.TrimSpace(r.ProjectID) == "" {
		return fmt.Errorf("%w: projectId is required", ErrInvalidRequest)
	}
	if strings.TrimSpace(r.OneDriveAccessToken) == "" {
		return fmt.Errorf("%w: oneDriveAccessToken is required", ErrInvalidRequest)
	}
	if len(r.Clips) == 0 {
		return fmt.Errorf("%w: at least one clip is required", ErrInvalidRequest)
	}
	if strings.TrimSpace(r.Preset) == "" || strings.TrimSpace(r.OutputName) == "" {
		return fmt.Errorf("%w: preset and outputName are required", ErrInvalidRequest)
	}
	if r.CorrelationID != "" {
		if err := validateID(r.CorrelationID, "correlationId"); err != nil {
			return err
		}
	}
	for _, clip := range r.Clips {
		if err := validateID(clip.ID, "clip id"); err != nil {
			return err
		}
		if strings.TrimSpace(clip.OneDriveItemID) == "" || clip.InMS < 0 || clip.OutMS <= clip.InMS {
			return fmt.Errorf("%w: clip %s is invalid", ErrInvalidRequest, clip.ID)
		}
	}
	return nil
}

func (j RenderJob) Validate() error {
	if err := validateID(j.ID, "id"); err != nil {
		return err
	}
	switch j.Status {
	case RenderJobStatusStaging, RenderJobStatusQueued, RenderJobStatusRendering, RenderJobStatusUploading, RenderJobStatusSucceeded, RenderJobStatusFailed, RenderJobStatusCanceled:
	default:
		return fmt.Errorf("%w: invalid render status %q", ErrInvalidRequest, j.Status)
	}
	if strings.TrimSpace(j.ProjectID) == "" || strings.TrimSpace(j.OutputName) == "" {
		return fmt.Errorf("%w: render job is incomplete", ErrInvalidRequest)
	}
	if j.Status == RenderJobStatusSucceeded && (j.Output == nil || j.Output.Size <= 0) {
		return fmt.Errorf("%w: succeeded render output is incomplete", ErrInvalidRequest)
	}
	return nil
}

func (c *Client) CreateRenderJob(ctx context.Context, req CreateRenderJobRequest) (*RenderJobResponse, error) {
	if c == nil {
		return nil, fmt.Errorf("%w: nil client", ErrInvalidConfig)
	}
	if err := c.cfg.validate(); err != nil {
		return nil, err
	}
	if err := req.Validate(); err != nil {
		return nil, err
	}
	var out RenderJobResponse
	if err := c.doJSON(ctx, http.MethodPost, "/api/v1/render-jobs", req, &out); err != nil {
		return nil, fmt.Errorf("create render job: %w", err)
	}
	if err := out.Job.Validate(); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) GetRenderJob(ctx context.Context, id string) (*RenderJobResponse, error) {
	if c == nil {
		return nil, fmt.Errorf("%w: nil client", ErrInvalidConfig)
	}
	if err := validateID(id, "id"); err != nil {
		return nil, err
	}
	var out RenderJobResponse
	if err := c.doJSON(ctx, http.MethodGet, "/api/v1/render-jobs/"+url.PathEscape(strings.TrimSpace(id)), nil, &out); err != nil {
		return nil, fmt.Errorf("get render job: %w", err)
	}
	if err := out.Job.Validate(); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) CancelRenderJob(ctx context.Context, id string) (*RenderJobResponse, error) {
	if c == nil {
		return nil, fmt.Errorf("%w: nil client", ErrInvalidConfig)
	}
	if err := validateID(id, "id"); err != nil {
		return nil, err
	}
	var out RenderJobResponse
	path := "/api/v1/render-jobs/" + url.PathEscape(strings.TrimSpace(id)) + "/cancel"
	if err := c.doJSON(ctx, http.MethodPost, path, nil, &out); err != nil {
		return nil, fmt.Errorf("cancel render job: %w", err)
	}
	if err := out.Job.Validate(); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) OpenRenderOutput(ctx context.Context, id string) (*RenderOutputStream, error) {
	if c == nil {
		return nil, fmt.Errorf("%w: nil client", ErrInvalidConfig)
	}
	if err := c.cfg.validate(); err != nil {
		return nil, err
	}
	if err := validateID(id, "id"); err != nil {
		return nil, err
	}
	path := "/api/v1/render-jobs/" + url.PathEscape(strings.TrimSpace(id)) + "/output"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.cfg.Endpoint+path, nil)
	if err != nil {
		return nil, err
	}
	c.cfg.authorize(req)
	req.Header.Set("Accept", "video/mp4, application/octet-stream")
	req.Header.Set("User-Agent", "videoindexerstudio-client/1")
	resp, err := c.redirectSafeHTTPClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("open render output: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		return nil, fmt.Errorf("open render output: %w", decodeError(resp))
	}
	stream := &RenderOutputStream{Body: resp.Body, ContentLength: resp.ContentLength, ContentType: resp.Header.Get("Content-Type")}
	if raw := strings.TrimSpace(resp.Header.Get("Content-Length")); raw != "" && stream.ContentLength <= 0 {
		if parsed, parseErr := strconv.ParseInt(raw, 10, 64); parseErr == nil {
			stream.ContentLength = parsed
		}
	}
	if disposition := resp.Header.Get("Content-Disposition"); disposition != "" {
		if _, params, parseErr := mime.ParseMediaType(disposition); parseErr == nil {
			stream.FileName = strings.TrimSpace(params["filename"])
		}
	}
	return stream, nil
}
