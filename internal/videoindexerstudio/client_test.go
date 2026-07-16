package videoindexerstudio

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

type typedContractFixture struct {
	APIError         APIErrorResponse `json:"apiError"`
	CreateJobRequest CreateJobRequest `json:"createJobRequest"`
	Job              Job              `json:"job"`
	JobResponse      JobResponse      `json:"jobResponse"`
	JobListResponse  JobListResponse  `json:"jobListResponse"`
}

type rawContractFixture struct {
	CreateJobRequest json.RawMessage `json:"createJobRequest"`
	JobResponse      json.RawMessage `json:"jobResponse"`
	JobListResponse  json.RawMessage `json:"jobListResponse"`
	APIError         json.RawMessage `json:"apiError"`
}

func loadFixtures(t *testing.T) (typedContractFixture, rawContractFixture) {
	t.Helper()

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("failed to determine fixture path")
	}
	path := filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", "contracts", "videoindexer", "v1", "fixtures.json"))

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read fixture: %v", err)
	}

	var typed typedContractFixture
	if err := json.Unmarshal(data, &typed); err != nil {
		t.Fatalf("failed to decode typed fixture: %v", err)
	}
	var raw rawContractFixture
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("failed to decode raw fixture: %v", err)
	}
	return typed, raw
}

func testClient(t *testing.T, server *httptest.Server) *Client {
	t.Helper()
	t.Setenv(loopbackHTTPOptInEnv, "1")
	client, err := NewClient(Config{Endpoint: server.URL, APIKey: "test-api-key"}, server.Client())
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return client
}

func TestCreateJob_Success(t *testing.T) {
	fixture, _ := loadFixtures(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("unexpected method: %s", r.Method)
		}
		if r.URL.Path != "/api/v1/jobs" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-api-key" {
			t.Fatalf("unexpected Authorization header: %q", got)
		}
		if got := r.Header.Get("X-API-Key"); got != "test-api-key" {
			t.Fatalf("unexpected X-API-Key header: %q", got)
		}

		var req CreateJobRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}
		if req != fixture.CreateJobRequest {
			t.Fatalf("request mismatch:\nexpected: %#v\nactual:   %#v", fixture.CreateJobRequest, req)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(fixture.JobResponse)
	}))
	defer server.Close()

	client := testClient(t, server)
	resp, err := client.CreateJob(context.Background(), fixture.CreateJobRequest)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !reflect.DeepEqual(resp.Job, fixture.JobResponse.Job) {
		t.Fatalf("response mismatch:\nexpected: %#v\nactual:   %#v", fixture.JobResponse.Job, resp.Job)
	}
}

func TestGetJob_Success(t *testing.T) {
	fixture, _ := loadFixtures(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("unexpected method: %s", r.Method)
		}
		if r.URL.Path != "/api/v1/jobs/job-001" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(fixture.JobResponse)
	}))
	defer server.Close()

	client := testClient(t, server)
	resp, err := client.GetJob(context.Background(), "job-001")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !reflect.DeepEqual(resp.Job, fixture.JobResponse.Job) {
		t.Fatalf("response mismatch:\nexpected: %#v\nactual:   %#v", fixture.JobResponse.Job, resp.Job)
	}
}

func TestListJobs_Success(t *testing.T) {
	fixture, _ := loadFixtures(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("unexpected method: %s", r.Method)
		}
		if r.URL.Path != "/api/v1/jobs" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(fixture.JobListResponse)
	}))
	defer server.Close()

	client := testClient(t, server)
	resp, err := client.ListJobs(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Jobs) != len(fixture.JobListResponse.Jobs) {
		t.Fatalf("unexpected number of jobs: %d", len(resp.Jobs))
	}
}

func TestCancelJob_Success(t *testing.T) {
	fixture, _ := loadFixtures(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("unexpected method: %s", r.Method)
		}
		if r.URL.Path != "/api/v1/jobs/job-001/cancel" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(fixture.JobResponse)
	}))
	defer server.Close()

	client := testClient(t, server)
	resp, err := client.CancelJob(context.Background(), "job-001")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !reflect.DeepEqual(resp.Job, fixture.JobResponse.Job) {
		t.Fatalf("response mismatch:\nexpected: %#v\nactual:   %#v", fixture.JobResponse.Job, resp.Job)
	}
}

func TestErrorResponse_Bounded(t *testing.T) {
	fixture, _ := loadFixtures(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"code":"service_unavailable","message":"` + strings.Repeat("x", 8192) + `","retryable":true}`))
	}))
	defer server.Close()

	client := testClient(t, server)
	_, err := client.GetJob(context.Background(), fixture.Job.ID)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, ErrUnexpectedStatus) {
		t.Fatalf("expected ErrUnexpectedStatus, got: %v", err)
	}
	if strings.Contains(err.Error(), strings.Repeat("x", 5000)) {
		t.Fatalf("error body was not bounded: %v", err)
	}
}

func TestErrorResponse_DecodesStructured(t *testing.T) {
	fixture, _ := loadFixtures(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"code":"service_unavailable","message":"temporary outage","retryable":true}`))
	}))
	defer server.Close()

	client := testClient(t, server)
	_, err := client.GetJob(context.Background(), fixture.Job.ID)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var respErr *ResponseError
	if !errors.As(err, &respErr) {
		t.Fatalf("expected ResponseError, got %T", err)
	}
	if !errors.Is(err, ErrUnexpectedStatus) {
		t.Fatalf("expected ErrUnexpectedStatus, got: %v", err)
	}
	if respErr.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("unexpected status code: %d", respErr.StatusCode)
	}
	if respErr.Code != "service_unavailable" || respErr.Message != "temporary outage" || !respErr.Retryable {
		t.Fatalf("unexpected structured error: %#v", respErr.APIErrorResponse)
	}
}

func TestValidation(t *testing.T) {
	client, err := NewClient(Config{Endpoint: "https://example.com", APIKey: "test-api-key"}, http.DefaultClient)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	if _, err := client.CreateJob(context.Background(), CreateJobRequest{}); err == nil {
		t.Fatal("expected validation error")
	}
	if _, err := client.GetJob(context.Background(), ""); err == nil {
		t.Fatal("expected validation error")
	}
	if _, err := client.CancelJob(context.Background(), "bad/job"); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestNormalizeEndpointValidation(t *testing.T) {
	t.Setenv(loopbackHTTPOptInEnv, "0")

	tests := []struct {
		name    string
		raw     string
		wantErr string
	}{
		{name: "http rejected", raw: "http://example.com", wantErr: "https"},
		{name: "malformed rejected", raw: "not-a-url", wantErr: "absolute"},
		{name: "credentials rejected", raw: "https://user:pass@example.com", wantErr: "credentials"},
		{name: "fragment rejected", raw: "https://example.com/#frag", wantErr: "fragment"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NormalizeEndpoint(tc.raw); err == nil || !strings.Contains(strings.ToLower(err.Error()), tc.wantErr) {
				t.Fatalf("NormalizeEndpoint(%q) = %v", tc.raw, err)
			}
		})
	}
}

func TestNormalizeEndpointAcceptsHTTPS(t *testing.T) {
	got, err := NormalizeEndpoint("https://example.com/base/")
	if err != nil {
		t.Fatalf("NormalizeEndpoint: %v", err)
	}
	if got != "https://example.com/base" {
		t.Fatalf("NormalizeEndpoint = %q", got)
	}
}

func TestCreateJob_RedirectDoesNotForwardCredentials(t *testing.T) {
	t.Setenv(loopbackHTTPOptInEnv, "1")

	redirected := make(chan struct{}, 1)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirected <- struct{}{}
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("unexpected method: %s", r.Method)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-api-key" {
			t.Fatalf("unexpected Authorization header: %q", got)
		}
		if got := r.Header.Get("X-API-Key"); got != "test-api-key" {
			t.Fatalf("unexpected X-API-Key header: %q", got)
		}
		http.Redirect(w, r, target.URL+"/api/v1/jobs", http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()

	client, err := NewClient(Config{Endpoint: redirect.URL, APIKey: "test-api-key"}, redirect.Client())
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if client == nil {
		t.Fatal("expected client")
	}
	_, err = client.CreateJob(context.Background(), CreateJobRequest{
		OneDriveItemID:      "item-001",
		OneDriveAccessToken: "token-abc",
	})
	if err == nil {
		t.Fatal("expected redirect error")
	}
	select {
	case <-redirected:
		t.Fatal("redirect target should not receive credentials")
	default:
	}
}

func TestContractTypes_MarshalMatchFixture(t *testing.T) {
	typed, raw := loadFixtures(t)

	assertJSONMatches(t, typed.CreateJobRequest, raw.CreateJobRequest)
	assertJSONMatches(t, typed.JobResponse, raw.JobResponse)
	assertJSONMatches(t, typed.JobListResponse, raw.JobListResponse)
}

func TestContractErrorShape(t *testing.T) {
	typed, raw := loadFixtures(t)
	assertJSONMatches(t, typed.APIError, raw.APIError)
}

func assertJSONMatches(t *testing.T, got any, wantJSON json.RawMessage) {
	t.Helper()
	gotJSON, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var gotValue any
	if err := json.Unmarshal(gotJSON, &gotValue); err != nil {
		t.Fatalf("unmarshal got: %v", err)
	}
	var wantValue any
	if err := json.Unmarshal(wantJSON, &wantValue); err != nil {
		t.Fatalf("unmarshal want: %v", err)
	}
	if !deepEqualJSON(gotValue, wantValue) {
		t.Fatalf("json mismatch:\n got: %s\nwant: %s", gotJSON, string(wantJSON))
	}
}

func deepEqualJSON(a, b any) bool {
	switch av := a.(type) {
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for k, v := range av {
			if !deepEqualJSON(v, bv[k]) {
				return false
			}
		}
		return true
	case []any:
		bv, ok := b.([]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for i := range av {
			if !deepEqualJSON(av[i], bv[i]) {
				return false
			}
		}
		return true
	default:
		return a == b
	}
}

func narrativeRankingRequestFixture() NarrativeRankingRequest {
	return NarrativeRankingRequest{
		SchemaVersion: NarrativeRankingSchemaVersion,
		CompositionID: "composition-1",
		Candidates:    []NarrativeRankingCandidate{{ID: "clip-a", SourceAssetID: "asset-a", StartMs: 0, EndMs: 100, EvidenceIDs: []string{"asset-a:scene:scene-a"}}},
		Evidence:      []NarrativeEvidence{{ID: "asset-a:scene:scene-a", SourceAssetID: "asset-a", Kind: "scene", StartMs: 0, EndMs: 100}},
	}
}

func TestRankNarrative_Success(t *testing.T) {
	request := narrativeRankingRequestFixture()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/narrative-rankings" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-api-key" || r.Header.Get("X-API-Key") != "test-api-key" {
			t.Fatalf("missing narrative ranking credentials")
		}
		var got NarrativeRankingRequest
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil || !reflect.DeepEqual(got, request) {
			t.Fatalf("unexpected narrative request: %#v, %v", got, err)
		}
		_ = json.NewEncoder(w).Encode(NarrativeRankingResponse{SchemaVersion: NarrativeRankingSchemaVersion, OrderedClips: []NarrativeRankedClip{{CandidateID: "clip-a", EvidenceIDs: []string{"asset-a:scene:scene-a"}}}})
	}))
	defer server.Close()

	response, err := testClient(t, server).RankNarrative(context.Background(), request)
	if err != nil || response.OrderedClips[0].CandidateID != "clip-a" {
		t.Fatalf("RankNarrative = %#v, %v", response, err)
	}
}

func TestRankNarrative_PropagatesServiceError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(APIErrorResponse{Code: "narrative_ranker_unavailable", Message: "unavailable", Retryable: true})
	}))
	defer server.Close()

	_, err := testClient(t, server).RankNarrative(context.Background(), narrativeRankingRequestFixture())
	var responseErr *ResponseError
	if !errors.As(err, &responseErr) || responseErr.StatusCode != http.StatusServiceUnavailable || responseErr.Code != "narrative_ranker_unavailable" || !responseErr.Retryable {
		t.Fatalf("unexpected error: %v", err)
	}
}
