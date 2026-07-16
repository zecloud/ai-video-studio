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

type narrativeSegmentPlannerFunc func(context.Context, videoindexerstudio.NarrativeSegmentPlanningRequest) (videoindexerstudio.NarrativeSegmentPlanningResponse, error)

func (f narrativeSegmentPlannerFunc) Plan(ctx context.Context, request videoindexerstudio.NarrativeSegmentPlanningRequest) (videoindexerstudio.NarrativeSegmentPlanningResponse, error) {
	return f(ctx, request)
}

type narrativeSegmentPlannerRunnerFunc func(context.Context, string) (videoindexerstudio.NarrativeSegmentPlanningResponse, error)

func (f narrativeSegmentPlannerRunnerFunc) RunSegmentPlan(ctx context.Context, packet string) (videoindexerstudio.NarrativeSegmentPlanningResponse, error) {
	return f(ctx, packet)
}

func segmentPlanningRequest() videoindexerstudio.NarrativeSegmentPlanningRequest {
	return videoindexerstudio.NarrativeSegmentPlanningRequest{SchemaVersion: videoindexerstudio.NarrativeSegmentPlanningSchemaVersion, CompositionID: "composition-1", NarrativeIntent: "robots dansants en mode video TikTok", Profile: videoindexerstudio.NarrativeIntentProfileSocialShortForm, Catalog: []videoindexerstudio.NarrativeSegmentCatalogItem{{SegmentID: "segment-1", CandidateID: "candidate-1", SourceAssetID: "asset-1", AllowedStartMs: 1_000, AllowedEndMs: 7_000, EvidenceIDs: []string{"evidence-1"}}}}
}

func TestNarrativeSegmentPlannerRejectsLimitTimeoutAndInvalidResponse(t *testing.T) {
	request := segmentPlanningRequest()
	planner := narrativeSegmentPlanner{timeout: time.Second, maxCatalog: 1, maxSegments: 1, runner: narrativeSegmentPlannerRunnerFunc(func(_ context.Context, _ string) (videoindexerstudio.NarrativeSegmentPlanningResponse, error) {
		return videoindexerstudio.NarrativeSegmentPlanningResponse{SchemaVersion: videoindexerstudio.NarrativeSegmentPlanningSchemaVersion, Segments: []videoindexerstudio.NarrativeSegmentPlanItem{{SegmentID: "segment-1", Role: videoindexerstudio.NarrativeSegmentRoleHook, EvidenceIDs: []string{"evidence-1"}}}}, nil
	})}
	if _, err := planner.Plan(context.Background(), request); err != nil {
		t.Fatalf("valid plan: %v", err)
	}
	planner.runner = narrativeSegmentPlannerRunnerFunc(func(context.Context, string) (videoindexerstudio.NarrativeSegmentPlanningResponse, error) {
		return videoindexerstudio.NarrativeSegmentPlanningResponse{}, nil
	})
	if _, err := planner.Plan(context.Background(), request); err == nil || narrativeFailureFor(err) != narrativeFailureInvalid {
		t.Fatalf("expected response rejection, got %v", err)
	}
	planner.timeout = time.Millisecond
	planner.runner = narrativeSegmentPlannerRunnerFunc(func(ctx context.Context, _ string) (videoindexerstudio.NarrativeSegmentPlanningResponse, error) {
		<-ctx.Done()
		return videoindexerstudio.NarrativeSegmentPlanningResponse{}, ctx.Err()
	})
	if _, err := planner.Plan(context.Background(), request); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected timeout, got %v", err)
	}
}

func TestNarrativeSegmentPlanningHandlerStrictAndPrivate(t *testing.T) {
	server := NewServer(Config{APIKey: "test-key"}, nil)
	body, err := json.Marshal(segmentPlanningRequest())
	if err != nil {
		t.Fatal(err)
	}
	request := func(payload []byte) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/narrative-segment-plans", bytes.NewReader(payload))
		req.Header.Set("X-API-Key", "test-key")
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, req)
		return response
	}
	if response := request(body); response.Code != http.StatusServiceUnavailable {
		t.Fatalf("unavailable = %d", response.Code)
	}
	secret := "private editorial preference"
	server.SetNarrativeSegmentPlanner(narrativeSegmentPlannerFunc(func(context.Context, videoindexerstudio.NarrativeSegmentPlanningRequest) (videoindexerstudio.NarrativeSegmentPlanningResponse, error) {
		return videoindexerstudio.NarrativeSegmentPlanningResponse{}, errors.New(secret)
	}))
	if response := request(body); response.Code != http.StatusBadGateway || strings.Contains(response.Body.String(), secret) {
		t.Fatalf("unsafe planner error: %d %s", response.Code, response.Body.String())
	}
	if response := request([]byte(`{"schemaVersion":1,"extra":true}`)); response.Code != http.StatusBadRequest {
		t.Fatalf("unknown field = %d", response.Code)
	}
}

func TestNarrativeSegmentPlannerConfigCapsBounds(t *testing.T) {
	cfg := (Config{NarrativeSegmentPlannerTimeout: 30 * time.Second, NarrativeSegmentPlannerMaxCatalog: 49, NarrativeSegmentPlannerMaxSegments: 49}).Normalize()
	if cfg.NarrativeSegmentPlannerTimeout != 20*time.Second || cfg.NarrativeSegmentPlannerMaxCatalog != 48 || cfg.NarrativeSegmentPlannerMaxSegments != 24 {
		t.Fatalf("normalized planner config = %#v", cfg)
	}
}

func TestNarrativeSegmentPlannerRetriesTransientButNotInvalidResponse(t *testing.T) {
	request := segmentPlanningRequest()
	attempts := 0
	planner := narrativeSegmentPlanner{timeout: time.Second, maxCatalog: 1, maxSegments: 1, runner: narrativeSegmentPlannerRunnerFunc(func(context.Context, string) (videoindexerstudio.NarrativeSegmentPlanningResponse, error) {
		attempts++
		if attempts == 1 {
			return videoindexerstudio.NarrativeSegmentPlanningResponse{}, errors.New("temporary upstream failure")
		}
		return videoindexerstudio.NarrativeSegmentPlanningResponse{SchemaVersion: 1, Segments: []videoindexerstudio.NarrativeSegmentPlanItem{{SegmentID: "segment-1", Role: videoindexerstudio.NarrativeSegmentRoleHook, EvidenceIDs: []string{"evidence-1"}}}}, nil
	})}
	if _, err := planner.Plan(context.Background(), request); err != nil || attempts != 2 {
		t.Fatalf("plan = %v, attempts = %d", err, attempts)
	}
	attempts = 0
	planner.runner = narrativeSegmentPlannerRunnerFunc(func(context.Context, string) (videoindexerstudio.NarrativeSegmentPlanningResponse, error) {
		attempts++
		return videoindexerstudio.NarrativeSegmentPlanningResponse{}, nil
	})
	if _, err := planner.Plan(context.Background(), request); narrativeFailureFor(err) != narrativeFailureInvalid || attempts != 1 {
		t.Fatalf("invalid response = %v, attempts = %d", err, attempts)
	}
}

func TestNarrativeSegmentPlannerNormalizesOnlyOmittedSchemaVersion(t *testing.T) {
	request := segmentPlanningRequest()
	planner := narrativeSegmentPlanner{timeout: time.Second, maxCatalog: 1, maxSegments: 1, runner: narrativeSegmentPlannerRunnerFunc(func(context.Context, string) (videoindexerstudio.NarrativeSegmentPlanningResponse, error) {
		return videoindexerstudio.NarrativeSegmentPlanningResponse{Segments: []videoindexerstudio.NarrativeSegmentPlanItem{{SegmentID: "segment-1", Role: videoindexerstudio.NarrativeSegmentRoleHook, EvidenceIDs: []string{"evidence-1"}}}}, nil
	})}
	response, err := planner.Plan(context.Background(), request)
	if err != nil || response.SchemaVersion != videoindexerstudio.NarrativeSegmentPlanningSchemaVersion {
		t.Fatalf("omitted schema version = %#v, %v", response, err)
	}
	planner.runner = narrativeSegmentPlannerRunnerFunc(func(context.Context, string) (videoindexerstudio.NarrativeSegmentPlanningResponse, error) {
		return videoindexerstudio.NarrativeSegmentPlanningResponse{SchemaVersion: 2, Segments: []videoindexerstudio.NarrativeSegmentPlanItem{{SegmentID: "segment-1", Role: videoindexerstudio.NarrativeSegmentRoleHook, EvidenceIDs: []string{"evidence-1"}}}}, nil
	})
	if _, err := planner.Plan(context.Background(), request); narrativeFailureFor(err) != narrativeFailureInvalid {
		t.Fatalf("nonzero incompatible schema must remain invalid: %v", err)
	}
}
