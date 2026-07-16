package main

import (
	"strings"
	"testing"
	"time"
)

func roleConfig(role string) Config {
	return Config{
		ServiceRole: role, ListenAddr: ":8080", APIKey: "api-key", StorageURL: "https://storage.example.test",
		StagingContainer: "staging", JobContainer: "jobs", GraphBaseURL: defaultGraphBaseURL,
		DTSEndpoint: "https://dts.example.test", DTSTaskHub: "indexing-hub", DTSRenderTaskHub: "render-hub",
		VideoIndexerSubscriptionID: "subscription", VideoIndexerResourceGroup: "group", VideoIndexerAccountName: "account",
		VideoIndexerTimeout: time.Minute, ManagedIdentityClientID: "identity", FFmpegPath: "ffmpeg",
		RenderWorkspaceRoot: "render-work", RenderTimeout: time.Hour,
	}
}

func TestConfigRequiresRoleScopedTaskHubs(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr string
	}{
		{name: "api needs indexing", cfg: func() Config { c := roleConfig("api"); c.DTSTaskHub = ""; return c }(), wantErr: "DTS_TASK_HUB"},
		{name: "api needs render", cfg: func() Config { c := roleConfig("api"); c.DTSRenderTaskHub = ""; return c }(), wantErr: "DTS_RENDER_TASK_HUB"},
		{name: "index worker ignores render", cfg: func() Config { c := roleConfig("worker"); c.DTSRenderTaskHub = ""; return c }()},
		{name: "ffmpeg worker ignores indexing and Video Indexer", cfg: func() Config {
			c := roleConfig("ffmpeg-worker")
			c.DTSTaskHub, c.VideoIndexerSubscriptionID, c.VideoIndexerResourceGroup, c.VideoIndexerAccountName, c.ManagedIdentityClientID = "", "", "", "", ""
			return c
		}()},
		{name: "ffmpeg worker needs render", cfg: func() Config { c := roleConfig("ffmpeg-worker"); c.DTSRenderTaskHub = ""; return c }(), wantErr: "DTS_RENDER_TASK_HUB"},
		{name: "hubs must differ", cfg: func() Config { c := roleConfig("api"); c.DTSRenderTaskHub = c.DTSTaskHub; return c }(), wantErr: "must be different"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			if tc.wantErr == "" && err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
			if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("Validate() error = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

func TestNarrativeRankingTimeoutDefaultsAndCapsAtOneMinute(t *testing.T) {
	if got := (Config{}).Normalize().NarrativeRankingTimeout; got != time.Minute {
		t.Fatalf("default ranking timeout = %v", got)
	}
	if got := (Config{NarrativeRankingTimeout: 2 * time.Minute}).Normalize().NarrativeRankingTimeout; got != time.Minute {
		t.Fatalf("capped ranking timeout = %v", got)
	}
}
