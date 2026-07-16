package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/zecloud/ai-video-studio/internal/videoindexerstudio"
)

type narrativeIntentClassifierFunc func(context.Context, videoindexerstudio.NarrativeIntentClassificationRequest) (videoindexerstudio.NarrativeIntentClassificationResponse, error)

func (f narrativeIntentClassifierFunc) Classify(ctx context.Context, req videoindexerstudio.NarrativeIntentClassificationRequest) (videoindexerstudio.NarrativeIntentClassificationResponse, error) {
	return f(ctx, req)
}

type narrativeIntentClassifierRunnerFunc func(context.Context, string) (videoindexerstudio.NarrativeIntentClassificationResponse, error)

func (f narrativeIntentClassifierRunnerFunc) RunClassification(ctx context.Context, intent string) (videoindexerstudio.NarrativeIntentClassificationResponse, error) {
	return f(ctx, intent)
}

func narrativeIntentClassificationRequest(t *testing.T, server *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/narrative-intent-classifications", bytes.NewBufferString(body))
	req.Header.Set("X-API-Key", "test-key")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, req)
	return response
}

func TestNarrativeIntentClassifierEnforcesBoundsAndClosedResponse(t *testing.T) {
	classifier := narrativeIntentClassifier{
		timeout: time.Second,
		runner: narrativeIntentClassifierRunnerFunc(func(_ context.Context, intent string) (videoindexerstudio.NarrativeIntentClassificationResponse, error) {
			if intent != "video dynamique" {
				t.Fatalf("intent = %q", intent)
			}
			return videoindexerstudio.NarrativeIntentClassificationResponse{SchemaVersion: 1, Profile: videoindexerstudio.NarrativeIntentProfileEnergetic}, nil
		}),
	}
	response, err := classifier.Classify(context.Background(), videoindexerstudio.NarrativeIntentClassificationRequest{SchemaVersion: 1, NarrativeIntent: "video dynamique"})
	if err != nil || response.Profile != videoindexerstudio.NarrativeIntentProfileEnergetic {
		t.Fatalf("classification = %#v, %v", response, err)
	}

	classifier.runner = narrativeIntentClassifierRunnerFunc(func(context.Context, string) (videoindexerstudio.NarrativeIntentClassificationResponse, error) {
		return videoindexerstudio.NarrativeIntentClassificationResponse{SchemaVersion: 1, Profile: "untrusted"}, nil
	})
	if _, err := classifier.Classify(context.Background(), videoindexerstudio.NarrativeIntentClassificationRequest{SchemaVersion: 1, NarrativeIntent: "video dynamique"}); err == nil || !strings.Contains(err.Error(), "invalid classifier response") {
		t.Fatalf("expected closed-contract rejection, got %v", err)
	}
}

func TestNarrativeIntentClassifierTimeout(t *testing.T) {
	classifier := narrativeIntentClassifier{
		timeout: time.Millisecond,
		runner: narrativeIntentClassifierRunnerFunc(func(ctx context.Context, _ string) (videoindexerstudio.NarrativeIntentClassificationResponse, error) {
			<-ctx.Done()
			return videoindexerstudio.NarrativeIntentClassificationResponse{}, ctx.Err()
		}),
	}
	_, err := classifier.Classify(context.Background(), videoindexerstudio.NarrativeIntentClassificationRequest{SchemaVersion: 1, NarrativeIntent: "calm recap"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline exceeded, got %v", err)
	}
}

func TestNarrativeIntentClassificationHandlerStrictErrorsAreSafe(t *testing.T) {
	server := NewServer(Config{APIKey: "test-key"}, nil)
	response := narrativeIntentClassificationRequest(t, server, `{}`)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("unconfigured status = %d", response.Code)
	}

	server.SetNarrativeIntentClassifier(narrativeIntentClassifierFunc(func(_ context.Context, req videoindexerstudio.NarrativeIntentClassificationRequest) (videoindexerstudio.NarrativeIntentClassificationResponse, error) {
		return videoindexerstudio.NarrativeIntentClassificationResponse{SchemaVersion: req.SchemaVersion, Profile: videoindexerstudio.NarrativeIntentProfileCalm}, nil
	}))
	valid, err := json.Marshal(videoindexerstudio.NarrativeIntentClassificationRequest{SchemaVersion: 1, NarrativeIntent: "recapitulatif calme"})
	if err != nil {
		t.Fatal(err)
	}
	response = narrativeIntentClassificationRequest(t, server, string(valid))
	if response.Code != http.StatusOK {
		t.Fatalf("valid status = %d: %s", response.Code, response.Body.String())
	}
	response = narrativeIntentClassificationRequest(t, server, `{"schemaVersion":1,"narrativeIntent":"calm recap","extra":true}`)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unknown field status = %d", response.Code)
	}
	response = narrativeIntentClassificationRequest(t, server, `{"schemaVersion":1,"narrativeIntent":"calm\nrecap"}`)
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unnormalized status = %d", response.Code)
	}
}

func TestNarrativeIntentClassificationHandlerDoesNotEchoIntent(t *testing.T) {
	const secretIntent = "private editorial preference"
	server := NewServer(Config{APIKey: "test-key"}, nil)
	server.SetNarrativeIntentClassifier(narrativeIntentClassifierFunc(func(context.Context, videoindexerstudio.NarrativeIntentClassificationRequest) (videoindexerstudio.NarrativeIntentClassificationResponse, error) {
		return videoindexerstudio.NarrativeIntentClassificationResponse{}, errors.New(secretIntent)
	}))
	body, err := json.Marshal(videoindexerstudio.NarrativeIntentClassificationRequest{SchemaVersion: 1, NarrativeIntent: secretIntent})
	if err != nil {
		t.Fatal(err)
	}
	response := narrativeIntentClassificationRequest(t, server, string(body))
	if response.Code != http.StatusBadGateway || strings.Contains(response.Body.String(), secretIntent) {
		t.Fatalf("unsafe classifier error: %d %s", response.Code, response.Body.String())
	}
}

func TestNarrativeIntentClassifierAcceptsMultilingualSocialShortFormProfile(t *testing.T) {
	classifier := narrativeIntentClassifier{timeout: time.Second, runner: narrativeIntentClassifierRunnerFunc(func(_ context.Context, intent string) (videoindexerstudio.NarrativeIntentClassificationResponse, error) {
		if intent != "robots dansants en mode video TikTok" {
			t.Fatalf("intent = %q", intent)
		}
		return videoindexerstudio.NarrativeIntentClassificationResponse{SchemaVersion: 1, Profile: videoindexerstudio.NarrativeIntentProfileSocialShortForm}, nil
	})}
	response, err := classifier.Classify(context.Background(), videoindexerstudio.NarrativeIntentClassificationRequest{SchemaVersion: 1, NarrativeIntent: "robots dansants en mode video TikTok"})
	if err != nil || response.Profile != videoindexerstudio.NarrativeIntentProfileSocialShortForm {
		t.Fatalf("classification = %#v, %v", response, err)
	}
}
