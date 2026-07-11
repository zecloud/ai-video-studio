package mediaservice

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testConfig(endpoint string) Config {
	return Config{Endpoint: endpoint, APIKey: "test-api-key"}
}

func TestCopyToBlob_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/copy" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method: %s", r.Method)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-api-key" {
			t.Errorf("unexpected Authorization header: %q", got)
		}

		var req copyRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("failed to decode request body: %v", err)
		}
		if req.OneDriveItemID != "item-123" {
			t.Errorf("unexpected oneDriveItemID: %q", req.OneDriveItemID)
		}
		if req.OneDriveToken != "token-abc" {
			t.Errorf("unexpected oneDriveToken: %q", req.OneDriveToken)
		}
		if req.BlobName != "clip.mp4" {
			t.Errorf("unexpected blobName: %q", req.BlobName)
		}
		if req.BlobContainer != "staging" {
			t.Errorf("unexpected blobContainer: %q", req.BlobContainer)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(CopyResult{
			BlobURL: "https://storage.blob.core.windows.net/staging/clip.mp4",
			SASURL:  "https://storage.blob.core.windows.net/staging/clip.mp4?sv=sas",
		})
	}))
	defer server.Close()

	client := NewClient(testConfig(server.URL), server.Client())
	result, err := client.CopyToBlob(context.Background(), "item-123", "token-abc", "clip.mp4", "staging")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.BlobURL != "https://storage.blob.core.windows.net/staging/clip.mp4" {
		t.Errorf("unexpected BlobURL: %q", result.BlobURL)
	}
	if result.SASURL != "https://storage.blob.core.windows.net/staging/clip.mp4?sv=sas" {
		t.Errorf("unexpected SASURL: %q", result.SASURL)
	}
}

func TestAnalyze_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/analyze" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Fatalf("unexpected method: %s", r.Method)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Fatalf("unexpected Content-Type header: %q", got)
		}
		if got := r.Header.Get("Accept"); got != "application/json" {
			t.Fatalf("unexpected Accept header: %q", got)
		}

		var req AnalyzeRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}
		wantReq := AnalyzeRequest{
			OneDriveItemID: "item-123",
			OneDriveToken:  "token-abc",
			AssetID:        "asset-1",
			AssetName:      "clip.mp4",
		}
		if req != wantReq {
			t.Fatalf("unexpected request:\nwant: %#v\ngot:  %#v", wantReq, req)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(AnalyzeResult{
			JobID:  "job-1",
			Status: "Succeeded",
			Scenes: []AnalyzeScene{{
				ID:        "scene-1",
				StartMS:   0,
				EndMS:     1000,
				Labels:    []string{"outdoor"},
				Summary:   "opening",
				Highlight: true,
			}},
			Transcript: []AnalyzeTranscript{{
				StartMS: 0,
				EndMS:   1000,
				Text:    "hello",
				Speaker: "speaker-1",
				Score:   0.9,
			}},
			Highlights: []AnalyzeHighlight{{
				ID:      "highlight-1",
				StartMS: 0,
				EndMS:   1000,
				Reason:  "action",
				Score:   0.8,
			}},
			Suggestions: []AnalyzeSuggestion{{
				ID:          "suggestion-1",
				Title:       "Trim intro",
				Description: "Remove the first second.",
				SceneIDs:    []string{"scene-1"},
			}},
		})
	}))
	defer server.Close()

	client := NewClient(testConfig(server.URL), server.Client())
	result, err := client.Analyze(context.Background(), AnalyzeRequest{
		OneDriveItemID: "item-123",
		OneDriveToken:  "token-abc",
		AssetID:        "asset-1",
		AssetName:      "clip.mp4",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.JobID != "job-1" || result.Status != "Succeeded" {
		t.Fatalf("unexpected metadata: %+v", result)
	}
	if len(result.Scenes) != 1 || result.Scenes[0].ID != "scene-1" || !result.Scenes[0].Highlight {
		t.Fatalf("unexpected scenes: %+v", result.Scenes)
	}
	if len(result.Transcript) != 1 || result.Transcript[0].Text != "hello" {
		t.Fatalf("unexpected transcript: %+v", result.Transcript)
	}
	if len(result.Highlights) != 1 || result.Highlights[0].Reason != "action" {
		t.Fatalf("unexpected highlights: %+v", result.Highlights)
	}
	if len(result.Suggestions) != 1 || result.Suggestions[0].Title != "Trim intro" {
		t.Fatalf("unexpected suggestions: %+v", result.Suggestions)
	}
}

func TestCopyToBlob_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom"))
	}))
	defer server.Close()

	client := NewClient(testConfig(server.URL), server.Client())
	_, err := client.CopyToBlob(context.Background(), "item-123", "token-abc", "clip.mp4", "staging")
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if !errors.Is(err, ErrUnexpectedStatus) {
		t.Errorf("expected ErrUnexpectedStatus, got: %v", err)
	}
}

func TestCopyToBlob_ClientError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid api key"}`))
	}))
	defer server.Close()

	client := NewClient(testConfig(server.URL), server.Client())
	_, err := client.CopyToBlob(context.Background(), "item-123", "token-abc", "clip.mp4", "staging")
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if !errors.Is(err, ErrUnexpectedStatus) {
		t.Errorf("expected ErrUnexpectedStatus, got: %v", err)
	}
	if !strings.Contains(err.Error(), "invalid api key") {
		t.Errorf("expected error to include server message, got: %v", err)
	}
}

func TestCopyToBlob_MissingFields(t *testing.T) {
	client := NewClient(testConfig("https://example.com"), http.DefaultClient)

	if _, err := client.CopyToBlob(context.Background(), "", "token", "blob", "container"); err == nil {
		t.Error("expected error for missing oneDriveItemID")
	}
	if _, err := client.CopyToBlob(context.Background(), "item", "", "blob", "container"); err == nil {
		t.Error("expected error for missing oneDriveToken")
	}
	if _, err := client.CopyToBlob(context.Background(), "item", "token", "", "container"); err == nil {
		t.Error("expected error for missing blobName")
	}
}

func TestDeleteBlob_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/blobs/clip.mp4" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.URL.Query().Get("container") != "staging" {
			t.Errorf("unexpected container param: %s", r.URL.Query().Get("container"))
		}
		if r.Method != http.MethodDelete {
			t.Errorf("unexpected method: %s", r.Method)
		}
		if r.Body != http.NoBody {
			t.Errorf("expected no body for DELETE request")
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-api-key" {
			t.Errorf("unexpected Authorization header: %q", got)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(deleteResponse{Status: "deleted"})
	}))
	defer server.Close()

	client := NewClient(testConfig(server.URL), server.Client())
	if err := client.DeleteBlob(context.Background(), "clip.mp4", "staging"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
func TestDeleteBlob_Error(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	client := NewClient(testConfig(server.URL), server.Client())
	err := client.DeleteBlob(context.Background(), "clip.mp4", "staging")
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if !errors.Is(err, ErrUnexpectedStatus) {
		t.Errorf("expected ErrUnexpectedStatus in error chain, got: %v", err)
	}
}
func TestDeleteBlob_MissingBlobName(t *testing.T) {
	client := NewClient(testConfig("https://example.com"), http.DefaultClient)
	if err := client.DeleteBlob(context.Background(), "", "staging"); err == nil {
		t.Error("expected error for missing blobName")
	}
}

func TestHealth_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Method != http.MethodGet {
			t.Errorf("unexpected method: %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(healthResponse{Status: "ok"})
	}))
	defer server.Close()

	client := NewClient(testConfig(server.URL), server.Client())
	if err := client.Health(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestHealth_Error(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	client := NewClient(testConfig(server.URL), server.Client())
	if err := client.Health(context.Background()); err == nil {
		t.Fatal("expected an error, got nil")
	}
}

func TestContextCancellation(t *testing.T) {
	blocker := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-blocker
	}))
	defer server.Close()
	defer close(blocker)

	client := NewClient(testConfig(server.URL), server.Client())

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := client.CopyToBlob(ctx, "item", "token", "blob", "container")
	if err == nil {
		t.Fatal("expected an error due to context cancellation, got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected context.DeadlineExceeded, got: %v", err)
	}
}

func TestConfig_Validate_MissingEndpoint(t *testing.T) {
	client := NewClient(Config{APIKey: "key"}, http.DefaultClient)
	if _, err := client.CopyToBlob(context.Background(), "item", "token", "blob", ""); err == nil {
		t.Error("expected error for missing endpoint")
	} else if !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("expected ErrInvalidConfig, got: %v", err)
	}
}

func TestConfig_Validate_MissingAPIKey(t *testing.T) {
	client := NewClient(Config{Endpoint: "https://example.com"}, http.DefaultClient)
	if _, err := client.CopyToBlob(context.Background(), "item", "token", "blob", ""); err == nil {
		t.Error("expected error for missing API key")
	} else if !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("expected ErrInvalidConfig, got: %v", err)
	}
}

func TestConfig_Validate_InvalidEndpointURL(t *testing.T) {
	client := NewClient(Config{Endpoint: "not a url", APIKey: "key"}, http.DefaultClient)
	if _, err := client.CopyToBlob(context.Background(), "item", "token", "blob", ""); err == nil {
		t.Error("expected error for invalid endpoint URL")
	} else if !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("expected ErrInvalidConfig, got: %v", err)
	}
}

func TestNewClient_DefaultsHTTPClient(t *testing.T) {
	client := NewClient(testConfig("https://example.com"), nil)
	if client.http == nil {
		t.Fatal("expected default http client to be set")
	}
}
