package contentunderstanding

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSubmitAnalysisPostsSourceURLAndReturnsOperation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %q, want POST", r.Method)
		}
		if r.URL.Path != "/contentunderstanding/analyzers/prebuilt-videoSearch:analyze" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if r.URL.Query().Get("api-version") != DefaultAPIVersion {
			t.Fatalf("api-version = %q", r.URL.Query().Get("api-version"))
		}
		if r.Header.Get("Ocp-Apim-Subscription-Key") != "secret" {
			t.Fatalf("missing API key header")
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body["url"] != "https://storage.example/clip.mp4?sas=redacted" {
			t.Fatalf("url body = %q", body["url"])
		}
		w.Header().Set("Operation-Location", serverURL(r)+"/contentunderstanding/analyzerResults/job-1?api-version=2025-11-01")
		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, `{"id":"job-1","status":"Running"}`)
	}))
	defer server.Close()

	client := NewClient(Config{Endpoint: server.URL, APIKey: "secret"}, server.Client())
	got, err := client.SubmitAnalysis(context.Background(), VideoAsset{
		ID:        "asset-1",
		Name:      "clip.mp4",
		SourceURL: "https://storage.example/clip.mp4?sas=redacted",
	})
	if err != nil {
		t.Fatalf("SubmitAnalysis returned error: %v", err)
	}
	if got.JobID != "job-1" || got.Status != "Running" || !strings.Contains(got.OperationLocation, "/analyzerResults/job-1") {
		t.Fatalf("unexpected submit response: %+v", got)
	}
}

func TestConfigNormalizedDefaults(t *testing.T) {
	got := Config{
		Endpoint:   " https://example.com/ ",
		APIKey:     " secret ",
		AnalyzerID: " ",
		APIVersion: " ",
		SourceMode: " ",
	}.normalized()

	if got.Endpoint != "https://example.com" {
		t.Fatalf("Endpoint = %q", got.Endpoint)
	}
	if got.APIKey != "secret" {
		t.Fatalf("APIKey = %q", got.APIKey)
	}
	if got.AnalyzerID != PrebuiltVideoAnalyzerID {
		t.Fatalf("AnalyzerID = %q", got.AnalyzerID)
	}
	if got.APIVersion != DefaultAPIVersion {
		t.Fatalf("APIVersion = %q", got.APIVersion)
	}
	if got.SourceMode != "https_url" {
		t.Fatalf("SourceMode = %q", got.SourceMode)
	}
}

func TestStatusReportsNormalizedConfiguration(t *testing.T) {
	client := NewClient(Config{
		Endpoint:   " https://example.com/ ",
		APIKey:     " secret ",
		AnalyzerID: " custom-analyzer ",
		APIVersion: " 2025-11-01 ",
		SourceMode: " blob_sas ",
	}, nil)

	status, err := client.Status(context.Background())
	if err != nil {
		t.Fatalf("Status returned error: %v", err)
	}
	if !status.Configured {
		t.Fatal("expected configured status")
	}
	if status.Endpoint != "https://example.com" {
		t.Fatalf("Endpoint = %q", status.Endpoint)
	}
	if status.AnalyzerID != "custom-analyzer" {
		t.Fatalf("AnalyzerID = %q", status.AnalyzerID)
	}
	if status.APIVersion != "2025-11-01" {
		t.Fatalf("APIVersion = %q", status.APIVersion)
	}
	if status.SourceMode != "blob_sas" {
		t.Fatalf("SourceMode = %q", status.SourceMode)
	}
	if status.Message != "Azure Content Understanding is configured." {
		t.Fatalf("Message = %q", status.Message)
	}
}

func TestNormalizeOperationMapsMarkdownToSuggestion(t *testing.T) {
	got := normalizeOperation(analysisOperationResponse{
		ID:     "job-1",
		Status: "Succeeded",
		Result: analysisPayload{
			Contents: []analysisContent{
				{Markdown: "Scene summary and transcript highlights."},
				{Markdown: "   "},
			},
		},
	})

	if got.JobID != "job-1" || got.Status != "Succeeded" {
		t.Fatalf("unexpected metadata: %+v", got)
	}
	if len(got.Suggestions) != 1 {
		t.Fatalf("Suggestions = %+v", got.Suggestions)
	}
	suggestion := got.Suggestions[0]
	if suggestion.ID != "summary-1" || suggestion.Title != "Content Understanding summary" || suggestion.Description != "Scene summary and transcript highlights." {
		t.Fatalf("unexpected suggestion: %+v", suggestion)
	}
}

func TestSubmitAnalysisRequiresAccessibleSourceURL(t *testing.T) {
	client := NewClient(Config{Endpoint: "https://example.cognitiveservices.azure.com", APIKey: "secret"}, nil)
	_, err := client.SubmitAnalysis(context.Background(), VideoAsset{ID: "asset-1"})
	if !errors.Is(err, ErrInvalidAnalysisRequest) {
		t.Fatalf("expected ErrInvalidAnalysisRequest, got %v", err)
	}
}

func TestGetResultNormalizesMarkdownSummary(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("method = %q, want GET", r.Method)
		}
		if r.Header.Get("Ocp-Apim-Subscription-Key") != "secret" {
			t.Fatalf("missing API key header")
		}
		_, _ = io.WriteString(w, `{
			"id": "job-1",
			"status": "Succeeded",
			"result": {
				"analyzerId": "prebuilt-videoSearch",
				"contents": [
					{"markdown": "Scene summary and transcript highlights."}
				]
			}
		}`)
	}))
	defer server.Close()

	client := NewClient(Config{Endpoint: server.URL, APIKey: "secret"}, server.Client())
	result, err := client.GetResult(context.Background(), "job-1")
	if err != nil {
		t.Fatalf("GetResult returned error: %v", err)
	}
	if result.JobID != "job-1" || result.Status != "Succeeded" {
		t.Fatalf("unexpected result metadata: %+v", result)
	}
	if len(result.Suggestions) != 1 || result.Suggestions[0].Description != "Scene summary and transcript highlights." {
		t.Fatalf("unexpected suggestions: %+v", result.Suggestions)
	}
}

func serverURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}
