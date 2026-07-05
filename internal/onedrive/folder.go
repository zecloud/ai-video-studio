package onedrive

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

// DriveItem describes a file or folder returned by Microsoft Graph.
type DriveItem struct {
	ID   string    `json:"id"`
	Name string    `json:"name"`
	Size int64     `json:"size"`
	File *FileInfo `json:"file,omitempty"`
}

// FileInfo indicates the item is a file and carries its MIME type.
type FileInfo struct {
	MimeType string `json:"mimeType"`
}

type graphChildrenResponse struct {
	Value    []DriveItem `json:"value"`
	NextLink string      `json:"@odata.nextLink,omitempty"`
}

// ListFolderItems lists all files and folders at the given OneDrive path.
// If the client is configured for app-folder access (Destination.Mode == "app_folder"),
// the path is resolved relative to /me/drive/special/approot and the destination
// path prefix is automatically stripped (e.g. "Apps/AI Video Studio/Imports" → "Imports").
// Otherwise it uses /me/drive/root:.
func (c *Client) ListFolderItems(ctx context.Context, folderPath string) ([]DriveItem, error) {
	if c == nil {
		return nil, fmt.Errorf("onedrive client is nil")
	}
	folderPath = strings.TrimPrefix(folderPath, "/")
	folderPath = strings.TrimSuffix(folderPath, "/")
	if folderPath == "" {
		return nil, fmt.Errorf("folder path is empty")
	}

	baseURL := strings.TrimRight(c.GraphBaseURL, "/")

	var currentURL string
	if c.Destination.Mode == "app_folder" {
		// Strip the destination prefix so the path is relative to approot.
		destPrefix := strings.Trim(strings.TrimPrefix(c.Destination.Path, "/"), "/")
		folderPath = strings.TrimPrefix(folderPath, destPrefix)
		folderPath = strings.Trim(folderPath, "/")
		if folderPath == "" {
				// Root of app folder: no colon segment needed, just list children.
				currentURL = fmt.Sprintf("%s/me/drive/special/approot/children", baseURL)
				} else {
				encoded := encodePathSegment(folderPath)
				currentURL = fmt.Sprintf("%s/me/drive/special/approot:/%s:/children", baseURL, encoded)
				}
			} else {
				encoded := encodePathSegment(folderPath)
				currentURL = fmt.Sprintf("%s/me/drive/root:/%s:/children", baseURL, encoded)
			}

	var items []DriveItem
		for currentURL != "" {
			page, nextLink, err := fetchChildrenPage(ctx, c, currentURL)
			if err != nil {
				return nil, err
			}
			items = append(items, page...)
			currentURL = nextLink
		}

	return items, nil
}

// encodePathSegment encodes a OneDrive path for use in the colon-based Graph URL:
// /me/drive/root:/path/to/folder:/children
func encodePathSegment(path string) string {
	// url.PathEscape encodes spaces as %20 and handles most special chars,
	// but it also encodes / which we don't want (we need folder separators).
	// So we split, escape each segment, and rejoin.
	parts := strings.Split(path, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}

// fetchChildrenPage fetches a single page of OneDrive folder children and
// returns the items plus the @odata.nextLink (empty when no more pages).
// The response body is always closed before returning.
func fetchChildrenPage(ctx context.Context, c *Client, pageURL string) ([]DriveItem, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
	if err != nil {
		return nil, "", fmt.Errorf("list folder: %w", err)
	}

	token, err := c.TokenProvider.AccessToken(ctx, c.Scopes)
	if err != nil {
		return nil, "", fmt.Errorf("list folder token: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("list folder request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, "", fmt.Errorf("list folder: HTTP %d: %s", resp.StatusCode, string(bytes.TrimSpace(body)))
	}

	var page graphChildrenResponse
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		return nil, "", fmt.Errorf("list folder decode: %w", err)
	}
	return page.Value, page.NextLink, nil
}