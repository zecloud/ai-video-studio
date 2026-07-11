package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestServerCreateJobAuthAndValidation(t *testing.T) {
	store := newMemoryJobStore()
	manager := NewJobManager(JobManagerConfig{QueueSize: 8, WorkerConcurrency: 1}, store, nil, nil, nil, fixedClock{now: time.Date(2026, 7, 10, 13, 46, 9, 0, time.UTC)})
	server := NewServer(Config{APIKey: "test-api-key"}, manager)

	t.Run("missing authorization", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/index-jobs", strings.NewReader(`{"oneDriveItemId":"item-001","oneDriveAccessToken":"token-abc"}`))
		rr := httptest.NewRecorder()
		server.Handler().ServeHTTP(rr, req)
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("unexpected status: %d", rr.Code)
		}
		var body APIErrorResponse
		if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if body.Code != "unauthorized" {
			t.Fatalf("unexpected error body: %#v", body)
		}
	})

	t.Run("strict body validation", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/index-jobs", strings.NewReader(`{"oneDriveItemId":"item-001","oneDriveAccessToken":"token-abc","unexpected":true}`))
		req.Header.Set("X-API-Key", "test-api-key")
		rr := httptest.NewRecorder()
		server.Handler().ServeHTTP(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("unexpected status: %d", rr.Code)
		}
		var body APIErrorResponse
		if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if body.Code != "bad_request" {
			t.Fatalf("unexpected error body: %#v", body)
		}
	})

	t.Run("accepted job response", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/index-jobs", strings.NewReader(`{"oneDriveItemId":"item-001","oneDriveAccessToken":"token-abc","sourceName":"clip.mp4","correlationId":"batch-001"}`))
		req.Header.Set("Authorization", "Bearer test-api-key")
		rr := httptest.NewRecorder()
		server.Handler().ServeHTTP(rr, req)
		if rr.Code != http.StatusAccepted {
			t.Fatalf("unexpected status: %d", rr.Code)
		}
		if rr.Header().Get("Location") == "" {
			t.Fatal("expected location header")
		}
		var resp JobResponse
		if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if resp.Job.ID == "" || resp.Job.Status != JobStatusQueued {
			t.Fatalf("unexpected job response: %#v", resp.Job)
		}
		stored, err := store.Get(req.Context(), resp.Job.ID)
		if err != nil {
			t.Fatalf("store get: %v", err)
		}
		if stored.CorrelationID != "batch-001" {
			t.Fatalf("expected correlation id to persist, got %#v", stored.JobDocument)
		}
	})
}
