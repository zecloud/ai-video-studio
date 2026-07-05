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
		if r.URL.Path != "/api/v1/delete" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method: %s", r.Method)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-api-key" {
			t.Errorf("unexpected Authorization header: %q", got)
		}

		var req deleteRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("failed to decode request body: %v", err)
		}
		if req.BlobName != "clip.mp4" {
			t.Errorf("unexpected blobName: %q", req.BlobName)
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
		t.Errorf("expected ErrUnexpectedStatus, got: %v", err)
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
