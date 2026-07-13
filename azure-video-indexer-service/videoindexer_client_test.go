package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
)

type staticTokenCredential struct {
	token string
}

func (c staticTokenCredential) GetToken(context.Context, policy.TokenRequestOptions) (azcore.AccessToken, error) {
	return azcore.AccessToken{Token: c.token, ExpiresOn: time.Now().Add(time.Hour)}, nil
}

func newTestVideoIndexerClient(t *testing.T, server *httptest.Server) *VideoIndexerClient {
	t.Helper()
	client, err := NewVideoIndexerClient(VideoIndexerConfig{
		SubscriptionID: "sub-123",
		ResourceGroup:  "rg-123",
		AccountName:    "account-name",
		AccountID:      "account-456",
		Location:       "westus2",
		ARMBaseURL:     server.URL,
		APIBaseURL:     server.URL,
		PollTimeout:    time.Minute,
	}, staticTokenCredential{token: "arm-token"}, server.Client())
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	return client
}

func TestVideoIndexerClient_AccessTokenRequest(t *testing.T) {
	var mu sync.Mutex
	var tokenCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/generateAccessToken") {
			mu.Lock()
			tokenCalls++
			mu.Unlock()
			if r.Method != http.MethodPost {
				t.Fatalf("unexpected method: %s", r.Method)
			}
			if got := r.Header.Get("Authorization"); got != "Bearer arm-token" {
				t.Fatalf("unexpected authorization: %q", got)
			}
			if got := r.URL.Query().Get("api-version"); got != defaultAPIVersion {
				t.Fatalf("unexpected api version: %q", got)
			}
			var body videoIndexerAccessTokenRequest
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode body: %v", err)
			}
			if body.PermissionType != "Contributor" || body.Scope != "Account" {
				t.Fatalf("unexpected body: %#v", body)
			}
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{"accessToken":"vi-account-token"}`)
			return
		}
		t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
	}))
	defer server.Close()

	client := newTestVideoIndexerClient(t, server)
	token, err := client.AccountAccessToken(context.Background())
	if err != nil {
		t.Fatalf("account access token: %v", err)
	}
	if token != "vi-account-token" {
		t.Fatalf("unexpected token: %q", token)
	}
	mu.Lock()
	defer mu.Unlock()
	if tokenCalls != 1 {
		t.Fatalf("unexpected token request count: %d", tokenCalls)
	}
}

func TestVideoIndexerClient_UploadURLQueryEncoding(t *testing.T) {
	var gotUpload url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/generateAccessToken"):
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{"accessToken":"vi-account-token"}`)
		case strings.HasSuffix(r.URL.Path, "/Videos") && r.Method == http.MethodPost:
			gotUpload = r.URL.Query()
			if got := gotUpload.Get("language"); got != "" {
				t.Fatalf("unexpected language: %q", got)
			}
			if got := gotUpload.Get("name"); got != "clip my video-.mp4" {
				t.Fatalf("unexpected safe name: %q", got)
			}
			if got := gotUpload.Get("externalId"); got != "job-123" {
				t.Fatalf("unexpected externalId: %q", got)
			}
			if got := gotUpload.Get("preventDuplicates"); got != "true" {
				t.Fatalf("unexpected preventDuplicates: %q", got)
			}
			if got := gotUpload.Get("videoUrl"); got != "https://example.com/media/input.mp4?sig=abc" {
				t.Fatalf("unexpected videoUrl: %q", got)
			}
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{"id":"video-123"}`)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	client := newTestVideoIndexerClient(t, server)
	videoID, err := client.UploadVideoURL(context.Background(), "https://example.com/media/input.mp4?sig=abc", "../clip my video?.mp4", "job-123")
	if err != nil {
		t.Fatalf("upload video: %v", err)
	}
	if videoID != "video-123" {
		t.Fatalf("unexpected video id: %q", videoID)
	}
}

func TestVideoIndexerClient_PollVideoIndexProcessed(t *testing.T) {
	var getCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/generateAccessToken"):
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{"accessToken":"vi-account-token"}`)
		case strings.Contains(r.URL.Path, "/Index"):
			getCalls++
			switch getCalls {
			case 1:
				w.WriteHeader(http.StatusOK)
				_, _ = io.WriteString(w, `{"id":"video-123","state":"Processing"}`)
			default:
				w.WriteHeader(http.StatusOK)
				_, _ = io.WriteString(w, `{"id":"video-123","state":"Processed","insights":{"summary":"done"}}`)
			}
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	client := newTestVideoIndexerClient(t, server)
	client.wait = func(context.Context, time.Duration) error { return nil }
	index, err := client.PollVideoIndex(context.Background(), "video-123", time.Minute)
	if err != nil {
		t.Fatalf("poll video index: %v", err)
	}
	if index.VideoID() != "video-123" || index.State() != "Processed" {
		t.Fatalf("unexpected index result: video=%q state=%q", index.VideoID(), index.State())
	}
	if !strings.Contains(string(index.RawJSON()), `"summary":"done"`) {
		t.Fatalf("raw json was not returned: %s", string(index.RawJSON()))
	}
	if getCalls != 2 {
		t.Fatalf("unexpected get call count: %d", getCalls)
	}
}

func TestVideoIndexerClient_PollVideoIndexRetryAfter(t *testing.T) {
	var delays []time.Duration
	var getCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/generateAccessToken"):
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{"accessToken":"vi-account-token"}`)
		case strings.Contains(r.URL.Path, "/Index"):
			getCalls++
			if getCalls == 1 {
				w.Header().Set("Retry-After", "7")
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = io.WriteString(w, `{"error":{"code":"TooManyRequests","message":"slow down"}}`)
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{"id":"video-123","state":"Processed"}`)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	client := newTestVideoIndexerClient(t, server)
	client.wait = func(_ context.Context, d time.Duration) error {
		delays = append(delays, d)
		return nil
	}
	index, err := client.PollVideoIndex(context.Background(), "video-123", time.Minute)
	if err != nil {
		t.Fatalf("poll video index: %v", err)
	}
	if index.State() != "Processed" {
		t.Fatalf("unexpected state: %s", index.State())
	}
	if getCalls != 2 {
		t.Fatalf("unexpected get call count: %d", getCalls)
	}
	if len(delays) != 1 {
		t.Fatalf("unexpected wait call count: %d", len(delays))
	}
	if delays[0] != 7*time.Second {
		t.Fatalf("unexpected retry-after delay: %s", delays[0])
	}
}

func TestVideoIndexerClient_PollVideoIndexFailed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/generateAccessToken"):
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{"accessToken":"vi-account-token"}`)
		case strings.Contains(r.URL.Path, "/Index"):
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{"id":"video-123","state":"Failed"}`)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	client := newTestVideoIndexerClient(t, server)
	client.wait = func(context.Context, time.Duration) error { return nil }
	_, err := client.PollVideoIndex(context.Background(), "video-123", time.Minute)
	if err == nil {
		t.Fatal("expected error")
	}
	var svcErr *ServiceError
	if !errors.As(err, &svcErr) || svcErr.Code != "video_index_failed" || svcErr.Status != http.StatusUnprocessableEntity {
		t.Fatalf("unexpected error: %#v", err)
	}
}

func TestVideoIndexerClient_PollVideoIndexCancellation(t *testing.T) {
	waitStarted := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/generateAccessToken"):
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{"accessToken":"vi-account-token"}`)
		case strings.Contains(r.URL.Path, "/Index"):
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{"id":"video-123","state":"Processing"}`)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	client := newTestVideoIndexerClient(t, server)
	client.wait = func(ctx context.Context, d time.Duration) error {
		select {
		case waitStarted <- struct{}{}:
		default:
		}
		<-ctx.Done()
		return ctx.Err()
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-waitStarted
		cancel()
	}()

	_, err := client.PollVideoIndex(ctx, "video-123", time.Minute)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
}
