package backend

import (
	"context"
	"testing"

	cu "github.com/zecloud/ai-video-studio/internal/contentunderstanding"
	"github.com/zecloud/ai-video-studio/internal/mediaservice"
)

var (
	_ cu.Service    = (*ContentUnderstandingService)(nil)
	_ RenderBackend = (*mediaservice.Client)(nil)
)

func TestContentUnderstandingAndMediaServiceConstructorsRemainUsable(t *testing.T) {
	cuService := NewContentUnderstandingService()
	if cuService == nil {
		t.Fatal("expected content understanding service")
	}
	if _, err := cuService.Status(context.Background()); err != nil {
		t.Fatalf("Status returned error: %v", err)
	}

	fromSettings := NewContentUnderstandingServiceFromSettings(nil)
	if fromSettings == nil {
		t.Fatal("expected content understanding service from settings")
	}
	if _, err := fromSettings.Status(context.Background()); err != nil {
		t.Fatalf("Status from settings returned error: %v", err)
	}

	client := NewMediaServiceClient(nil)
	if client == nil {
		t.Fatal("expected media service client")
	}
}
