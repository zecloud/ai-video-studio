package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
)

// OneDriveDownloader downloads content from Microsoft Graph without ever
// touching local disk. Callers are expected to stream the returned body
// directly into the blob upload path.
type OneDriveDownloader struct {
	httpClient *http.Client
}

// NewOneDriveDownloader creates a downloader backed by a plain net/http
// client. A dedicated client (rather than http.DefaultClient) lets us tune
// timeouts independently of other outbound calls.
func NewOneDriveDownloader() *OneDriveDownloader {
	return &OneDriveDownloader{
		httpClient: &http.Client{},
	}
}

const graphBaseURL = "https://graph.microsoft.com/v1.0"

// DownloadItem fetches the content stream of a OneDrive drive item using a
// delegated access token supplied by the desktop app. The caller owns the
// returned io.ReadCloser and must close it once the stream has been
// consumed (e.g. after the blob upload completes).
func (d *OneDriveDownloader) DownloadItem(ctx context.Context, itemID, token string) (io.ReadCloser, int64, error) {
	if itemID == "" {
		return nil, 0, fmt.Errorf("oneDriveItemID is required")
	}
	if token == "" {
		return nil, 0, fmt.Errorf("oneDriveToken is required")
	}

	url := fmt.Sprintf("%s/me/drive/items/%s/content", graphBaseURL, itemID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("building OneDrive download request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := d.httpClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("requesting OneDrive content: %w", err)
	}

	// Microsoft Graph returns a redirect to a pre-authenticated download
	// URL for /content by default, but http.Client follows redirects
	// automatically, so resp here already points at the final content
	// response.
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, 0, fmt.Errorf("OneDrive download failed: status %d: %s", resp.StatusCode, string(body))
	}

	return resp.Body, resp.ContentLength, nil
}
