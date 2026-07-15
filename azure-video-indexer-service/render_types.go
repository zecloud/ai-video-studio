package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"time"
)

const renderSchemaVersion = 1

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

func (s RenderJobStatus) Valid() bool {
	switch s {
	case RenderJobStatusStaging, RenderJobStatusQueued, RenderJobStatusRendering, RenderJobStatusUploading, RenderJobStatusSucceeded, RenderJobStatusFailed, RenderJobStatusCanceled:
		return true
	default:
		return false
	}
}

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

type StagedRenderClip struct {
	ID         string `json:"id"`
	Container  string `json:"container"`
	BlobName   string `json:"blobName"`
	SourceName string `json:"sourceName"`
	InMS       int64  `json:"inMs"`
	OutMS      int64  `json:"outMs"`
	Muted      bool   `json:"muted,omitempty"`
}

type RenderOutput struct {
	Container string `json:"container"`
	BlobName  string `json:"blobName"`
	Size      int64  `json:"size,omitempty"`
	MediaType string `json:"mediaType,omitempty"`
}

type RenderJobDocument struct {
	SchemaVersion           int                       `json:"schemaVersion"`
	ID                      string                    `json:"id"`
	ProjectID               string                    `json:"projectId"`
	Status                  RenderJobStatus           `json:"status"`
	Preset                  string                    `json:"preset"`
	OutputName              string                    `json:"outputName"`
	CorrelationID           string                    `json:"correlationId,omitempty"`
	RequestFingerprint      string                    `json:"requestFingerprint,omitempty"`
	Clips                   []StagedRenderClip        `json:"clips"`
	Transitions             []RenderTransitionRequest `json:"transitions,omitempty"`
	OrchestrationID         string                    `json:"orchestrationId"`
	OrchestrationName       string                    `json:"orchestrationName"`
	OrchestrationVersion    string                    `json:"orchestrationVersion"`
	Output                  *RenderOutput             `json:"output,omitempty"`
	CancellationRequestedAt *time.Time                `json:"cancellationRequestedAt,omitempty"`
	ExecutionActive         bool                      `json:"executionActive,omitempty"`
	InputsCleanupPending    bool                      `json:"inputsCleanupPending,omitempty"`
	CleanupError            *APIErrorResponse         `json:"cleanupError,omitempty"`
	CreatedAt               time.Time                 `json:"createdAt"`
	UpdatedAt               time.Time                 `json:"updatedAt"`
	StartedAt               *time.Time                `json:"startedAt,omitempty"`
	CompletedAt             *time.Time                `json:"completedAt,omitempty"`
	Error                   *APIErrorResponse         `json:"error,omitempty"`
}

type StoredRenderJob struct {
	RenderJobDocument
	ETag string `json:"-"`
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
type RenderJobListResponse struct {
	Jobs []RenderJob `json:"jobs"`
}

func (r CreateRenderJobRequest) normalize() CreateRenderJobRequest {
	r.ProjectID = strings.TrimSpace(r.ProjectID)
	r.OneDriveAccessToken = strings.TrimSpace(r.OneDriveAccessToken)
	r.Preset = strings.ToLower(strings.TrimSpace(r.Preset))
	r.OutputName = sanitizeBlobSegment(r.OutputName)
	r.CorrelationID = strings.TrimSpace(r.CorrelationID)
	for i := range r.Clips {
		r.Clips[i].ID = strings.TrimSpace(r.Clips[i].ID)
		r.Clips[i].OneDriveItemID = strings.TrimSpace(r.Clips[i].OneDriveItemID)
		r.Clips[i].SourceName = strings.TrimSpace(r.Clips[i].SourceName)
	}
	for i := range r.Transitions {
		r.Transitions[i].Kind = strings.ToLower(strings.TrimSpace(r.Transitions[i].Kind))
	}
	return r
}

func (r CreateRenderJobRequest) Validate() error {
	r = r.normalize()
	if r.ProjectID == "" || len(r.ProjectID) > 128 {
		return fmt.Errorf("projectId is required and must be at most 128 characters")
	}
	if r.OneDriveAccessToken == "" {
		return fmt.Errorf("oneDriveAccessToken is required")
	}
	if len(r.Clips) == 0 {
		return fmt.Errorf("at least one clip is required")
	}
	if len(r.Clips) > 64 {
		return fmt.Errorf("at most 64 clips are supported")
	}
	if r.Preset != "mpeg4-1080p" && r.Preset != "mpeg4-720p" {
		return fmt.Errorf("preset is unsupported")
	}
	if r.OutputName == "" || len(r.OutputName) > 255 || !strings.HasSuffix(strings.ToLower(r.OutputName), ".mp4") {
		return fmt.Errorf("outputName must be a safe .mp4 filename of at most 255 characters")
	}
	if len(r.Transitions) > 0 && len(r.Transitions) != len(r.Clips)-1 {
		return fmt.Errorf("transitions must contain one entry between each pair of clips")
	}
	clipIDs := make(map[string]struct{}, len(r.Clips))
	for _, clip := range r.Clips {
		if clip.OneDriveItemID == "" {
			return fmt.Errorf("each clip requires id and oneDriveItemId")
		}
		if err := validateID(clip.ID, "clip id"); err != nil {
			return err
		}
		if len(clip.SourceName) > 255 {
			return fmt.Errorf("clip %s sourceName must be at most 255 characters", clip.ID)
		}
		if _, exists := clipIDs[clip.ID]; exists {
			return fmt.Errorf("clip id %q is duplicated", clip.ID)
		}
		clipIDs[clip.ID] = struct{}{}
		if clip.InMS < 0 || clip.OutMS <= clip.InMS {
			return fmt.Errorf("clip %s has an invalid trim range", clip.ID)
		}
	}
	for _, transition := range r.Transitions {
		if transition.Kind != "cut" {
			return fmt.Errorf("transition kind %q is unsupported; only cut is supported", transition.Kind)
		}
		if transition.DurationMS != 0 {
			return fmt.Errorf("cut transitions cannot have a duration")
		}
	}
	if r.CorrelationID != "" {
		return validateID(r.CorrelationID, "correlationId")
	}
	return nil
}

func (j RenderJobDocument) Validate() error {
	if j.SchemaVersion != renderSchemaVersion {
		return fmt.Errorf("unsupported render schema version %d", j.SchemaVersion)
	}
	if err := validateID(j.ID, "id"); err != nil {
		return err
	}
	if j.ProjectID == "" || !j.Status.Valid() || j.OutputName == "" {
		return fmt.Errorf("render job document is incomplete")
	}
	if j.Preset != "mpeg4-1080p" && j.Preset != "mpeg4-720p" {
		return fmt.Errorf("render job preset is unsupported")
	}
	if j.OrchestrationID != j.ID || j.OrchestrationName != ffmpegRenderOrchestrationName || j.OrchestrationVersion != ffmpegRenderOrchestrationVersion {
		return fmt.Errorf("render job orchestration metadata is invalid")
	}
	if j.Status == RenderJobStatusQueued || j.Status == RenderJobStatusRendering || j.Status == RenderJobStatusUploading || j.Status == RenderJobStatusSucceeded {
		if len(j.Clips) == 0 || j.Output == nil || j.Output.Container == "" || j.Output.BlobName == "" {
			return fmt.Errorf("render job execution references are incomplete")
		}
	}
	for _, clip := range j.Clips {
		if err := validateID(clip.ID, "clip id"); err != nil {
			return err
		}
		if clip.Container == "" || clip.BlobName == "" || clip.InMS < 0 || clip.OutMS <= clip.InMS {
			return fmt.Errorf("render job clip reference is invalid")
		}
	}
	if j.Status != RenderJobStatusStaging && j.Status != RenderJobStatusFailed && j.Status != RenderJobStatusCanceled && len(j.Transitions) > 0 && len(j.Transitions) != len(j.Clips)-1 {
		return fmt.Errorf("render job transitions are incomplete")
	}
	for _, transition := range j.Transitions {
		if transition.Kind != "cut" || transition.DurationMS != 0 {
			return fmt.Errorf("render job supports only zero-duration cuts")
		}
	}
	return nil
}

func (j RenderJobDocument) ToRenderJob() RenderJob {
	return RenderJob{
		ID: j.ID, ProjectID: j.ProjectID, Status: j.Status, Preset: j.Preset, OutputName: j.OutputName,
		Output: j.Output, CreatedAt: j.CreatedAt, UpdatedAt: j.UpdatedAt, StartedAt: j.StartedAt,
		CompletedAt: j.CompletedAt, Error: j.Error, CancellationRequestedAt: j.CancellationRequestedAt,
		InputsCleanupPending: j.InputsCleanupPending, CleanupError: j.CleanupError,
	}
}

func renderRequestFingerprint(req CreateRenderJobRequest) (string, error) {
	req = req.normalize()
	shape := struct {
		ProjectID   string                    `json:"projectId"`
		Clips       []RenderClipRequest       `json:"clips"`
		Transitions []RenderTransitionRequest `json:"transitions,omitempty"`
		Preset      string                    `json:"preset"`
		OutputName  string                    `json:"outputName"`
	}{req.ProjectID, req.Clips, req.Transitions, req.Preset, req.OutputName}
	encoded, err := json.Marshal(shape)
	if err != nil {
		return "", fmt.Errorf("encoding render request shape: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func validRenderOutputIdentity(job RenderJob) bool {
	return job.Status == RenderJobStatusSucceeded && job.Output != nil && job.Output.Container != "" &&
		job.Output.BlobName == renderOutputBlobName(job.ID, job.OutputName)
}

func renderStageBlobName(jobID, clipID, sourceName string) string {
	return path.Join("render-inputs", jobID, sanitizeBlobSegment(clipID)+"-"+sanitizeBlobSegment(sourceName))
}
func renderOutputBlobName(jobID, outputName string) string {
	return path.Join("render-outputs", jobID, sanitizeBlobSegment(outputName))
}
