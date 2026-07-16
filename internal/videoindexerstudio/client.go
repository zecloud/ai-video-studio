package videoindexerstudio

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

type HTTPClient interface {
	Do(*http.Request) (*http.Response, error)
}

type Config struct {
	Endpoint string
	APIKey   string
}

type Client struct {
	cfg  Config
	http HTTPClient
}

func NewClient(cfg Config, httpClient HTTPClient) (*Client, error) {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	cfg = cfg.normalize()
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &Client{cfg: cfg, http: httpClient}, nil
}

func (c Config) normalize() Config {
	c.Endpoint = strings.TrimRight(strings.TrimSpace(c.Endpoint), "/")
	c.APIKey = strings.TrimSpace(c.APIKey)
	return c
}

func (c Config) validate() error {
	if _, err := NormalizeEndpoint(c.Endpoint); err != nil {
		return err
	}
	if c.APIKey == "" {
		return fmt.Errorf("%w: api key is required", ErrInvalidConfig)
	}
	return nil
}

func (c Config) authorize(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	req.Header.Set("X-API-Key", c.APIKey)
}

func (c *Client) CreateJob(ctx context.Context, req CreateJobRequest) (*JobResponse, error) {
	if c == nil {
		return nil, fmt.Errorf("%w: nil client", ErrInvalidConfig)
	}
	if err := c.cfg.validate(); err != nil {
		return nil, err
	}
	req = req.normalize()
	if err := req.Validate(); err != nil {
		return nil, err
	}

	var out JobResponse
	if err := c.doJSON(ctx, http.MethodPost, "/api/v1/jobs", req, &out); err != nil {
		return nil, fmt.Errorf("create job: %w", err)
	}
	if err := out.Validate(); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) GetJob(ctx context.Context, id string) (*JobResponse, error) {
	if c == nil {
		return nil, fmt.Errorf("%w: nil client", ErrInvalidConfig)
	}
	if err := c.cfg.validate(); err != nil {
		return nil, err
	}
	if err := validateID(id, "id"); err != nil {
		return nil, err
	}

	var out JobResponse
	if err := c.doJSON(ctx, http.MethodGet, "/api/v1/jobs/"+url.PathEscape(strings.TrimSpace(id)), nil, &out); err != nil {
		return nil, fmt.Errorf("get job: %w", err)
	}
	if err := out.Validate(); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) ListJobs(ctx context.Context) (*JobListResponse, error) {
	if c == nil {
		return nil, fmt.Errorf("%w: nil client", ErrInvalidConfig)
	}
	if err := c.cfg.validate(); err != nil {
		return nil, err
	}

	var out JobListResponse
	if err := c.doJSON(ctx, http.MethodGet, "/api/v1/jobs", nil, &out); err != nil {
		return nil, fmt.Errorf("list jobs: %w", err)
	}
	if err := out.Validate(); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) CancelJob(ctx context.Context, id string) (*JobResponse, error) {
	if c == nil {
		return nil, fmt.Errorf("%w: nil client", ErrInvalidConfig)
	}

	if err := c.cfg.validate(); err != nil {
		return nil, err
	}

	if err := validateID(id, "id"); err != nil {
		return nil, err
	}

	var out JobResponse
	path := "/api/v1/jobs/" + url.PathEscape(strings.TrimSpace(id)) + "/cancel"
	if err := c.doJSON(ctx, http.MethodPost, path, nil, &out); err != nil {
		return nil, fmt.Errorf("cancel job: %w", err)
	}
	if err := out.Validate(); err != nil {
		return nil, err
	}
	return &out, nil
}

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
	req.Header.Set("User-Agent", "videoindexerstudio-client/1")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.redirectSafeHTTPClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return decodeError(resp)
	}

	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil && err != io.EOF {
		return err
	}
	return nil
}

func (c *Client) redirectSafeHTTPClient() HTTPClient {
	client, ok := c.http.(*http.Client)
	if !ok || client == nil {
		return c.http
	}
	clone := *client
	clone.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &clone
}

type ResponseError struct {
	StatusCode int
	APIErrorResponse
	Body string
}

func (e *ResponseError) Error() string {
	if e == nil {
		return "<nil>"
	}
	message := strings.TrimSpace(e.Message)
	if message == "" {
		message = strings.TrimSpace(e.Body)
	}
	if message == "" {
		return fmt.Sprintf("%v: %s", ErrUnexpectedStatus, http.StatusText(e.StatusCode))
	}
	if code := strings.TrimSpace(e.Code); code != "" {
		return fmt.Sprintf("%v: %s (%s)", ErrUnexpectedStatus, message, code)
	}
	return fmt.Sprintf("%v: %s", ErrUnexpectedStatus, message)
}

func (e *ResponseError) Unwrap() error {
	return ErrUnexpectedStatus
}

func (e *ResponseError) APIError() APIErrorResponse {
	if e == nil {
		return APIErrorResponse{}
	}
	return e.APIErrorResponse
}

func decodeError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
	text := strings.TrimSpace(string(body))
	if text == "" {
		return &ResponseError{
			StatusCode: resp.StatusCode,
			APIErrorResponse: APIErrorResponse{
				Code:      "http_status",
				Message:   resp.Status,
				Retryable: classifyHTTPStatus(resp.StatusCode),
			},
		}
	}

	var apiErr APIErrorResponse
	if json.Unmarshal(body, &apiErr) == nil && apiErr.Message != "" {
		return &ResponseError{StatusCode: resp.StatusCode, APIErrorResponse: apiErr}
	}
	return &ResponseError{
		StatusCode: resp.StatusCode,
		APIErrorResponse: APIErrorResponse{
			Code:      "http_status",
			Message:   text,
			Retryable: classifyHTTPStatus(resp.StatusCode),
		},
		Body: text,
	}
}

func classifyHTTPStatus(status int) bool {
	switch {
	case status == http.StatusRequestTimeout,
		status == http.StatusTooManyRequests,
		status == http.StatusConflict,
		status == http.StatusLocked,
		status == http.StatusFailedDependency,
		status == http.StatusTooEarly,
		status >= http.StatusInternalServerError:
		return true
	default:
		return false
	}
}
