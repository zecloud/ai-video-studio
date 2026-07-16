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

func TestNarrativeRankingHandlerReturnsSafeFailureCodes(t *testing.T) {
	request, err := json.Marshal(narrativeRequest())
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name    string
		failure error
		status  int
		code    string
	}{
		{name: "timeout", failure: narrativeFailureError(narrativeFailureTimeout, context.DeadlineExceeded), status: http.StatusGatewayTimeout, code: "narrative_ranking_timeout"},
		{name: "invalid response", failure: narrativeFailureError(narrativeFailureInvalid, errors.New("private provider response")), status: http.StatusBadGateway, code: "narrative_ranking_invalid_response"},
		{name: "limit", failure: narrativeFailureError(narrativeFailureLimit, errors.New("private limit")), status: http.StatusUnprocessableEntity, code: "narrative_ranking_request_limit"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := NewServer(Config{APIKey: "test-key"}, nil)
			server.SetNarrativeRanker(narrativeRankerFunc(func(context.Context, videoindexerstudio.NarrativeRankingRequest) (videoindexerstudio.NarrativeRankingResponse, error) {
				return videoindexerstudio.NarrativeRankingResponse{}, test.failure
			}))
			response := narrativeRankingHTTPTestRequest(t, server, string(request))
			var apiErr APIErrorResponse
			if err := json.NewDecoder(response.Body).Decode(&apiErr); err != nil || response.Code != test.status || apiErr.Code != test.code || strings.Contains(apiErr.Message, "private") {
				t.Fatalf("safe mapping = status %d payload %#v err %v", response.Code, apiErr, err)
			}
		})
	}
}
