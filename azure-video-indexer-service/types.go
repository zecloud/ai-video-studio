package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"strings"
	"time"
	"unicode"

	"github.com/zecloud/ai-video-studio/internal/videoindexerstudio"
)

const (
	maxRequestBodyBytes = 1 << 20
	schemaVersion       = 1
)

var jobIDPattern = func() func(string) bool {
	return func(raw string) bool {
		if len(raw) == 0 || len(raw) > 128 {
			return false
		}
		for i, r := range raw {
			switch {
			case unicode.IsLetter(r), unicode.IsDigit(r), r == '-', r == '_', r == '.':
				if i == 0 && (r == '-' || r == '.' || r == '_') {
					return false
				}
			default:
				return false
			}
		}
		return true
	}
}()

type JobStatus string

const (
	JobStatusQueued           JobStatus = "queued"
	JobStatusStaging          JobStatus = "staging"
	JobStatusStaged           JobStatus = "staged"
	JobStatusProcessing       JobStatus = "processing"
	JobStatusSubmitting       JobStatus = "submitting"
	JobStatusIndexing         JobStatus = "indexing"
	JobStatusNormalizing      JobStatus = "normalizing"
	JobStatusGenerating       JobStatus = "generating"
	JobStatusBuildingTimeline JobStatus = "building_timeline"
	JobStatusSucceeded        JobStatus = "succeeded"
	JobStatusFailed           JobStatus = "failed"
	JobStatusCanceled         JobStatus = "canceled"
)

var jobStatusRank = map[JobStatus]int{
	JobStatusQueued:           10,
	JobStatusStaging:          20,
	JobStatusStaged:           30,
	JobStatusProcessing:       40,
	JobStatusSubmitting:       50,
	JobStatusIndexing:         60,
	JobStatusNormalizing:      70,
	JobStatusGenerating:       75,
	JobStatusBuildingTimeline: 78,
	JobStatusSucceeded:        80,
	JobStatusFailed:           80,
	JobStatusCanceled:         80,
}

func (s JobStatus) Terminal() bool {
	switch s {
	case JobStatusSucceeded, JobStatusFailed, JobStatusCanceled:
		return true
	default:
		return false
	}
}

func (s JobStatus) Valid() bool {
	_, ok := jobStatusRank[s]
	return ok
}

type CreateIndexJobRequest struct {
	OneDriveItemID      string `json:"oneDriveItemId"`
	OneDriveAccessToken string `json:"oneDriveAccessToken"`
	SourceName          string `json:"sourceName,omitempty"`
	CallbackURL         string `json:"callbackUrl,omitempty"`
	CorrelationID       string `json:"correlationId,omitempty"`
}

type CreateJobRequest = CreateIndexJobRequest

type JobDocument struct {
	SchemaVersion       int                                `json:"schemaVersion"`
	ID                  string                             `json:"id"`
	Status              JobStatus                          `json:"status"`
	OneDriveItemID      string                             `json:"oneDriveItemId"`
	SourceName          string                             `json:"sourceName,omitempty"`
	CorrelationID       string                             `json:"correlationId,omitempty"`
	StagingContainer    string                             `json:"stagingContainer,omitempty"`
	StagedBlobName      string                             `json:"stagedBlobName,omitempty"`
	VideoIndexerVideoID string                             `json:"videoIndexerVideoId,omitempty"`
	VideoIndexerState   string                             `json:"videoIndexerState,omitempty"`
	VideoIndexResult    *VideoIndexResult                  `json:"videoIndexResult,omitempty"`
	EditPlan            *EditPlan                          `json:"editPlan,omitempty"`
	TimelineDrafts      []videoindexerstudio.TimelineDraft `json:"timelineDrafts,omitempty"`
	Checkpoints         []JobCheckpoint                    `json:"checkpoints,omitempty"`
	CreatedAt           time.Time                          `json:"createdAt"`
	UpdatedAt           time.Time                          `json:"updatedAt"`
	StartedAt           *time.Time                         `json:"startedAt,omitempty"`
	CompletedAt         *time.Time                         `json:"completedAt,omitempty"`
	Error               *APIErrorResponse                  `json:"error,omitempty"`
	ClaimedBy           string                             `json:"claimedBy,omitempty"`
}

type StoredJob struct {
	JobDocument
	ETag string `json:"-"`
}

type Job struct {
	ID                  string                             `json:"id"`
	Status              JobStatus                          `json:"status"`
	OneDriveItemID      string                             `json:"oneDriveItemId"`
	SourceName          string                             `json:"sourceName,omitempty"`
	StagedBlobName      string                             `json:"stagedBlobName,omitempty"`
	VideoIndexerVideoID string                             `json:"videoIndexerVideoId,omitempty"`
	VideoIndexerState   string                             `json:"videoIndexerState,omitempty"`
	VideoIndexResult    *VideoIndexResult                  `json:"videoIndexResult,omitempty"`
	EditPlan            *EditPlan                          `json:"editPlan,omitempty"`
	TimelineDrafts      []videoindexerstudio.TimelineDraft `json:"timelineDrafts,omitempty"`
	Checkpoints         []JobCheckpoint                    `json:"checkpoints,omitempty"`
	CreatedAt           time.Time                          `json:"createdAt"`
	UpdatedAt           time.Time                          `json:"updatedAt"`
	StartedAt           *time.Time                         `json:"startedAt,omitempty"`
	CompletedAt         *time.Time                         `json:"completedAt,omitempty"`
	Error               *APIErrorResponse                  `json:"error,omitempty"`
}

type JobCheckpoint struct {
	Stage    string          `json:"stage"`
	At       time.Time       `json:"at"`
	VideoID  string          `json:"videoId,omitempty"`
	State    string          `json:"state,omitempty"`
	Detail   string          `json:"detail,omitempty"`
	Metadata json.RawMessage `json:"metadata,omitempty"`
}

type JobResponse struct {
	Job Job `json:"job"`
}

type JobListResponse struct {
	Jobs []Job `json:"jobs"`
}

type APIErrorResponse struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

func (r CreateIndexJobRequest) normalize() CreateIndexJobRequest {
	r.OneDriveItemID = strings.TrimSpace(r.OneDriveItemID)
	r.OneDriveAccessToken = strings.TrimSpace(r.OneDriveAccessToken)
	r.SourceName = strings.TrimSpace(r.SourceName)
	r.CallbackURL = strings.TrimSpace(r.CallbackURL)
	r.CorrelationID = strings.TrimSpace(r.CorrelationID)
	return r
}

func (r CreateIndexJobRequest) Validate() error {
	r = r.normalize()
	if r.OneDriveItemID == "" {
		return fmt.Errorf("oneDriveItemId is required")
	}
	if len(r.OneDriveItemID) > 256 {
		return fmt.Errorf("oneDriveItemId is too long")
	}
	if r.OneDriveAccessToken == "" {
		return fmt.Errorf("oneDriveAccessToken is required")
	}
	if len(r.SourceName) > 255 {
		return fmt.Errorf("sourceName is too long")
	}
	if r.CallbackURL != "" {
		if err := validateHTTPURL(r.CallbackURL, "callbackUrl"); err != nil {
			return err
		}
	}
	if r.CorrelationID != "" {
		if err := validateID(r.CorrelationID, "correlationId"); err != nil {
			return err
		}
	}
	return nil
}

func (j JobDocument) Validate() error {
	if j.SchemaVersion != schemaVersion {
		return fmt.Errorf("unsupported schema version %d", j.SchemaVersion)
	}
	if err := validateID(j.ID, "id"); err != nil {
		return err
	}
	if j.CorrelationID != "" {
		if err := validateID(j.CorrelationID, "correlationId"); err != nil {
			return err
		}
	}
	if !j.Status.Valid() {
		return fmt.Errorf("invalid job status %q", j.Status)
	}
	if j.OneDriveItemID == "" {
		return fmt.Errorf("oneDriveItemId is required")
	}
	return nil
}

func (j JobDocument) ToJob() Job {
	return Job{
		ID:                  j.ID,
		Status:              j.Status,
		OneDriveItemID:      j.OneDriveItemID,
		SourceName:          j.SourceName,
		StagedBlobName:      j.StagedBlobName,
		VideoIndexerVideoID: j.VideoIndexerVideoID,
		VideoIndexerState:   j.VideoIndexerState,
		VideoIndexResult:    j.VideoIndexResult,
		EditPlan:            j.EditPlan,
		TimelineDrafts:      append([]videoindexerstudio.TimelineDraft(nil), j.TimelineDrafts...),
		Checkpoints:         append([]JobCheckpoint(nil), j.Checkpoints...),
		CreatedAt:           j.CreatedAt,
		UpdatedAt:           j.UpdatedAt,
		StartedAt:           j.StartedAt,
		CompletedAt:         j.CompletedAt,
		Error:               j.Error,
	}
}

func (j JobDocument) clone() JobDocument {
	return j
}

func (j JobDocument) next(status JobStatus, now time.Time) (JobDocument, error) {
	if !status.Valid() {
		return JobDocument{}, fmt.Errorf("invalid job status %q", status)
	}
	if jobStatusRank[status] < jobStatusRank[j.Status] {
		return JobDocument{}, fmt.Errorf("job status cannot move from %s to %s", j.Status, status)
	}
	next := j.clone()
	next.Status = status
	next.UpdatedAt = now
	if next.StartedAt == nil && status != JobStatusQueued {
		startedAt := now
		next.StartedAt = &startedAt
	}
	if status.Terminal() {
		completed := now
		next.CompletedAt = &completed
	}
	return next, nil
}

func (e APIErrorResponse) Validate() error {
	if strings.TrimSpace(e.Code) == "" {
		return fmt.Errorf("code is required")
	}
	if strings.TrimSpace(e.Message) == "" {
		return fmt.Errorf("message is required")
	}
	return nil
}

func validateHTTPURL(raw, field string) error {
	parsed, err := url.ParseRequestURI(strings.TrimSpace(raw))
	if err != nil {
		return fmt.Errorf("%s must be an absolute URL", field)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("%s must use http or https", field)
	}
	if parsed.Host == "" {
		return fmt.Errorf("%s must include a host", field)
	}
	return nil
}

func validateID(raw, field string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fmt.Errorf("%s is required", field)
	}
	if !jobIDPattern(raw) {
		return fmt.Errorf("%s contains invalid characters", field)
	}
	return nil
}

func stageBlobName(jobID, sourceName string) string {
	base := sanitizeBlobSegment(sourceName)
	if base == "" {
		base = "input.bin"
	}
	return path.Join("jobs", jobID, base)
}

func sanitizeBlobSegment(raw string) string {
	raw = strings.TrimSpace(path.Base(raw))
	if raw == "" || raw == "." || raw == ".." {
		return ""
	}
	var b strings.Builder
	b.Grow(len(raw))
	for _, r := range raw {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r), r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	result := strings.Trim(b.String(), "._-")
	if result == "" {
		return ""
	}
	return result
}
