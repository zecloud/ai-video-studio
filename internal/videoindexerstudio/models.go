package videoindexerstudio

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const maxErrorBodyBytes = 4096

var (
	ErrInvalidConfig    = errors.New("invalid video indexer client configuration")
	ErrInvalidRequest   = errors.New("invalid video indexer request")
	ErrUnexpectedStatus = errors.New("unexpected video indexer response status")
)

var jobIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

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
	JobStatusRunning          JobStatus = "running"
	JobStatusSucceeded        JobStatus = "succeeded"
	JobStatusFailed           JobStatus = "failed"
	JobStatusCanceled         JobStatus = "canceled"
)

type CreateJobRequest struct {
	OneDriveItemID      string `json:"oneDriveItemId"`
	OneDriveAccessToken string `json:"oneDriveAccessToken"`
	SourceName          string `json:"sourceName,omitempty"`
	CallbackURL         string `json:"callbackUrl,omitempty"`
	CorrelationID       string `json:"correlationId,omitempty"`
}

type Job struct {
	ID                  string            `json:"id"`
	Status              JobStatus         `json:"status"`
	OneDriveItemID      string            `json:"oneDriveItemId"`
	SourceName          string            `json:"sourceName,omitempty"`
	CorrelationID       string            `json:"correlationId,omitempty"`
	StagingContainer    string            `json:"stagingContainer,omitempty"`
	StagedBlobName      string            `json:"stagedBlobName,omitempty"`
	VideoIndexerVideoID string            `json:"videoIndexerVideoId,omitempty"`
	VideoIndexerState   string            `json:"videoIndexerState,omitempty"`
	VideoIndexResult    *VideoIndexResult `json:"videoIndexResult,omitempty"`
	EditPlan            *EditPlan         `json:"editPlan,omitempty"`
	TimelineDrafts      []TimelineDraft   `json:"timelineDrafts,omitempty"`
	Checkpoints         []JobCheckpoint   `json:"checkpoints,omitempty"`
	CreatedAt           time.Time         `json:"createdAt"`
	UpdatedAt           time.Time         `json:"updatedAt"`
	StartedAt           *time.Time        `json:"startedAt,omitempty"`
	CompletedAt         *time.Time        `json:"completedAt,omitempty"`
	Error               *APIErrorResponse `json:"error,omitempty"`
	ClaimedBy           string            `json:"claimedBy,omitempty"`
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

type JobCheckpoint struct {
	Stage    string          `json:"stage"`
	At       time.Time       `json:"at"`
	VideoID  string          `json:"videoId,omitempty"`
	State    string          `json:"state,omitempty"`
	Detail   string          `json:"detail,omitempty"`
	Metadata json.RawMessage `json:"metadata,omitempty"`
}

type VideoIndexResult struct {
	VideoID          string             `json:"videoId"`
	State            string             `json:"state,omitempty"`
	DurationMs       int64              `json:"durationMs,omitempty"`
	DetectedLanguage string             `json:"detectedLanguage,omitempty"`
	SourceLanguage   string             `json:"sourceLanguage,omitempty"`
	SourceLanguages  []string           `json:"sourceLanguages,omitempty"`
	SourceIDs        []string           `json:"sourceIds,omitempty"`
	Videos           []VideoIndexVideo  `json:"videos,omitempty"`
	Insights         VideoIndexInsights `json:"insights"`
	TechnicalSignals *MediaSignals      `json:"technicalSignals,omitempty"`
}

type VideoIndexVideo struct {
	ID               string  `json:"id"`
	SourceID         string  `json:"sourceId,omitempty"`
	DurationMs       int64   `json:"durationMs,omitempty"`
	DetectedLanguage string  `json:"detectedLanguage,omitempty"`
	Confidence       float64 `json:"confidence,omitempty"`
}

type VideoIndexInsights struct {
	Speakers   []VideoIndexSpeaker    `json:"speakers,omitempty"`
	Scenes     []VideoIndexScene      `json:"scenes,omitempty"`
	Shots      []VideoIndexShot       `json:"shots,omitempty"`
	Keyframes  []VideoIndexKeyframe   `json:"keyframes,omitempty"`
	Transcript []VideoIndexTranscript `json:"transcript,omitempty"`
	OCR        []VideoIndexOCR        `json:"ocr,omitempty"`
	Labels     []VideoIndexLabel      `json:"labels,omitempty"`
	Objects    []VideoIndexObject     `json:"objects,omitempty"`
}

type VideoIndexSpeaker struct {
	ID            string   `json:"id"`
	Name          string   `json:"name,omitempty"`
	Language      string   `json:"language,omitempty"`
	TranscriptIDs []string `json:"transcriptIds,omitempty"`
}

type VideoIndexScene struct {
	ID         string        `json:"id"`
	IndexID    int64         `json:"indexId,omitempty"`
	SourceID   string        `json:"sourceId,omitempty"`
	StartMs    int64         `json:"startMs"`
	EndMs      int64         `json:"endMs"`
	Confidence float64       `json:"confidence,omitempty"`
	Thumbnail  *ThumbnailRef `json:"thumbnail,omitempty"`
}

type VideoIndexShot struct {
	ID          string   `json:"id"`
	IndexID     int64    `json:"indexId,omitempty"`
	SourceID    string   `json:"sourceId,omitempty"`
	StartMs     int64    `json:"startMs"`
	EndMs       int64    `json:"endMs"`
	Tags        []string `json:"tags,omitempty"`
	KeyframeIDs []string `json:"keyframeIds,omitempty"`
	Confidence  float64  `json:"confidence,omitempty"`
}

type VideoIndexKeyframe struct {
	ID         string       `json:"id"`
	IndexID    int64        `json:"indexId,omitempty"`
	SourceID   string       `json:"sourceId,omitempty"`
	ShotID     string       `json:"shotId,omitempty"`
	StartMs    int64        `json:"startMs"`
	EndMs      int64        `json:"endMs"`
	Confidence float64      `json:"confidence,omitempty"`
	Thumbnail  ThumbnailRef `json:"thumbnail"`
}

type ThumbnailRef struct {
	VideoID     string `json:"videoId,omitempty"`
	ThumbnailID string `json:"thumbnailId,omitempty"`
	URL         string `json:"url,omitempty"`
}

type VideoIndexTranscript struct {
	ID          string  `json:"id"`
	IndexID     int64   `json:"indexId,omitempty"`
	SourceID    string  `json:"sourceId,omitempty"`
	SpeakerID   string  `json:"speakerId,omitempty"`
	SpeakerName string  `json:"speakerName,omitempty"`
	Language    string  `json:"language,omitempty"`
	StartMs     int64   `json:"startMs"`
	EndMs       int64   `json:"endMs"`
	Text        string  `json:"text"`
	Confidence  float64 `json:"confidence,omitempty"`
}

type VideoIndexOCR struct {
	ID         string         `json:"id"`
	IndexID    int64          `json:"indexId,omitempty"`
	SourceID   string         `json:"sourceId,omitempty"`
	Language   string         `json:"language,omitempty"`
	Text       string         `json:"text"`
	StartMs    int64          `json:"startMs"`
	EndMs      int64          `json:"endMs"`
	Confidence float64        `json:"confidence,omitempty"`
	Bounds     VideoIndexRect `json:"bounds,omitempty"`
}

type VideoIndexRect struct {
	Left   int `json:"left,omitempty"`
	Top    int `json:"top,omitempty"`
	Width  int `json:"width,omitempty"`
	Height int `json:"height,omitempty"`
	Angle  int `json:"angle,omitempty"`
}

type VideoIndexLabel struct {
	ID          string  `json:"id"`
	IndexID     int64   `json:"indexId,omitempty"`
	SourceID    string  `json:"sourceId,omitempty"`
	Language    string  `json:"language,omitempty"`
	Name        string  `json:"name"`
	ReferenceID string  `json:"referenceId,omitempty"`
	StartMs     int64   `json:"startMs"`
	EndMs       int64   `json:"endMs"`
	Confidence  float64 `json:"confidence,omitempty"`
}

type VideoIndexObject struct {
	ID          string       `json:"id"`
	IndexID     int64        `json:"indexId,omitempty"`
	SourceID    string       `json:"sourceId,omitempty"`
	Type        string       `json:"type"`
	DisplayName string       `json:"displayName,omitempty"`
	WikiDataID  string       `json:"wikiDataId,omitempty"`
	StartMs     int64        `json:"startMs"`
	EndMs       int64        `json:"endMs"`
	Confidence  float64      `json:"confidence,omitempty"`
	Thumbnail   ThumbnailRef `json:"thumbnail,omitempty"`
}

type MediaSignals struct {
	SourceURL string            `json:"sourceUrl"`
	Duration  time.Duration     `json:"duration"`
	Video     MediaVideoSignals `json:"video"`
	Audio     MediaAudioSignals `json:"audio"`
	Silences  []SilenceInterval `json:"silences,omitempty"`
}

type MediaVideoSignals struct {
	Present bool    `json:"present"`
	Codec   string  `json:"codec,omitempty"`
	Width   int     `json:"width,omitempty"`
	Height  int     `json:"height,omitempty"`
	FPS     float64 `json:"fps,omitempty"`
}

type MediaAudioSignals struct {
	Present    bool   `json:"present"`
	Codec      string `json:"codec,omitempty"`
	Channels   int    `json:"channels,omitempty"`
	SampleRate int    `json:"sampleRate,omitempty"`
}

type SilenceInterval struct {
	Start time.Duration `json:"start"`
	End   time.Duration `json:"end"`
}

type EditPlan struct {
	SchemaVersion int              `json:"schemaVersion"`
	VideoID       string           `json:"videoId"`
	AssetID       string           `json:"assetId"`
	Title         string           `json:"title"`
	Summary       string           `json:"summary"`
	Highlights    []Highlight      `json:"highlights,omitempty"`
	Suggestions   []EditSuggestion `json:"suggestions,omitempty"`
	SourceRefs    []SourceRef      `json:"sourceRefs,omitempty"`
}

type Highlight struct {
	ID         string      `json:"id"`
	Title      string      `json:"title"`
	Reason     string      `json:"reason"`
	StartMs    int64       `json:"startMs"`
	EndMs      int64       `json:"endMs"`
	Score      float64     `json:"score"`
	SourceRefs []SourceRef `json:"sourceRefs"`
}

type EditSuggestion struct {
	ID         string          `json:"id"`
	Title      string          `json:"title"`
	Reason     string          `json:"reason"`
	StartMs    int64           `json:"startMs"`
	EndMs      int64           `json:"endMs"`
	Score      float64         `json:"score"`
	SourceRefs []SourceRef     `json:"sourceRefs"`
	Clips      []SuggestedClip `json:"clips,omitempty"`
}

type SuggestedClip struct {
	ID         string      `json:"id"`
	Title      string      `json:"title"`
	Reason     string      `json:"reason"`
	StartMs    int64       `json:"startMs"`
	EndMs      int64       `json:"endMs"`
	Score      float64     `json:"score"`
	SourceRefs []SourceRef `json:"sourceRefs"`
}

type SourceRef struct {
	RefID         string `json:"refId"`
	SourceKind    string `json:"sourceKind"`
	SourceAssetID string `json:"sourceAssetId"`
	FactKind      string `json:"factKind,omitempty"`
	StartMs       int64  `json:"startMs,omitempty"`
	EndMs         int64  `json:"endMs,omitempty"`
	Text          string `json:"text,omitempty"`
}

func (r CreateJobRequest) normalize() CreateJobRequest {
	r.OneDriveItemID = strings.TrimSpace(r.OneDriveItemID)
	r.OneDriveAccessToken = strings.TrimSpace(r.OneDriveAccessToken)
	r.SourceName = strings.TrimSpace(r.SourceName)
	r.CallbackURL = strings.TrimSpace(r.CallbackURL)
	r.CorrelationID = strings.TrimSpace(r.CorrelationID)
	return r
}

func (r CreateJobRequest) Validate() error {
	r = r.normalize()
	if r.OneDriveItemID == "" {
		return fmt.Errorf("%w: oneDriveItemId is required", ErrInvalidRequest)
	}
	if len(r.OneDriveItemID) > 256 {
		return fmt.Errorf("%w: oneDriveItemId is too long", ErrInvalidRequest)
	}
	if r.OneDriveAccessToken == "" {
		return fmt.Errorf("%w: oneDriveAccessToken is required", ErrInvalidRequest)
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

func (j Job) Validate() error {
	if err := validateID(j.ID, "id"); err != nil {
		return err
	}
	switch j.Status {
	case JobStatusQueued, JobStatusStaging, JobStatusStaged, JobStatusProcessing, JobStatusSubmitting, JobStatusIndexing, JobStatusNormalizing, JobStatusGenerating, JobStatusBuildingTimeline, JobStatusRunning, JobStatusSucceeded, JobStatusFailed, JobStatusCanceled:
	default:
		return fmt.Errorf("%w: invalid job status %q", ErrInvalidRequest, j.Status)
	}
	if strings.TrimSpace(j.OneDriveItemID) == "" {
		return fmt.Errorf("%w: oneDriveItemId is required", ErrInvalidRequest)
	}
	if j.Error != nil {
		if err := j.Error.Validate(); err != nil {
			return err
		}
	}
	return nil
}

func (r JobResponse) Validate() error {
	if err := r.Job.Validate(); err != nil {
		return err
	}
	return nil
}

func (r JobListResponse) Validate() error {
	for i, job := range r.Jobs {
		if err := job.Validate(); err != nil {
			return fmt.Errorf("%w: jobs[%d]: %v", ErrInvalidRequest, i, err)
		}
	}
	return nil
}

func (e APIErrorResponse) Validate() error {
	if strings.TrimSpace(e.Code) == "" {
		return fmt.Errorf("%w: code is required", ErrInvalidRequest)
	}
	if strings.TrimSpace(e.Message) == "" {
		return fmt.Errorf("%w: message is required", ErrInvalidRequest)
	}
	return nil
}

func validateHTTPURL(raw, field string) error {
	parsed, err := url.ParseRequestURI(strings.TrimSpace(raw))
	if err != nil {
		return fmt.Errorf("%w: %s must be an absolute URL", ErrInvalidRequest, field)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("%w: %s must use http or https", ErrInvalidRequest, field)
	}
	if parsed.Host == "" {
		return fmt.Errorf("%w: %s must include a host", ErrInvalidRequest, field)
	}
	return nil
}

func validateID(raw, field string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fmt.Errorf("%w: %s is required", ErrInvalidRequest, field)
	}
	if !jobIDPattern.MatchString(raw) {
		return fmt.Errorf("%w: %s contains invalid characters", ErrInvalidRequest, field)
	}
	return nil
}
