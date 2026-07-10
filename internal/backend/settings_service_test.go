package backend

import (
	"context"
	"strings"
	"testing"

	"github.com/zecloud/ai-video-studio/internal/settings"
)

func TestSettingsServiceVideoIndexerStatus(t *testing.T) {
	svc := NewSettingsService(testSettingsStore{settings: settings.AppSettings{
		VideoIndexerServiceEndpoint: "https://video.example",
		VideoIndexerServiceAPIKey:   "secret",
	}})

	status, err := svc.GetVideoIndexerServiceStatus(context.Background())
	if err != nil {
		t.Fatalf("GetVideoIndexerServiceStatus: %v", err)
	}
	if !status.Configured || !status.HasAPIKey || status.Endpoint != "https://video.example" {
		t.Fatalf("unexpected status: %#v", status)
	}
}

func TestSettingsServiceSetVideoIndexerServiceEndpointRejectsInvalidURL(t *testing.T) {
	svc := NewSettingsService(testSettingsStore{settings: settings.AppSettings{
		VideoIndexerServiceAPIKey: "secret",
	}})

	err := svc.SetVideoIndexerServiceEndpoint(context.Background(), "not-a-url", "")
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "https") {
		t.Fatalf("expected invalid endpoint error, got %v", err)
	}
}

func TestSettingsServiceSetVideoIndexerServiceEndpointRejectsUnsafeEndpoints(t *testing.T) {
	svc := NewSettingsService(testSettingsStore{settings: settings.AppSettings{
		VideoIndexerServiceAPIKey: "secret",
	}})

	for _, endpoint := range []string{
		"http://example.com",
		"https://user:pass@example.com",
		"https://example.com/#frag",
	} {
		if err := svc.SetVideoIndexerServiceEndpoint(context.Background(), endpoint, "secret"); err == nil {
			t.Fatalf("expected endpoint %q to be rejected", endpoint)
		}
	}
}

func TestSettingsServiceSetVideoIndexerServiceEndpointAcceptsHTTPS(t *testing.T) {
	store := &mutableSettingsStore{settings: settings.AppSettings{}}
	svc := NewSettingsService(store)

	if err := svc.SetVideoIndexerServiceEndpoint(context.Background(), "https://video.example/", "secret"); err != nil {
		t.Fatalf("SetVideoIndexerServiceEndpoint: %v", err)
	}
	if got := store.settings.VideoIndexerServiceEndpoint; got != "https://video.example" {
		t.Fatalf("stored endpoint = %q", got)
	}
}
