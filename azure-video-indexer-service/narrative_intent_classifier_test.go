package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/microsoft/agent-framework-go/agent/format/jsonformat"
	"github.com/zecloud/ai-video-studio/internal/videoindexerstudio"
)

type narrativeIntentClassifierFunc func(context.Context, videoindexerstudio.NarrativeIntentClassificationRequest) (videoindexerstudio.NarrativeIntentClassificationResponse, error)

func (f narrativeIntentClassifierFunc) Classify(ctx context.Context, req videoindexerstudio.NarrativeIntentClassificationRequest) (videoindexerstudio.NarrativeIntentClassificationResponse, error) {
	return f(ctx, req)
}

type narrativeIntentClassifierRunnerFunc func(context.Context, string) (videoindexerstudio.NarrativeIntentClassificationResponse, error)

func (f narrativeIntentClassifierRunnerFunc) RunClassification(ctx context.Context, intent string, _ bool) (videoindexerstudio.NarrativeIntentClassificationResponse, error) {
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
	if _, err := classifier.Classify(context.Background(), videoindexerstudio.NarrativeIntentClassificationRequest{SchemaVersion: 1, NarrativeIntent: "video dynamique"}); err == nil || narrativeFailureFor(err) != narrativeFailureInvalid {
		t.Fatalf("expected closed-contract rejection, got %v", err)
	}
}

func TestNarrativeIntentClassifierRejectsInvalidQuery(t *testing.T) {
	classifier := narrativeIntentClassifier{
		timeout: time.Second,
		runner: narrativeIntentClassifierRunnerFunc(func(context.Context, string) (videoindexerstudio.NarrativeIntentClassificationResponse, error) {
			return videoindexerstudio.NarrativeIntentClassificationResponse{
				SchemaVersion: 1,
				Profile:       videoindexerstudio.NarrativeIntentProfileSocialShortForm,
				Query: &videoindexerstudio.NarrativeQuery{
					SchemaVersion: 1,
					Coverage:      videoindexerstudio.NarrativeQueryCoverageBestSubset,
					Clauses: []videoindexerstudio.NarrativeQueryClause{{
						ID: "c1", Importance: videoindexerstudio.NarrativeQueryMust,
						Predicate: videoindexerstudio.NarrativeQueryVisibleEntity,
						Terms:     []string{"NOT NORMALIZED"}, MatchMode: videoindexerstudio.NarrativeQueryMatchAny,
						Relation: videoindexerstudio.NarrativeQueryRelationOverlap,
					}},
				},
			}, nil
		}),
	}
	response, err := classifier.Classify(context.Background(), videoindexerstudio.NarrativeIntentClassificationRequest{SchemaVersion: 1, NarrativeIntent: "dynamic tiktok video"})
	if err == nil || narrativeFailureFor(err) != narrativeFailureInvalid {
		t.Fatalf("expected invalid query rejection, got %#v, %v", response, err)
	}
}

func TestNarrativeIntentClassifierRetriesWithCorrectionAfterInvalidResponse(t *testing.T) {
	attempts := 0
	classifier := narrativeIntentClassifier{
		timeout: time.Second,
		runner: narrativeIntentClassifierRunnerFunc(func(_ context.Context, _ string) (videoindexerstudio.NarrativeIntentClassificationResponse, error) {
			attempts++
			if attempts == 1 {
				return videoindexerstudio.NarrativeIntentClassificationResponse{SchemaVersion: 1, Profile: "invalid"}, nil
			}
			return videoindexerstudio.NarrativeIntentClassificationResponse{SchemaVersion: 1, Profile: videoindexerstudio.NarrativeIntentProfileCalm}, nil
		}),
	}
	response, err := classifier.Classify(context.Background(), videoindexerstudio.NarrativeIntentClassificationRequest{SchemaVersion: 1, NarrativeIntent: "calm recap"})
	if err != nil || attempts != 2 || response.Profile != videoindexerstudio.NarrativeIntentProfileCalm {
		t.Fatalf("corrective classification = %#v, %v, attempts=%d", response, err, attempts)
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
	} else {
		var apiErr videoindexerstudio.APIErrorResponse
		if err := json.Unmarshal(response.Body.Bytes(), &apiErr); err != nil || apiErr.Code != "narrative_intent_classification_unavailable" {
			t.Fatalf("unconfigured response = %#v, %v", apiErr, err)
		}
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

func TestNarrativeIntentClassifierRetriesOnlyTransientFailure(t *testing.T) {
	attempts := 0
	classifier := narrativeIntentClassifier{timeout: time.Second, runner: narrativeIntentClassifierRunnerFunc(func(context.Context, string) (videoindexerstudio.NarrativeIntentClassificationResponse, error) {
		attempts++
		if attempts == 1 {
			return videoindexerstudio.NarrativeIntentClassificationResponse{}, errors.New("temporary transport failure")
		}
		return videoindexerstudio.NarrativeIntentClassificationResponse{SchemaVersion: 1, Profile: videoindexerstudio.NarrativeIntentProfileEnergetic}, nil
	})}
	if _, err := classifier.Classify(context.Background(), videoindexerstudio.NarrativeIntentClassificationRequest{SchemaVersion: 1, NarrativeIntent: "dynamic tiktok video"}); err != nil || attempts != 2 {
		t.Fatalf("classification = %v, attempts = %d", err, attempts)
	}
}

// TestFoundryNarrativeIntentClassificationSchemaRequiresEveryProperty guards
// against a regression of the production bug where Azure OpenAI/Foundry
// strict-mode structured output rejected the classifier's schema with
// "invalid_json_schema" because "omitempty"-tagged fields on the shared
// videoindexerstudio.NarrativeIntentClassificationResponse type were excluded
// from the JSON schema's "required" array at every nesting level. The
// Foundry-facing mirror type must keep every property required (nested
// optionality is expressed via nullable types instead), mirroring the same
// guarantee already enforced for EditPlan.
func TestFoundryNarrativeIntentClassificationSchemaRequiresEveryProperty(t *testing.T) {
	format, err := jsonformat.For[foundryNarrativeIntentClassification]()
	if err != nil {
		t.Fatalf("generate foundryNarrativeIntentClassification response format: %v", err)
	}
	schema, err := json.Marshal(format.Schema)
	if err != nil {
		t.Fatalf("marshal foundryNarrativeIntentClassification schema: %v", err)
	}

	var root map[string]any
	if err := json.Unmarshal(schema, &root); err != nil {
		t.Fatalf("decode foundryNarrativeIntentClassification schema: %v", err)
	}
	assertStrictObjectSchemas(t, root, "foundryNarrativeIntentClassification")
}

func TestNarrativeIntentClassificationFromFoundryMapsNilQuery(t *testing.T) {
	result := narrativeIntentClassificationFromFoundry(foundryNarrativeIntentClassification{SchemaVersion: 1, Profile: "calm"})
	if result.SchemaVersion != 1 || result.Profile != videoindexerstudio.NarrativeIntentProfileCalm || result.Query != nil {
		t.Fatalf("nil query mapping = %#v", result)
	}
}

func TestNarrativeIntentClassificationFromFoundryMapsQueryFields(t *testing.T) {
	output := foundryNarrativeIntentClassification{
		SchemaVersion: 1,
		Profile:       "social_short_form",
		Query: &foundryNarrativeQuery{
			SchemaVersion: 1,
			Coverage:      "best_subset",
			Ambiguous:     true,
			Clauses: []foundryNarrativeQueryClause{{
				ID:         "c1",
				Importance: "must",
				Predicate:  "visible_entity",
				Terms:      []string{"robot"},
				MatchMode:  "any",
				Relation:   "overlap",
			}},
		},
	}
	result := narrativeIntentClassificationFromFoundry(output)
	want := videoindexerstudio.NarrativeIntentClassificationResponse{
		SchemaVersion: 1,
		Profile:       videoindexerstudio.NarrativeIntentProfileSocialShortForm,
		Query: &videoindexerstudio.NarrativeQuery{
			SchemaVersion: 1,
			Coverage:      videoindexerstudio.NarrativeQueryCoverageBestSubset,
			Ambiguous:     true,
			Clauses: []videoindexerstudio.NarrativeQueryClause{{
				ID:         "c1",
				Importance: videoindexerstudio.NarrativeQueryMust,
				Predicate:  videoindexerstudio.NarrativeQueryVisibleEntity,
				Terms:      []string{"robot"},
				MatchMode:  videoindexerstudio.NarrativeQueryMatchAny,
				Relation:   videoindexerstudio.NarrativeQueryRelationOverlap,
			}},
		},
	}
	if !reflect.DeepEqual(result, want) {
		t.Fatalf("query mapping = %#v, want %#v", result, want)
	}
}

func TestNarrativeIntentClassifierNormalizesOnlyOmittedSchemaVersion(t *testing.T) {
	classifier := narrativeIntentClassifier{timeout: time.Second, runner: narrativeIntentClassifierRunnerFunc(func(context.Context, string) (videoindexerstudio.NarrativeIntentClassificationResponse, error) {
		return videoindexerstudio.NarrativeIntentClassificationResponse{Profile: videoindexerstudio.NarrativeIntentProfileSocialShortForm}, nil
	})}
	response, err := classifier.Classify(context.Background(), videoindexerstudio.NarrativeIntentClassificationRequest{SchemaVersion: 1, NarrativeIntent: "dynamic tiktok video"})
	if err != nil || response.SchemaVersion != 1 || response.Profile != videoindexerstudio.NarrativeIntentProfileSocialShortForm {
		t.Fatalf("omitted schema version = %#v, %v", response, err)
	}
	classifier.runner = narrativeIntentClassifierRunnerFunc(func(context.Context, string) (videoindexerstudio.NarrativeIntentClassificationResponse, error) {
		return videoindexerstudio.NarrativeIntentClassificationResponse{SchemaVersion: 2, Profile: videoindexerstudio.NarrativeIntentProfileSocialShortForm}, nil
	})
	if _, err := classifier.Classify(context.Background(), videoindexerstudio.NarrativeIntentClassificationRequest{SchemaVersion: 1, NarrativeIntent: "dynamic tiktok video"}); narrativeFailureFor(err) != narrativeFailureInvalid {
		t.Fatalf("nonzero incompatible schema must remain invalid: %v", err)
	}
}
