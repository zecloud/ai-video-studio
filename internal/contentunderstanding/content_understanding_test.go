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
