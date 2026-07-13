package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

const (
	defaultARMBaseURL  = "https://management.azure.com"
	defaultVIBaseURL   = "https://api.videoindexer.ai"
	defaultAPIVersion  = "2025-04-01"
	defaultPollTimeout = 30 * time.Minute
	maxVideoNameLength = 80
)

type VideoIndexerConfig struct {
	SubscriptionID string
	ResourceGroup  string
	AccountName    string
	AccountID      string
	Location       string
	ARMBaseURL     string
	APIBaseURL     string
	APIVersion     string
	PollTimeout    time.Duration
}

func (c VideoIndexerConfig) Normalize() VideoIndexerConfig {
	c.SubscriptionID = strings.TrimSpace(c.SubscriptionID)
	c.ResourceGroup = strings.TrimSpace(c.ResourceGroup)
	c.AccountName = strings.TrimSpace(c.AccountName)
	c.AccountID = strings.TrimSpace(c.AccountID)
	c.Location = strings.TrimSpace(c.Location)
	c.ARMBaseURL = strings.TrimRight(strings.TrimSpace(c.ARMBaseURL), "/")
	c.APIBaseURL = strings.TrimRight(strings.TrimSpace(c.APIBaseURL), "/")
	c.APIVersion = strings.TrimSpace(c.APIVersion)
	if c.ARMBaseURL == "" {
		c.ARMBaseURL = defaultARMBaseURL
	}
	if c.APIBaseURL == "" {
		c.APIBaseURL = defaultVIBaseURL
	}
	if c.APIVersion == "" {
		c.APIVersion = defaultAPIVersion
	}
	if c.PollTimeout <= 0 {
		c.PollTimeout = defaultPollTimeout
	}
	return c
}

func (c VideoIndexerConfig) Validate() error {
	c = c.Normalize()
	if c.SubscriptionID == "" {
		return fmt.Errorf("%w: subscription id is required", ErrInvalidConfig)
	}
	if c.ResourceGroup == "" {
		return fmt.Errorf("%w: resource group is required", ErrInvalidConfig)
	}
	if c.AccountName == "" {
		return fmt.Errorf("%w: account name is required", ErrInvalidConfig)
	}
	if c.PollTimeout <= 0 {
		return fmt.Errorf("%w: poll timeout must be positive", ErrInvalidConfig)
	}
	if _, err := url.ParseRequestURI(c.ARMBaseURL); err != nil {
		return fmt.Errorf("%w: arm base url must be absolute: %v", ErrInvalidConfig, err)
	}
	if _, err := url.ParseRequestURI(c.APIBaseURL); err != nil {
		return fmt.Errorf("%w: api base url must be absolute: %v", ErrInvalidConfig, err)
	}
	return nil
}

type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type waitFunc func(context.Context, time.Duration) error

type VideoIndexerClient struct {
	cfg        VideoIndexerConfig
	credential azcore.TokenCredential
	http       HTTPDoer
	wait       waitFunc
	rng        *rand.Rand
	obs        *Observability

	mu      sync.Mutex
	account ResolvedVideoIndexerAccount
	cached  bool
}

type ResolvedVideoIndexerAccount struct {
	SubscriptionID string
	ResourceGroup  string
	AccountName    string
	AccountID      string
	Location       string
}

type videoIndexerARMAccountResponse struct {
	Location   string `json:"location"`
	Properties struct {
		AccountID string `json:"accountId"`
		ID        string `json:"id"`
	} `json:"properties"`
}

type videoIndexerAccessTokenRequest struct {
	PermissionType string `json:"permissionType"`
	Scope          string `json:"scope"`
	ProjectID      string `json:"projectId,omitempty"`
	VideoID        string `json:"videoId,omitempty"`
}

type videoIndexerAccessTokenResponse struct {
	AccessToken string `json:"accessToken"`
}

type videoIndexerUploadResponse struct {
	ID      string `json:"id"`
	VideoID string `json:"videoId"`
}

type videoIndexerIndexResponse struct {
	ID      string          `json:"id"`
	VideoID string          `json:"videoId"`
	State   string          `json:"state"`
	Raw     json.RawMessage `json:"-"`
}

type videoIndexerVideoListResponse struct {
	Results []struct {
		ID      string `json:"id"`
		VideoID string `json:"videoId"`
	} `json:"results"`
}
type VideoIndexData interface {
	VideoID() string
	State() string
	RawJSON() json.RawMessage
}

type VideoIndexerAPI interface {
	UploadVideoURL(ctx context.Context, readURL, sourceName, externalID string) (string, error)
	PollVideoIndex(ctx context.Context, videoID string, timeout time.Duration) (VideoIndexData, error)
	PollTimeout() time.Duration
}

type RawVideoIndex struct {
	videoID string
	state   string
	raw     json.RawMessage
}

func (r RawVideoIndex) VideoID() string          { return r.videoID }
func (r RawVideoIndex) State() string            { return r.state }
func (r RawVideoIndex) RawJSON() json.RawMessage { return append(json.RawMessage(nil), r.raw...) }

func NewVideoIndexerClient(cfg VideoIndexerConfig, credential azcore.TokenCredential, httpClient HTTPDoer) (*VideoIndexerClient, error) {
	cfg = cfg.Normalize()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if credential == nil {
		return nil, fmt.Errorf("%w: credential is required", ErrInvalidConfig)
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &VideoIndexerClient{
		cfg:        cfg,
		credential: credential,
		http:       httpClient,
		wait:       sleepContext,
		rng:        rand.New(rand.NewSource(time.Now().UnixNano())),
	}, nil
}

func NewManagedIdentityVideoIndexerClient(cfg VideoIndexerConfig, clientID string, httpClient HTTPDoer) (*VideoIndexerClient, error) {
	options := &azidentity.ManagedIdentityCredentialOptions{}
	if clientID = strings.TrimSpace(clientID); clientID != "" {
		options.ID = azidentity.ClientID(clientID)
	}
	cred, err := azidentity.NewManagedIdentityCredential(options)
	if err != nil {
		return nil, fmt.Errorf("creating managed identity credential: %w", err)
	}
	return NewVideoIndexerClient(cfg, cred, httpClient)
}

func (c *VideoIndexerClient) AccountAccessToken(ctx context.Context) (string, error) {
	return c.generateAccessToken(ctx, videoIndexerAccessTokenRequest{
		PermissionType: "Contributor",
		Scope:          "Account",
	})
}

func (c *VideoIndexerClient) PollTimeout() time.Duration {
	if c == nil || c.cfg.PollTimeout <= 0 {
		return defaultPollTimeout
	}
	return c.cfg.PollTimeout
}

// GetVideoIndexOnce retrieves the current remote state without waiting. Durable
// orchestrations use it with a durable timer instead of holding a worker while
// Video Indexer is processing a video.
func (c *VideoIndexerClient) GetVideoIndexOnce(ctx context.Context, videoID string) (VideoIndexData, error) {
	account, err := c.ResolveAccount(ctx)
	if err != nil {
		return nil, err
	}
	accessToken, err := c.AccountAccessToken(ctx)
	if err != nil {
		return nil, err
	}
	index, _, err := c.getVideoIndex(ctx, account, videoID, accessToken)
	return index, err
}

func (c *VideoIndexerClient) ResolveAccount(ctx context.Context) (ResolvedVideoIndexerAccount, error) {
	if c == nil {
		return ResolvedVideoIndexerAccount{}, fmt.Errorf("%w: nil client", ErrInvalidConfig)
	}
	if err := ctx.Err(); err != nil {
		return ResolvedVideoIndexerAccount{}, err
	}
	if c.cfg.AccountID != "" && c.cfg.Location != "" {
		return ResolvedVideoIndexerAccount{
			SubscriptionID: c.cfg.SubscriptionID,
			ResourceGroup:  c.cfg.ResourceGroup,
			AccountName:    c.cfg.AccountName,
			AccountID:      c.cfg.AccountID,
			Location:       c.cfg.Location,
		}, nil
	}

	c.mu.Lock()
	if c.cached {
		account := c.account
		c.mu.Unlock()
		return account, nil
	}
	c.mu.Unlock()

	token, err := c.armToken(ctx)
	if err != nil {
		return ResolvedVideoIndexerAccount{}, err
	}

	armURL := fmt.Sprintf("%s/subscriptions/%s/resourceGroups/%s/providers/Microsoft.VideoIndexer/accounts/%s?api-version=%s",
		c.cfg.ARMBaseURL,
		url.PathEscape(c.cfg.SubscriptionID),
		url.PathEscape(c.cfg.ResourceGroup),
		url.PathEscape(c.cfg.AccountName),
		url.QueryEscape(c.cfg.APIVersion),
	)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, armURL, nil)
	if err != nil {
		return ResolvedVideoIndexerAccount{}, &ServiceError{Status: http.StatusInternalServerError, Code: "request_build_failed", Message: redactURLsInText(err.Error()), Retryable: false}
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return ResolvedVideoIndexerAccount{}, &ServiceError{Status: http.StatusBadGateway, Code: "video_indexer_arm_request_failed", Message: redactURLsInText(err.Error()), Retryable: true}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ResolvedVideoIndexerAccount{}, decodeHTTPError(resp, "Video Indexer account lookup")
	}

	var decoded videoIndexerARMAccountResponse
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return ResolvedVideoIndexerAccount{}, &ServiceError{Status: http.StatusInternalServerError, Code: "account_decode_failed", Message: redactURLsInText(err.Error()), Retryable: false}
	}
	account := ResolvedVideoIndexerAccount{
		SubscriptionID: c.cfg.SubscriptionID,
		ResourceGroup:  c.cfg.ResourceGroup,
		AccountName:    c.cfg.AccountName,
		AccountID:      firstNonEmpty(decoded.Properties.AccountID, decoded.Properties.ID),
		Location:       decoded.Location,
	}
	if account.AccountID == "" || account.Location == "" {
		return ResolvedVideoIndexerAccount{}, newServiceError(http.StatusInternalServerError, "account_lookup_failed", "video indexer account lookup did not return account id and location", false)
	}

	c.mu.Lock()
	c.account = account
	c.cached = true
	c.mu.Unlock()
	return account, nil
}

// FindVideoByExternalID reconciles an ambiguous upload before an activity retries
// or replays. UploadVideoURL always uses the durable job ID as externalId.
func (c *VideoIndexerClient) FindVideoByExternalID(ctx context.Context, externalID string) (string, error) {
	if strings.TrimSpace(externalID) == "" {
		return "", nil
	}
	account, err := c.ResolveAccount(ctx)
	if err != nil {
		return "", err
	}
	accessToken, err := c.AccountAccessToken(ctx)
	if err != nil {
		return "", err
	}
	values := url.Values{}
	values.Set("accessToken", accessToken)
	values.Set("externalId", externalID)
	lookupURL := fmt.Sprintf("%s/%s/Accounts/%s/Videos?%s",
		c.cfg.APIBaseURL,
		url.PathEscape(account.Location),
		url.PathEscape(account.AccountID),
		values.Encode(),
	)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, lookupURL, nil)
	if err != nil {
		return "", &ServiceError{Status: http.StatusInternalServerError, Code: "request_build_failed", Message: redactURLsInText(err.Error()), Retryable: false}
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", &ServiceError{Status: http.StatusBadGateway, Code: "video_indexer_lookup_failed", Message: redactURLsInText(err.Error()), Retryable: true}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", decodeHTTPError(resp, "Video Indexer video lookup")
	}
	var decoded videoIndexerVideoListResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxRequestBodyBytes)).Decode(&decoded); err != nil {
		return "", &ServiceError{Status: http.StatusInternalServerError, Code: "video_lookup_decode_failed", Message: redactURLsInText(err.Error()), Retryable: false}
	}
	if len(decoded.Results) == 0 {
		return "", nil
	}
	videoID := firstNonEmpty(decoded.Results[0].ID, decoded.Results[0].VideoID)
	if videoID == "" {
		return "", newServiceError(http.StatusInternalServerError, "video_lookup_missing_video_id", "Video Indexer video lookup returned an empty video id", false)
	}
	return videoID, nil
}
func (c *VideoIndexerClient) UploadVideoURL(ctx context.Context, videoURL, videoName, externalID string) (videoID string, err error) {
	account, err := c.ResolveAccount(ctx)
	if err != nil {
		return "", err
	}
	accountToken, err := c.AccountAccessToken(ctx)
	if err != nil {
		return "", err
	}
	start := time.Now()
	var span trace.Span
	if c.obs != nil {
		ctx, span = c.obs.StartSpan(ctx, "vi.submit", attribute.String("stage", "vi.submit"))
		defer func() {
			c.obs.FinishSpan(ctx, span, "vi.submit", start, []attribute.KeyValue{attribute.String("stage", "vi.submit")}, err)
		}()
	}
	uploadURL, err := c.buildVideoURL(account, accountToken, videoURL, videoName, externalID)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, uploadURL, bytes.NewReader([]byte("{}")))
	if err != nil {
		err = &ServiceError{Status: http.StatusInternalServerError, Code: "request_build_failed", Message: redactURLsInText(err.Error()), Retryable: false}
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		err = &ServiceError{Status: http.StatusBadGateway, Code: "video_indexer_upload_failed", Message: redactURLsInText(err.Error()), Retryable: true}
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		err = decodeHTTPError(resp, "Video Indexer upload")
		return "", err
	}

	var decoded videoIndexerUploadResponse
	if decodeErr := json.NewDecoder(resp.Body).Decode(&decoded); decodeErr != nil && decodeErr != io.EOF {
		err = &ServiceError{Status: http.StatusInternalServerError, Code: "upload_decode_failed", Message: redactURLsInText(decodeErr.Error()), Retryable: false}
		return "", err
	}
	videoID = firstNonEmpty(decoded.ID, decoded.VideoID)
	if videoID == "" {
		err = newServiceError(http.StatusInternalServerError, "upload_missing_video_id", "video indexer upload response did not include a video id", false)
		return "", err
	}
	return videoID, nil
}

func (c *VideoIndexerClient) PollVideoIndex(ctx context.Context, videoID string, timeout time.Duration) (index VideoIndexData, err error) {
	account, err := c.ResolveAccount(ctx)
	if err != nil {
		return nil, err
	}
	accountToken, err := c.AccountAccessToken(ctx)
	if err != nil {
		return nil, err
	}
	start := time.Now()
	var span trace.Span
	if c.obs != nil {
		ctx, span = c.obs.StartSpan(ctx, "vi.poll", attribute.String("stage", "vi.poll"), attribute.String("video_id", videoID))
		defer func() {
			c.obs.FinishSpan(ctx, span, "vi.poll", start, []attribute.KeyValue{attribute.String("stage", "vi.poll"), attribute.String("video_id", videoID)}, err)
		}()
	}
	if timeout <= 0 {
		timeout = c.cfg.PollTimeout
	}
	if timeout <= 0 {
		timeout = defaultPollTimeout
	}

	deadline := time.Now().Add(timeout)
	attempt := 0
	for {
		if ctxErr := ctx.Err(); ctxErr != nil {
			err = ctxErr
			return nil, err
		}
		if time.Now().After(deadline) {
			err = newServiceError(http.StatusGatewayTimeout, "video_index_timeout", fmt.Sprintf("video indexer polling timed out after %s", timeout), true)
			return nil, err
		}

		index, resp, err := c.getVideoIndex(ctx, account, videoID, accountToken)
		if err == nil {
			switch normalizeVideoState(index.State()) {
			case "processed":
				return index, nil
			case "failed", "canceled", "cancelled":
				err = videoIndexerTerminalError(index)
				return nil, err
			default:
				if waitErr := c.wait(ctx, c.nextDelay(resp, attempt)); waitErr != nil {
					err = waitErr
					return nil, err
				}

				attempt++
				continue
			}
		}

		var se *ServiceError
		if errors.As(err, &se) && se.Retryable && (se.Status == http.StatusTooManyRequests || se.Status == http.StatusServiceUnavailable) {
			if c.obs != nil {
				c.obs.RecordRetry(ctx, "vi.poll", se.Status, attribute.String("video_id", videoID))
			}
			delay := c.nextDelay(resp, attempt)
			if waitErr := c.wait(ctx, delay); waitErr != nil {
				err = waitErr
				return nil, err
			}
			attempt++
			continue
		}
		return nil, err
	}
}

func videoIndexerTerminalError(index VideoIndexData) error {
	state := "unknown"
	var raw json.RawMessage
	if index != nil && strings.TrimSpace(index.State()) != "" {
		state = index.State()
	}
	if index != nil {
		raw = index.RawJSON()
	}
	message := videoIndexerFailureMessage(raw)
	if message == "" {
		message = fmt.Sprintf("Video Indexer reported terminal state %s", state)
	} else {
		message = fmt.Sprintf("Video Indexer reported terminal state %s: %s", state, message)
	}
	return newServiceError(http.StatusUnprocessableEntity, "video_index_failed", message, false)
}

func videoIndexerFailureMessage(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var payload struct {
		Error        json.RawMessage `json:"error"`
		Errors       json.RawMessage `json:"errors"`
		Message      string          `json:"message"`
		ErrorMessage string          `json:"errorMessage"`
		ErrorCode    string          `json:"errorCode"`
	}
	if json.Unmarshal(raw, &payload) != nil {
		return ""
	}
	parts := make([]string, 0, 2)
	if payload.ErrorCode != "" {
		parts = append(parts, payload.ErrorCode)
	}
	for _, value := range []string{payload.ErrorMessage, payload.Message, videoIndexerNestedMessage(payload.Error), videoIndexerNestedMessage(payload.Errors)} {
		value = strings.TrimSpace(redactURLsInText(value))
		if value != "" && !videoIndexerContainsString(parts, value) {
			parts = append(parts, value)
		}
	}
	return strings.Join(parts, ": ")
}

func videoIndexerContainsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func videoIndexerNestedMessage(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var nested struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Details string `json:"details"`
	}
	if json.Unmarshal(raw, &nested) != nil {
		return ""
	}
	if nested.Code != "" && nested.Message != "" {
		return nested.Code + ": " + nested.Message
	}
	return firstNonEmpty(nested.Message, nested.Details, nested.Code)
}

func (c *VideoIndexerClient) getVideoIndex(ctx context.Context, account ResolvedVideoIndexerAccount, videoID, accessToken string) (VideoIndexData, *http.Response, error) {
	indexURL := fmt.Sprintf("%s/%s/Accounts/%s/Videos/%s/Index?%s",
		c.cfg.APIBaseURL,
		url.PathEscape(account.Location),
		url.PathEscape(account.AccountID),
		url.PathEscape(videoID),
		c.encodeQuery(map[string]string{
			"accessToken": accessToken,
		}),
	)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, indexURL, nil)
	if err != nil {
		return nil, nil, &ServiceError{Status: http.StatusInternalServerError, Code: "request_build_failed", Message: redactURLsInText(err.Error()), Retryable: false}
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, nil, &ServiceError{Status: http.StatusBadGateway, Code: "video_indexer_index_failed", Message: redactURLsInText(err.Error()), Retryable: true}
	}
	defer func() {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
	}()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, resp, decodeHTTPError(resp, "Video Indexer index")
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxRequestBodyBytes))
	if err != nil {
		return nil, resp, &ServiceError{Status: http.StatusInternalServerError, Code: "index_decode_failed", Message: redactURLsInText(err.Error()), Retryable: false}
	}
	var decoded videoIndexerIndexResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, resp, &ServiceError{Status: http.StatusInternalServerError, Code: "index_decode_failed", Message: redactURLsInText(err.Error()), Retryable: false}
	}
	decoded.Raw = append(json.RawMessage(nil), body...)
	return RawVideoIndex{videoID: firstNonEmpty(decoded.ID, decoded.VideoID, videoID), state: decoded.State, raw: decoded.Raw}, resp, nil
}

func (c *VideoIndexerClient) buildVideoURL(account ResolvedVideoIndexerAccount, accessToken, videoURL, videoName, externalID string) (string, error) {
	values := url.Values{}
	values.Set("accessToken", accessToken)
	values.Set("name", safeVideoName(videoName))
	values.Set("videoUrl", videoURL)
	values.Set("privacy", "private")
	values.Set("preventDuplicates", "true")
	if externalID != "" {
		values.Set("externalId", externalID)
	}
	return fmt.Sprintf("%s/%s/Accounts/%s/Videos?%s",
		c.cfg.APIBaseURL,
		url.PathEscape(account.Location),
		url.PathEscape(account.AccountID),
		values.Encode(),
	), nil
}

func (c *VideoIndexerClient) encodeQuery(values map[string]string) string {
	encoded := url.Values{}
	for k, v := range values {
		if v != "" {
			encoded.Set(k, v)
		}
	}
	return encoded.Encode()
}

func (c *VideoIndexerClient) nextDelay(resp *http.Response, attempt int) time.Duration {
	if resp != nil {
		if delay := parseRetryAfter(resp.Header.Get("Retry-After")); delay > 0 {
			return delay
		}
	}
	delay := time.Second << min(attempt, 5)
	if delay > 30*time.Second {
		delay = 30 * time.Second
	}
	delay = c.jitter(delay)
	if delay > 30*time.Second {
		delay = 30 * time.Second
	}
	return delay
}

func (c *VideoIndexerClient) jitter(base time.Duration) time.Duration {
	if base <= 0 {
		return 0
	}
	window := base / 4
	if window <= 0 {
		return base
	}
	minDelay := base - window
	maxDelay := base + window
	if maxDelay <= minDelay {
		return base
	}
	delta := maxDelay - minDelay
	return minDelay + time.Duration(c.rng.Int63n(int64(delta)+1))
}

func (c *VideoIndexerClient) armToken(ctx context.Context) (string, error) {
	token, err := c.credential.GetToken(ctx, policy.TokenRequestOptions{Scopes: []string{"https://management.azure.com/.default"}})
	if err != nil {
		return "", &ServiceError{Status: http.StatusBadGateway, Code: "arm_token_failed", Message: redactURLsInText(err.Error()), Retryable: true}
	}
	if token.Token == "" {
		return "", newServiceError(http.StatusBadGateway, "arm_token_failed", "managed identity returned an empty ARM token", true)
	}
	return token.Token, nil
}

func (c *VideoIndexerClient) generateAccessToken(ctx context.Context, request videoIndexerAccessTokenRequest) (string, error) {
	account, err := c.ResolveAccount(ctx)
	if err != nil {
		return "", err
	}
	token, err := c.armToken(ctx)
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(request)
	if err != nil {
		return "", err
	}
	armURL := fmt.Sprintf("%s/subscriptions/%s/resourceGroups/%s/providers/Microsoft.VideoIndexer/accounts/%s/generateAccessToken?api-version=%s",
		c.cfg.ARMBaseURL,
		url.PathEscape(account.SubscriptionID),
		url.PathEscape(account.ResourceGroup),
		url.PathEscape(account.AccountName),
		url.QueryEscape(c.cfg.APIVersion),
	)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, armURL, bytes.NewReader(body))
	if err != nil {
		return "", &ServiceError{Status: http.StatusInternalServerError, Code: "request_build_failed", Message: redactURLsInText(err.Error()), Retryable: false}
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", &ServiceError{Status: http.StatusBadGateway, Code: "video_indexer_token_request_failed", Message: redactURLsInText(err.Error()), Retryable: true}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", decodeHTTPError(resp, "Video Indexer access token")
	}

	var decoded videoIndexerAccessTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return "", &ServiceError{Status: http.StatusInternalServerError, Code: "access_token_decode_failed", Message: redactURLsInText(err.Error()), Retryable: false}
	}
	if decoded.AccessToken == "" {
		return "", newServiceError(http.StatusInternalServerError, "access_token_missing", "video indexer access token response did not include a token", false)
	}
	return decoded.AccessToken, nil
}

func parseRetryAfter(raw string) time.Duration {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(raw); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	if when, err := http.ParseTime(raw); err == nil {
		if delay := time.Until(when); delay > 0 {
			return delay
		}
	}
	return 0
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func safeVideoName(raw string) string {
	raw = strings.TrimSpace(path.Base(raw))
	if raw == "" || raw == "." || raw == ".." {
		return "video"
	}
	var b strings.Builder
	b.Grow(len(raw))
	for _, r := range raw {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r), r == '-', r == '_', r == '.', r == ' ':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	name := strings.TrimSpace(b.String())
	name = strings.Trim(name, "._-")
	if name == "" {
		name = "video"
	}
	name = truncateRunes(name, maxVideoNameLength)
	return name
}

func truncateRunes(raw string, max int) string {
	if max <= 0 || utf8.RuneCountInString(raw) <= max {
		return raw
	}
	var b strings.Builder
	b.Grow(len(raw))
	count := 0
	for _, r := range raw {
		if count >= max {
			break
		}
		b.WriteRune(r)
		count++
	}
	return strings.TrimSpace(b.String())
}

func normalizeVideoState(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
