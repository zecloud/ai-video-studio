package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

type serviceContractFixture struct {
	APIError         APIErrorResponse      `json:"apiError"`
	CreateJobRequest CreateIndexJobRequest `json:"createJobRequest"`
	JobDocument      JobDocument           `json:"jobDocument"`
	JobResponse      JobResponse           `json:"jobResponse"`
	JobListResponse  JobListResponse       `json:"jobListResponse"`
}

type rawServiceContractFixture struct {
	APIError         json.RawMessage `json:"apiError"`
	CreateJobRequest json.RawMessage `json:"createJobRequest"`
	JobDocument      json.RawMessage `json:"jobDocument"`
	JobResponse      json.RawMessage `json:"jobResponse"`
	JobListResponse  json.RawMessage `json:"jobListResponse"`
}

func loadServiceContractFixture(t *testing.T) (serviceContractFixture, rawServiceContractFixture) {
	t.Helper()

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("failed to determine fixture path")
	}
	path := filepath.Clean(filepath.Join(filepath.Dir(file), "testdata", "service_contract_fixture.json"))

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	var typed serviceContractFixture
	if err := json.Unmarshal(data, &typed); err != nil {
		t.Fatalf("decode typed fixture: %v", err)
	}
	var raw rawServiceContractFixture
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("decode raw fixture: %v", err)
	}
	return typed, raw
}

func TestServiceContractFixtureMatchesTypes(t *testing.T) {
	typed, raw := loadServiceContractFixture(t)

	if typed.JobDocument.SchemaVersion != schemaVersion {
		t.Fatalf("schema version = %d want %d", typed.JobDocument.SchemaVersion, schemaVersion)
	}
	assertServiceJSONMatches(t, typed.APIError, raw.APIError)
	assertServiceJSONMatches(t, typed.CreateJobRequest, raw.CreateJobRequest)
	assertServiceJSONMatches(t, typed.JobDocument, raw.JobDocument)
	assertServiceJSONMatches(t, typed.JobResponse, raw.JobResponse)
	assertServiceJSONMatches(t, typed.JobListResponse, raw.JobListResponse)
}

func assertServiceJSONMatches(t *testing.T, got any, wantJSON json.RawMessage) {
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
