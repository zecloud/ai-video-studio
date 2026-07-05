package contentunderstanding

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
	"time"
)

const (
	DefaultAPIVersion       = "2025-11-01"
	PrebuiltVideoAnalyzerID = "prebuilt-videoSearch"
)

var (
	ErrInvalidConfig            = errors.New("invalid Content Understanding configuration")
	ErrInvalidAnalysisRequest   = errors.New("invalid Content Understanding analysis request")
	ErrHTTPClientMissing        = errors.New("Content Understanding HTTP client is not configured")
	ErrUnexpectedStatus         = errors.New("unexpected Content Understanding response status")
	ErrOperationLocationMissing = errors.New("Content Understanding operation location is missing")
)

type VideoAsset struct {
	ID           string `json:"id"`
	CloudAssetID string `json:"cloudAssetId"`
	Name         string `json:"name"`
	DurationMS   int64  `json:"durationMs"`
	SourceURL    string `json:"sourceUrl,omitempty"`
}

type Scene struct {
	ID        string   `json:"id"`
	StartMS   int64    `json:"startMs"`
	EndMS     int64    `json:"endMs"`
	Labels    []string `json:"labels"`
	Summary   string   `json:"summary,omitempty"`
	Highlight bool     `json:"highlight"`
}

type TranscriptSegment struct {
	StartMS int64   `json:"startMs"`
	EndMS   int64   `json:"endMs"`
	Text    string  `json:"text"`
	Speaker string  `json:"speaker,omitempty"`
	Score   float64 `json:"score,omitempty"`
}

type HighlightCandidate struct {
	ID      string  `json:"id"`
	StartMS int64   `json:"startMs"`
	EndMS   int64   `json:"endMs"`
	Reason  string  `json:"reason"`
	Score   float64 `json:"score"`
}

type EditSuggestion struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	SceneIDs    []string `json:"sceneIds"`
}

type AnalysisResult struct {
	Asset       VideoAsset           `json:"asset"`
	JobID       string               `json:"jobId,omitempty"`
	Status      string               `json:"status,omitempty"`
	Scenes      []Scene              `json:"scenes"`
	Transcript  []TranscriptSegment  `json:"transcript"`
	Highlights  []HighlightCandidate `json:"highlights"`
	Suggestions []EditSuggestion     `json:"suggestions"`
}

type ServiceStatus struct {
	Configured bool   `json:"configured"`
	Endpoint   string `json:"endpoint,omitempty"`
	AnalyzerID string `json:"analyzerId,omitempty"`
	APIVersion string `json:"apiVersion,omitempty"`
	SourceMode string `json:"sourceMode,omitempty"`
	Message    string `json:"message"`
}

type Config struct {
	Endpoint       string `json:"endpoint"`
	APIKey         string `json:"-"`
	AnalyzerID     string `json:"analyzerId"`
	APIVersion     string `json:"apiVersion"`
	SourceMode     string `json:"sourceMode"`
	PollIntervalMS int    `json:"pollIntervalMs,omitempty"`
}

type SubmitResponse struct {
	JobID             string `json:"jobId"`
	OperationLocation string `json:"operationLocation"`
	Status            string `json:"status"`
}

type Client struct {
	HTTPClient HTTPClient
	Config     Config
}

type HTTPClient interface {
	Do(*http.Request) (*http.Response, error)
}

type Service interface {
	Status(context.Context) (ServiceStatus, error)
	Submit(context.Context, VideoAsset) (string, error)
	GetResult(context.Context, string) (AnalysisResult, error)
	PollResult(ctx context.Context, operationLocation string) (AnalysisResult, error)
}

func NewClient(config Config, httpClient HTTPClient) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{Config: config, HTTPClient: httpClient}
}

func (c *Client) Status(context.Context) (ServiceStatus, error) {
	config := c.normalizedConfig()
	status := ServiceStatus{
		Configured: config.Endpoint != "" && config.APIKey != "",
		Endpoint:   config.Endpoint,
		AnalyzerID: config.AnalyzerID,
		APIVersion: config.APIVersion,
		SourceMode: config.SourceMode,
	}
	if status.Configured {
		status.Message = "Azure Content Understanding is configured."
	} else {
		status.Message = "Azure Content Understanding endpoint/API key are not configured."
	}
	return status, nil
}

func (c *Client) Submit(ctx context.Context, asset VideoAsset) (string, error) {
	submitted, err := c.SubmitAnalysis(ctx, asset)
	if err != nil {
		return "", err
	}
	return submitted.JobID, nil
}

func (c *Client) SubmitAnalysis(ctx context.Context, asset VideoAsset) (SubmitResponse, error) {
	if c == nil {
		return SubmitResponse{}, fmt.Errorf("%w: nil client", ErrInvalidConfig)
	}
	if c.HTTPClient == nil {
		return SubmitResponse{}, ErrHTTPClientMissing
	}
	config := c.normalizedConfig()
	if err := config.validate(); err != nil {
		return SubmitResponse{}, err
	}
	if strings.TrimSpace(asset.ID) == "" {
		return SubmitResponse{}, fmt.Errorf("%w: asset id is required", ErrInvalidAnalysisRequest)
	}
	if strings.TrimSpace(asset.SourceURL) == "" {
		return SubmitResponse{}, fmt.Errorf("%w: accessible source URL is required", ErrInvalidAnalysisRequest)
	}

	body, err := json.Marshal(map[string]string{"url": asset.SourceURL})
	if err != nil {
		return SubmitResponse{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, config.analyzeURL(), bytes.NewReader(body))
	if err != nil {
		return SubmitResponse{}, err
	}
	config.authorize(req)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return SubmitResponse{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return SubmitResponse{}, fmt.Errorf("%w: analyze returned %s", ErrUnexpectedStatus, resp.Status)
	}

	var operation analysisOperationResponse
	if err := json.NewDecoder(resp.Body).Decode(&operation); err != nil && !errors.Is(err, io.EOF) {
		return SubmitResponse{}, err
	}
	location := resp.Header.Get("Operation-Location")
	if location == "" {
		location = resultURL(config.Endpoint, operation.ID, config.APIVersion)
	}
	if location == "" {
		return SubmitResponse{}, ErrOperationLocationMissing
	}
	return SubmitResponse{JobID: operation.ID, OperationLocation: location, Status: operation.Status}, nil
}

func (c *Client) GetResult(ctx context.Context, jobID string) (AnalysisResult, error) {
	if c == nil {
		return AnalysisResult{}, fmt.Errorf("%w: nil client", ErrInvalidConfig)
	}
	if c.HTTPClient == nil {
		return AnalysisResult{}, ErrHTTPClientMissing
	}
	config := c.normalizedConfig()
	if err := config.validate(); err != nil {
		return AnalysisResult{}, err
	}
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return AnalysisResult{}, fmt.Errorf("%w: job id is required", ErrInvalidAnalysisRequest)
	}
	return c.getResultURL(ctx, resultURL(config.Endpoint, jobID, config.APIVersion))
}

func (c *Client) PollResult(ctx context.Context, operationLocation string) (AnalysisResult, error) {
	config := c.normalizedConfig()
	interval := time.Duration(config.PollIntervalMS) * time.Millisecond
	if interval <= 0 {
		interval = time.Second
	}
	for {
		result, err := c.getResultURL(ctx, operationLocation)
		if err != nil {
			return AnalysisResult{}, err
		}
		switch strings.ToLower(result.Status) {
		case "succeeded", "failed", "canceled", "cancelled":
			return result, nil
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return AnalysisResult{}, ctx.Err()
		case <-timer.C:
		}
	}
}

func (c *Client) getResultURL(ctx context.Context, operationLocation string) (AnalysisResult, error) {
	config := c.normalizedConfig()
	if strings.TrimSpace(operationLocation) == "" {
		return AnalysisResult{}, ErrOperationLocationMissing
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, operationLocation, nil)
	if err != nil {
		return AnalysisResult{}, err
	}
	config.authorize(req)
	req.Header.Set("Accept", "application/json")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return AnalysisResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return AnalysisResult{}, fmt.Errorf("%w: get result returned %s", ErrUnexpectedStatus, resp.Status)
	}

	var operation analysisOperationResponse
	if err := json.NewDecoder(resp.Body).Decode(&operation); err != nil {
		return AnalysisResult{}, err
	}
	return normalizeOperation(operation), nil
}

func (c *Client) normalizedConfig() Config {
	if c == nil {
		return Config{}
	}
	return c.Config.normalized()
}

type analysisOperationResponse struct {
	ID     string          `json:"id"`
	Status string          `json:"status"`
	Result analysisPayload `json:"result"`
}

type analysisPayload struct {
	AnalyzerID string            `json:"analyzerId"`
	Contents   []analysisContent `json:"contents"`
}

type analysisContent struct {
	Markdown string                   `json:"markdown"`
	Fields   map[string]analysisField `json:"fields"`
}

type analysisField struct {
	Type    string          `json:"type"`
	Value   json.RawMessage `json:"value"`
	Content string          `json:"content"`
}

func normalizeOperation(operation analysisOperationResponse) AnalysisResult {
	result := AnalysisResult{
		JobID:       operation.ID,
		Status:      operation.Status,
		Scenes:      []Scene{},
		Transcript:  []TranscriptSegment{},
		Highlights:  []HighlightCandidate{},
		Suggestions: []EditSuggestion{},
	}
	for index, content := range operation.Result.Contents {
		if strings.TrimSpace(content.Markdown) != "" {
			result.Suggestions = append(result.Suggestions, EditSuggestion{
				ID:          fmt.Sprintf("summary-%d", index+1),
				Title:       "Content Understanding summary",
				Description: content.Markdown,
			})
		}
	}
	return result
}

func (c Config) normalized() Config {
	c.Endpoint = strings.TrimRight(strings.TrimSpace(c.Endpoint), "/")
	c.APIKey = strings.TrimSpace(c.APIKey)
	c.AnalyzerID = strings.TrimSpace(c.AnalyzerID)
	if c.AnalyzerID == "" {
		c.AnalyzerID = PrebuiltVideoAnalyzerID
	}
	c.APIVersion = strings.TrimSpace(c.APIVersion)
	if c.APIVersion == "" {
		c.APIVersion = DefaultAPIVersion
	}
	c.SourceMode = strings.TrimSpace(c.SourceMode)
	if c.SourceMode == "" {
		c.SourceMode = "https_url"
	}
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
	if c.AnalyzerID == "" {
		return fmt.Errorf("%w: analyzer id is required", ErrInvalidConfig)
	}
	if c.APIVersion == "" {
		return fmt.Errorf("%w: API version is required", ErrInvalidConfig)
	}
	return nil
}

func (c Config) analyzeURL() string {
	return fmt.Sprintf("%s/contentunderstanding/analyzers/%s:analyze?api-version=%s", c.Endpoint, url.PathEscape(c.AnalyzerID), url.QueryEscape(c.APIVersion))
}

func (c Config) authorize(req *http.Request) {
	req.Header.Set("Ocp-Apim-Subscription-Key", c.APIKey)
}

func resultURL(endpoint, jobID, apiVersion string) string {
	if strings.TrimSpace(jobID) == "" {
		return ""
	}
	return fmt.Sprintf("%s/contentunderstanding/analyzerResults/%s?api-version=%s", strings.TrimRight(endpoint, "/"), url.PathEscape(jobID), url.QueryEscape(apiVersion))
}
