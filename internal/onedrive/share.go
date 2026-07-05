package onedrive

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// CreateShareableLink creates an anonymous ("view") share link for a drive item
// so Azure Content Understanding can access it. Returns the publicly accessible URL.
func (c *Client) CreateShareableLink(ctx context.Context, itemID string) (string, error) {
	if c == nil {
		return "", fmt.Errorf("onedrive client is nil")
	}
	itemID = strings.TrimSpace(itemID)
	if itemID == "" {
		return "", fmt.Errorf("item id is required")
	}

	url := fmt.Sprintf("%s/me/drive/items/%s/createLink", strings.TrimRight(c.GraphBaseURL, "/"), itemID)

	bodyPayload := map[string]string{"type": "view", "scope": "anonymous"}
	body, err := json.Marshal(bodyPayload)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(string(body)))
	if err != nil {
		return "", fmt.Errorf("create share link: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	token, err := c.TokenProvider.AccessToken(ctx, c.Scopes)
	if err != nil {
		return "", fmt.Errorf("create share link token: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("create share link request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("create share link: HTTP %d", resp.StatusCode)
	}

	var result struct {
			Link struct {
				WebURL     string `json:"webUrl"`
				Type       string `json:"type"`
				Scope      string `json:"scope"`
		} `json:"link"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("create share link decode: %w", err)
	}

		shareURL := strings.TrimSpace(result.Link.WebURL)
			if shareURL == "" {
				return "", fmt.Errorf("create share link: no URL returned")
	}

			// Convert the sharing URL to a direct download URL by appending "?download=1"
			// This gives us a URL Azure CU can stream from.
			if !strings.Contains(shareURL, "?") {
				shareURL += "?download=1"
			}

			return shareURL, nil
}