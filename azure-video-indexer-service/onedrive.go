package main

import (
	"context"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

const defaultGraphBaseURL = "https://graph.microsoft.com/v1.0"

type OneDriveDownloadMetadata struct {
	ItemID             string
	ContentLength      int64
	ContentType        string
	FileName           string
	ETag               string
	ContentDisposition string
}

type OneDriveClient struct {
	baseURL    string
	httpClient *http.Client
	obs        *Observability
}

func NewOneDriveClient(baseURL string, httpClient *http.Client) *OneDriveClient {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = defaultGraphBaseURL
	}
	if httpClient == nil {
		httpClient = &http.Client{
			Timeout: 0,
			Transport: &http.Transport{
				ResponseHeaderTimeout: 60 * time.Second,
			},
		}
	}
	redirect := httpClient.CheckRedirect
	httpClient.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		req.Header.Del("Authorization")
		if redirect != nil {
			return redirect(req, via)
		}
		return nil
	}
	return &OneDriveClient{baseURL: baseURL, httpClient: httpClient}
}

func (c *OneDriveClient) OpenItem(ctx context.Context, itemID, token string) (reader io.ReadCloser, meta OneDriveDownloadMetadata, err error) {
	itemID = strings.TrimSpace(itemID)
	token = strings.TrimSpace(token)
	if itemID == "" {
		return nil, OneDriveDownloadMetadata{}, newServiceError(http.StatusBadRequest, "validation_failed", "oneDriveItemId is required", false)
	}
	if token == "" {
		return nil, OneDriveDownloadMetadata{}, newServiceError(http.StatusBadRequest, "validation_failed", "oneDriveAccessToken is required", false)
	}
	start := time.Now()
	var span trace.Span
	if c.obs != nil {
		ctx, span = c.obs.StartSpan(ctx, "onedrive.download", attribute.String("stage", "onedrive.download"))
		defer func() {
			c.obs.FinishSpan(ctx, span, "onedrive.download", start, []attribute.KeyValue{attribute.String("stage", "onedrive.download")}, err)
		}()
	}
	endpoint := fmt.Sprintf("%s/me/drive/items/%s/content", c.baseURL, url.PathEscape(itemID))
	req, buildErr := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if buildErr != nil {
		err = &ServiceError{Status: http.StatusInternalServerError, Code: "request_build_failed", Message: redactURLsInText(buildErr.Error()), Retryable: false, Cause: buildErr}
		return nil, OneDriveDownloadMetadata{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, doErr := c.httpClient.Do(req)
	if doErr != nil {
		err = &ServiceError{Status: http.StatusBadGateway, Code: "onedrive_request_failed", Message: redactURLsInText(doErr.Error()), Retryable: true, Cause: doErr}
		return nil, OneDriveDownloadMetadata{}, err
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		err = decodeHTTPError(resp, "OneDrive download")
		return nil, OneDriveDownloadMetadata{}, err
	}

	meta = OneDriveDownloadMetadata{
		ItemID:        itemID,
		ContentLength: resp.ContentLength,
		ContentType:   resp.Header.Get("Content-Type"),
		ETag:          resp.Header.Get("ETag"),
	}
	meta.ContentDisposition = resp.Header.Get("Content-Disposition")
	if meta.ContentDisposition != "" {
		if _, params, err := mime.ParseMediaType(meta.ContentDisposition); err == nil {
			if name := strings.TrimSpace(params["filename*"]); name != "" {
				meta.FileName = name
			} else if name := strings.TrimSpace(params["filename"]); name != "" {
				meta.FileName = name
			}
		}
	}
	if meta.FileName == "" {
		meta.FileName = strings.TrimSpace(resp.Request.URL.Path[strings.LastIndex(resp.Request.URL.Path, "/")+1:])
	}
	return resp.Body, meta, nil
}
