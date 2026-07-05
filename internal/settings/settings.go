package settings

import (
	"context"

	cu "github.com/zecloud/ai-video-studio/internal/contentunderstanding"
	"github.com/zecloud/ai-video-studio/internal/onedrive"
)

type AppSettings struct {
	TenantID                  string                       `json:"tenantId,omitempty"`
	ClientID                  string                       `json:"clientId,omitempty"`
	OneDriveFolder            string                       `json:"oneDriveFolder"`
	GraphAuth                 onedrive.GraphAuthConfig     `json:"graphAuth"`
	OneDriveDestination       onedrive.OneDriveDestination `json:"oneDriveDestination"`
	AzureEndpoint             string                       `json:"azureEndpoint,omitempty"`
	AzureAnalyzerID           string                       `json:"azureAnalyzerId,omitempty"`
	AzureContentUnderstanding cu.Config                    `json:"azureContentUnderstanding"`
	FFmpegPath                string                       `json:"ffmpegPath,omitempty"`
	FFprobePath               string                       `json:"ffprobePath,omitempty"`
	ChunkSizeBytes            int64                        `json:"chunkSizeBytes"`
	MaxConcurrentImports      int                          `json:"maxConcurrentImports"`
	MediaServiceEndpoint      string                       `json:"mediaServiceEndpoint,omitempty"`
	MediaServiceAPIKey        string                       `json:"-"` // never serialized
}

type Service interface {
	Get(context.Context) (AppSettings, error)
	Save(context.Context, AppSettings) (AppSettings, error)
}
