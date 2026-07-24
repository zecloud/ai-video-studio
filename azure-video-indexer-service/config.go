package main

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	narrativeRankingDefaultTimeout = 90 * time.Second
	narrativeRankingMaxTimeout     = 120 * time.Second
)

type Config struct {
	ServiceRole                        string
	ListenAddr                         string
	APIKey                             string
	StorageURL                         string
	StagingContainer                   string
	JobContainer                       string
	SASValidity                        time.Duration
	GraphBaseURL                       string
	VideoIndexerSubscriptionID         string
	VideoIndexerResourceGroup          string
	VideoIndexerAccountName            string
	VideoIndexerAccountID              string
	VideoIndexerLocation               string
	VideoIndexerTimeout                time.Duration
	DTSEndpoint                        string
	DTSTaskHub                         string
	DTSRenderTaskHub                   string
	DTSCancellationGrace               time.Duration
	ManagedIdentityClientID            string
	FFmpegPath                         string
	RenderWorkspaceRoot                string
	RenderTimeout                      time.Duration
	NarrativeRankingTimeout            time.Duration
	NarrativeIntentClassifierTimeout   time.Duration
	NarrativeSegmentPlannerTimeout     time.Duration
	NarrativeSegmentPlannerMaxCatalog  int
	NarrativeSegmentPlannerMaxSegments int
	NarrativeRankingMaxCandidates      int
	NarrativeRankingMaxSources         int
}

func LoadConfig() (Config, error) {
	cfg := Config{
		ServiceRole:                        getEnvDefault("SERVICE_ROLE", "api"),
		ListenAddr:                         getEnvDefault("LISTEN_ADDR", ":8080"),
		APIKey:                             os.Getenv("API_KEY"),
		StorageURL:                         os.Getenv("AZURE_STORAGE_URL"),
		StagingContainer:                   getEnvDefault("AZURE_STORAGE_STAGING_CONTAINER", getEnvDefault("STAGING_CONTAINER", "video-indexer-staging")),
		JobContainer:                       getEnvDefault("AZURE_STORAGE_JOBS_CONTAINER", getEnvDefault("JOB_CONTAINER", "video-indexer-jobs")),
		SASValidity:                        getEnvDuration("SAS_VALIDITY", 2*time.Hour),
		GraphBaseURL:                       getEnvDefault("GRAPH_BASE_URL", "https://graph.microsoft.com/v1.0"),
		VideoIndexerSubscriptionID:         strings.TrimSpace(os.Getenv("AZURE_VIDEO_INDEXER_SUBSCRIPTION_ID")),
		VideoIndexerResourceGroup:          strings.TrimSpace(os.Getenv("AZURE_VIDEO_INDEXER_RESOURCE_GROUP")),
		VideoIndexerAccountName:            strings.TrimSpace(os.Getenv("AZURE_VIDEO_INDEXER_ACCOUNT_NAME")),
		VideoIndexerAccountID:              strings.TrimSpace(os.Getenv("AZURE_VIDEO_INDEXER_ACCOUNT_ID")),
		VideoIndexerLocation:               strings.TrimSpace(os.Getenv("AZURE_VIDEO_INDEXER_LOCATION")),
		VideoIndexerTimeout:                getEnvDuration("AZURE_VIDEO_INDEXER_TIMEOUT", 30*time.Minute),
		DTSEndpoint:                        strings.TrimSpace(os.Getenv("DTS_ENDPOINT")),
		DTSTaskHub:                         strings.TrimSpace(os.Getenv("DTS_TASK_HUB")),
		DTSRenderTaskHub:                   strings.TrimSpace(os.Getenv("DTS_RENDER_TASK_HUB")),
		DTSCancellationGrace:               getEnvDuration("DTS_CANCELLATION_GRACE", 30*time.Second),
		ManagedIdentityClientID:            strings.TrimSpace(os.Getenv("AZURE_CLIENT_ID")),
		FFmpegPath:                         getEnvDefault("FFMPEG_PATH", "ffmpeg"),
		RenderWorkspaceRoot:                getEnvDefault("RENDER_WORKSPACE_ROOT", os.TempDir()),
		RenderTimeout:                      getEnvDuration("RENDER_TIMEOUT", 2*time.Hour),
		NarrativeRankingTimeout:            getEnvDuration("NARRATIVE_RANKING_TIMEOUT", narrativeRankingDefaultTimeout),
		NarrativeIntentClassifierTimeout:   getEnvDuration("NARRATIVE_INTENT_CLASSIFIER_TIMEOUT", 8*time.Second),
		NarrativeSegmentPlannerTimeout:     getEnvDuration("NARRATIVE_SEGMENT_PLANNER_TIMEOUT", 12*time.Second),
		NarrativeSegmentPlannerMaxCatalog:  getEnvInt("NARRATIVE_SEGMENT_PLANNER_MAX_CATALOG", 48),
		NarrativeSegmentPlannerMaxSegments: getEnvInt("NARRATIVE_SEGMENT_PLANNER_MAX_SEGMENTS", 24),
		NarrativeRankingMaxCandidates:      getEnvInt("NARRATIVE_RANKING_MAX_CANDIDATES", 48),
		NarrativeRankingMaxSources:         getEnvInt("NARRATIVE_RANKING_MAX_SOURCES", 8),
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) Normalize() Config {
	c.ListenAddr = strings.TrimSpace(c.ListenAddr)
	c.ServiceRole = strings.ToLower(strings.TrimSpace(c.ServiceRole))
	c.APIKey = strings.TrimSpace(c.APIKey)
	c.StorageURL = strings.TrimSpace(c.StorageURL)
	c.StagingContainer = strings.TrimSpace(c.StagingContainer)
	c.JobContainer = strings.TrimSpace(c.JobContainer)
	c.GraphBaseURL = strings.TrimSpace(c.GraphBaseURL)
	c.VideoIndexerSubscriptionID = strings.TrimSpace(c.VideoIndexerSubscriptionID)
	c.VideoIndexerResourceGroup = strings.TrimSpace(c.VideoIndexerResourceGroup)
	c.VideoIndexerAccountName = strings.TrimSpace(c.VideoIndexerAccountName)
	c.VideoIndexerAccountID = strings.TrimSpace(c.VideoIndexerAccountID)
	c.VideoIndexerLocation = strings.TrimSpace(c.VideoIndexerLocation)
	c.DTSTaskHub = strings.TrimSpace(c.DTSTaskHub)
	c.DTSRenderTaskHub = strings.TrimSpace(c.DTSRenderTaskHub)
	c.ManagedIdentityClientID = strings.TrimSpace(c.ManagedIdentityClientID)
	c.FFmpegPath = strings.TrimSpace(c.FFmpegPath)
	c.RenderWorkspaceRoot = strings.TrimSpace(c.RenderWorkspaceRoot)

	if c.SASValidity <= 0 {
		c.SASValidity = 2 * time.Hour
	}
	if c.VideoIndexerTimeout <= 0 {
		c.VideoIndexerTimeout = 30 * time.Minute
	}
	if c.DTSCancellationGrace <= 0 {
		c.DTSCancellationGrace = 30 * time.Second
	}
	if c.RenderTimeout <= 0 {
		c.RenderTimeout = 2 * time.Hour
	}
	if c.NarrativeRankingTimeout <= 0 {
		c.NarrativeRankingTimeout = narrativeRankingDefaultTimeout
	}
	if c.NarrativeRankingTimeout > narrativeRankingMaxTimeout {
		c.NarrativeRankingTimeout = narrativeRankingMaxTimeout
	}
	if c.NarrativeIntentClassifierTimeout <= 0 {
		c.NarrativeIntentClassifierTimeout = 8 * time.Second
	}
	if c.NarrativeIntentClassifierTimeout > 20*time.Second {
		c.NarrativeIntentClassifierTimeout = 20 * time.Second
	}
	if c.NarrativeSegmentPlannerTimeout <= 0 {
		c.NarrativeSegmentPlannerTimeout = 12 * time.Second
	}
	if c.NarrativeSegmentPlannerTimeout > 20*time.Second {
		c.NarrativeSegmentPlannerTimeout = 20 * time.Second
	}
	if c.NarrativeSegmentPlannerMaxCatalog <= 0 || c.NarrativeSegmentPlannerMaxCatalog > 48 {
		c.NarrativeSegmentPlannerMaxCatalog = 48
	}
	if c.NarrativeSegmentPlannerMaxSegments <= 0 || c.NarrativeSegmentPlannerMaxSegments > 48 {
		c.NarrativeSegmentPlannerMaxSegments = 24
	}
	if c.NarrativeRankingMaxCandidates <= 0 {
		c.NarrativeRankingMaxCandidates = 48
	}
	if c.NarrativeRankingMaxCandidates > 96 {
		c.NarrativeRankingMaxCandidates = 96
	}
	if c.NarrativeRankingMaxSources <= 0 {
		c.NarrativeRankingMaxSources = 8
	}
	if c.NarrativeRankingMaxSources > 16 {
		c.NarrativeRankingMaxSources = 16
	}
	return c
}

func (c Config) Validate() error {
	c = c.Normalize()
	if c.ServiceRole != "api" && c.ServiceRole != "worker" && c.ServiceRole != "ffmpeg-worker" {
		return fmt.Errorf("SERVICE_ROLE must be api, worker, or ffmpeg-worker")
	}
	if c.ListenAddr == "" {
		return fmt.Errorf("LISTEN_ADDR is required")
	}
	if _, err := net.ResolveTCPAddr("tcp", c.ListenAddr); err != nil {
		return fmt.Errorf("LISTEN_ADDR must be a valid TCP listen address")
	}
	if c.ServiceRole == "api" && c.APIKey == "" {
		return fmt.Errorf("API_KEY environment variable is required for the API role")
	}
	if c.StorageURL == "" {
		return fmt.Errorf("AZURE_STORAGE_URL environment variable is required")
	}
	if _, err := url.ParseRequestURI(c.StorageURL); err != nil {
		return fmt.Errorf("AZURE_STORAGE_URL must be an absolute URL: %w", err)
	}
	if u, err := url.Parse(c.StorageURL); err != nil || u.Scheme != "https" || u.Host == "" {
		return fmt.Errorf("AZURE_STORAGE_URL must use https and include a host")
	}
	if c.StagingContainer == "" || c.JobContainer == "" {
		return fmt.Errorf("STAGING_CONTAINER and JOB_CONTAINER are required")
	}
	if c.GraphBaseURL == "" {
		return fmt.Errorf("GRAPH_BASE_URL is required")
	}
	if _, err := url.ParseRequestURI(c.GraphBaseURL); err != nil {
		return fmt.Errorf("GRAPH_BASE_URL must be an absolute URL: %w", err)
	}
	if c.ServiceRole == "ffmpeg-worker" {
		if c.FFmpegPath == "" {
			return fmt.Errorf("FFMPEG_PATH is required for the ffmpeg-worker role")
		}
		if c.RenderWorkspaceRoot == "" {
			return fmt.Errorf("RENDER_WORKSPACE_ROOT is required for the ffmpeg-worker role")
		}
		if c.RenderTimeout <= 0 {
			return fmt.Errorf("RENDER_TIMEOUT must be positive")
		}
	}
	if c.ServiceRole == "worker" {
		if c.VideoIndexerSubscriptionID == "" {
			return fmt.Errorf("AZURE_VIDEO_INDEXER_SUBSCRIPTION_ID is required for the worker role")
		}
		if c.VideoIndexerResourceGroup == "" {
			return fmt.Errorf("AZURE_VIDEO_INDEXER_RESOURCE_GROUP is required for the worker role")
		}
		if c.VideoIndexerAccountName == "" {
			return fmt.Errorf("AZURE_VIDEO_INDEXER_ACCOUNT_NAME is required for the worker role")
		}
		if c.VideoIndexerTimeout <= 0 {
			return fmt.Errorf("AZURE_VIDEO_INDEXER_TIMEOUT must be positive")
		}
		if c.ManagedIdentityClientID == "" {
			return fmt.Errorf("AZURE_CLIENT_ID is required for the worker role")
		}
	}
	if c.DTSEndpoint == "" {
		return fmt.Errorf("DTS_ENDPOINT is required")
	}
	if (c.ServiceRole == "api" || c.ServiceRole == "worker") && c.DTSTaskHub == "" {
		return fmt.Errorf("DTS_TASK_HUB is required for the api and worker roles")
	}
	if (c.ServiceRole == "api" || c.ServiceRole == "ffmpeg-worker") && c.DTSRenderTaskHub == "" {
		return fmt.Errorf("DTS_RENDER_TASK_HUB is required for the api and ffmpeg-worker roles")
	}
	if c.DTSTaskHub != "" && c.DTSRenderTaskHub != "" && c.DTSTaskHub == c.DTSRenderTaskHub {
		return fmt.Errorf("DTS_TASK_HUB and DTS_RENDER_TASK_HUB must be different")
	}
	if u, err := url.ParseRequestURI(c.DTSEndpoint); err != nil || u.Scheme != "https" || u.Host == "" {
		return fmt.Errorf("DTS_ENDPOINT must be an absolute https URL")
	}
	return nil
}

func getEnvDefault(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

func getEnvDuration(key string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}
