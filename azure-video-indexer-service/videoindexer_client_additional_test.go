package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type maxRandSource struct{}

func (maxRandSource) Int63() int64 { return 7_500_000_000 }
func (maxRandSource) Seed(int64)   {}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestVideoIndexerClientBackoffCapsAndRetryableFailures(t *testing.T) {
	t.Run("backoff caps at thirty seconds", func(t *testing.T) {
		client := &VideoIndexerClient{rng: rand.New(maxRandSource{})}
		if got := client.nextDelay(nil, 10); got != 30*time.Second {
			t.Fatalf("unexpected capped delay: %s", got)
		}
	})

	t.Run("access token failure is retryable", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case strings.Contains(r.URL.Path, "/generateAccessToken"):
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = io.WriteString(w, `{"code":"service_unavailable","message":"try again","retryable":true}`)
			default:
				t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
			}
		}))
		defer server.Close()

		client := newTestVideoIndexerClient(t, server)
		_, err := client.AccountAccessToken(context.Background())
		if err == nil {
			t.Fatal("expected access token error")
		}
		var svcErr *ServiceError
		if !errors.As(err, &svcErr) || !svcErr.Retryable || svcErr.Status != http.StatusServiceUnavailable {
			t.Fatalf("unexpected error: %#v", err)
		}
	})

	t.Run("upload failure is retryable", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case strings.Contains(r.URL.Path, "/generateAccessToken"):
				w.WriteHeader(http.StatusOK)
				_, _ = io.WriteString(w, `{"accessToken":"vi-account-token"}`)
			case strings.HasSuffix(r.URL.Path, "/Videos") && r.Method == http.MethodPost:
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = io.WriteString(w, `{"code":"too_many_requests","message":"slow down","retryable":true}`)
			default:
				t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
			}
		}))
		defer server.Close()

		client := newTestVideoIndexerClient(t, server)
		_, err := client.UploadVideoURL(context.Background(), "https://example.com/input.mp4", "clip.mp4", "job-123")
		if err == nil {
			t.Fatal("expected upload error")
		}
		var svcErr *ServiceError
		if !errors.As(err, &svcErr) || !svcErr.Retryable || svcErr.Status != http.StatusTooManyRequests {
			t.Fatalf("unexpected error: %#v", err)
		}
	})
}

func TestVideoIndexerClient_RoundTripperErrorIsRedacted(t *testing.T) {
	const token = "access-token-secret"
	client := &VideoIndexerClient{
		cfg: VideoIndexerConfig{APIBaseURL: "https://api.videoindexer.ai"},
		http: &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			return nil, fmt.Errorf("transport failed for %s", req.URL.String())
		})},
	}

	_, _, err := client.getVideoIndex(context.Background(), ResolvedVideoIndexerAccount{
		Location:  "westus2",
		AccountID: "account-456",
	}, "video-123", token)
	if err == nil {
		t.Fatal("expected transport error")
	}
	var svcErr *ServiceError
	if !errors.As(err, &svcErr) {
		t.Fatalf("expected service error, got %T", err)
	}
	for _, got := range []string{err.Error(), svcErr.Message, svcErr.APIError().Message, serviceErrorMessage(err), redactURLsInText(err.Error())} {
		if strings.Contains(got, token) {
			t.Fatalf("token leaked in redacted error: %q", got)
		}
	}
	if svcErr.Cause != nil {
		t.Fatalf("expected sanitized service error to drop raw cause, got %#v", svcErr.Cause)
	}
	apiErr, status := toAPIError(err)
	if status != http.StatusBadGateway {
		t.Fatalf("unexpected status: %d", status)
	}
	if strings.Contains(apiErr.Message, token) {
		t.Fatalf("token leaked in API error: %#v", apiErr)
	}
	persisted := APIErrorResponse{Code: serviceErrorCode(err), Message: serviceErrorMessage(err), Retryable: serviceRetryable(err)}
	if strings.Contains(persisted.Message, token) {
		t.Fatalf("token leaked in persisted error: %#v", persisted)
	}
}

func TestDecodeHTTPErrorRedactsTokenBodies(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusBadGateway,
		Status:     "502 Bad Gateway",
		Body:       io.NopCloser(strings.NewReader(`{"code":"failed","message":"request https://contoso.services.ai.azure.com/api/projects/smart-edit?accessToken=access-token-secret"}`)),
	}

	err := decodeHTTPError(resp, "Video Indexer index")
	if err == nil {
		t.Fatal("expected decode error")
	}
	var svcErr *ServiceError
	if !errors.As(err, &svcErr) {
		t.Fatalf("expected service error, got %T", err)
	}
	for _, got := range []string{err.Error(), svcErr.Message, svcErr.APIError().Message} {
		if strings.Contains(got, "access-token-secret") {
			t.Fatalf("token leaked in decoded error: %q", got)
		}
	}
}
