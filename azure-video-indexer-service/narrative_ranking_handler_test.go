package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zecloud/ai-video-studio/internal/videoindexerstudio"
)

func narrativeRankingHTTPTestRequest(t *testing.T, server *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/narrative-rankings", bytes.NewBufferString(body))
	req.Header.Set("X-API-Key", "test-key")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, req)
	return response
}

func TestNarrativeRankingHandlerUnavailable(t *testing.T) {
	server := NewServer(Config{APIKey: "test-key"}, nil)
	response := narrativeRankingHTTPTestRequest(t, server, `{}`)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusServiceUnavailable)
	}
	var apiErr APIErrorResponse
	_ = json.NewDecoder(response.Body).Decode(&apiErr)
	if apiErr.Code != "narrative_ranker_unavailable" || !apiErr.Retryable {
		t.Fatalf("unexpected error: %#v", apiErr)
	}
}

func TestNarrativeRankingHandlerValidatesAndMapsErrors(t *testing.T) {
	server := NewServer(Config{APIKey: "test-key"}, nil)
	server.SetNarrativeRanker(narrativeRankerFunc(func(_ context.Context, req videoindexerstudio.NarrativeRankingRequest) (videoindexerstudio.NarrativeRankingResponse, error) {
		return videoindexerstudio.NarrativeRankingResponse{SchemaVersion: videoindexerstudio.NarrativeRankingSchemaVersion, OrderedClips: []videoindexerstudio.NarrativeRankedClip{{CandidateID: req.Candidates[0].ID}}}, nil
	}))
	request, err := json.Marshal(narrativeRequest())
	if err != nil {
		t.Fatal(err)
	}
	response := narrativeRankingHTTPTestRequest(t, server, string(request))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}

	response = narrativeRankingHTTPTestRequest(t, server, `{}`)
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusUnprocessableEntity)
	}

	response = narrativeRankingHTTPTestRequest(t, server, `{"schemaVersion":1} trailing`)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusBadRequest)
	}
}

func TestNarrativeRankingHandlerMapsDeadline(t *testing.T) {
	server := NewServer(Config{APIKey: "test-key"}, nil)
	server.SetNarrativeRanker(narrativeRankerFunc(func(context.Context, videoindexerstudio.NarrativeRankingRequest) (videoindexerstudio.NarrativeRankingResponse, error) {
		return videoindexerstudio.NarrativeRankingResponse{}, context.DeadlineExceeded
	}))
	request, err := json.Marshal(narrativeRequest())
	if err != nil {
		t.Fatal(err)
	}
	response := narrativeRankingHTTPTestRequest(t, server, string(request))
	if response.Code != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusGatewayTimeout)
	}
}
