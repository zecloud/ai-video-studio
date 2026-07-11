package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestRedactionHelpers(t *testing.T) {
	got := redactURLsInText("download https://example.com/path?sig=secret&token=abc")
	if strings.Contains(got, "sig=secret") || strings.Contains(got, "token=abc") {
		t.Fatalf("query values leaked: %q", got)
	}
	if !strings.Contains(got, "https://example.com/path") {
		t.Fatalf("expected sanitized url, got %q", got)
	}
}

func TestObservabilitySpansMetricsAndReady(t *testing.T) {
	traceExporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(traceExporter))
	metricReader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(metricReader))

	obs := &Observability{
		logger:        slog.New(slog.NewJSONHandler(io.Discard, nil)),
		tracer:        tp.Tracer("test"),
		meter:         mp.Meter("test"),
		traceProvider: tp,
		meterProvider: mp,
	}
	obs.initInstruments()

	server := &Server{
		cfg: Config{
			ListenAddr:                 ":8080",
			APIKey:                     "test-api-key",
			StorageURL:                 "https://storage.example.com",
			StagingContainer:           "staging",
			JobContainer:               "jobs",
			GraphBaseURL:               "https://graph.microsoft.com/v1.0",
			VideoIndexerSubscriptionID: "sub-1",
			VideoIndexerResourceGroup:  "rg-1",
			VideoIndexerAccountName:    "acct-1",
			VideoIndexerTimeout:        time.Minute,
		},
		readiness: &fakeReadinessReporter{
			report: readinessReport{
				Status: readinessStatusReady,
				Checks: map[string]string{
					"config":                             readinessStatusReady,
					"storage.staging":                    readinessStatusReady,
					"storage.jobs":                       readinessStatusReady,
					"storage.user_delegation":            readinessStatusReady,
					"video_indexer.account":              readinessStatusReady,
					"video_indexer.account_access_token": readinessStatusReady,
					"agent":                              readinessStatusReady,
					"ffmpeg":                             readinessStatusReady,
					"ffprobe":                            readinessStatusReady,
				},
			},
		},
	}
	readyRR := httptest.NewRecorder()
	server.handleReady(readyRR, httptest.NewRequest(http.MethodGet, "/ready", nil))
	if readyRR.Code != http.StatusOK {
		t.Fatalf("/ready status = %d", readyRR.Code)
	}
	var readyBody struct {
		Status string            `json:"status"`
		Checks map[string]string `json:"checks"`
	}
	if err := json.Unmarshal(readyRR.Body.Bytes(), &readyBody); err != nil {
		t.Fatalf("decode ready body: %v", err)
	}
	if readyBody.Status != "ready" || readyBody.Checks["ffmpeg"] != "ready" || readyBody.Checks["ffprobe"] != "ready" {
		t.Fatalf("unexpected ready body: %#v", readyBody)
	}

	handler := obs.HTTPMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := obs.ContextWithAttrs(r.Context(),
			attribute.String("job_id", "job-1"),
			attribute.String("video_id", "video-1"),
			attribute.String("asset_id", "asset-1"),
			attribute.String("prompt_version", "v2"),
			attribute.String("model_deployment", "gpt-5.4"),
		)
		start := time.Now().Add(-1500 * time.Millisecond)
		ctx, span := obs.StartSpan(ctx, "vi.submit", attribute.String("stage", "vi.submit"))
		obs.FinishSpan(ctx, span, "vi.submit", start, []attribute.KeyValue{
			attribute.String("stage", "vi.submit"),
			attribute.String("job_id", "job-1"),
			attribute.String("video_id", "video-1"),
			attribute.String("asset_id", "asset-1"),
			attribute.String("prompt_version", "v2"),
			attribute.String("model_deployment", "gpt-5.4"),
		}, nil)
		obs.RecordEvidencePacket(ctx, 1234, 5,
			attribute.String("job_id", "job-1"),
			attribute.String("video_id", "video-1"),
			attribute.String("prompt_version", "v2"),
		)
		obs.RecordTimelineClips(ctx, 3,
			attribute.String("job_id", "job-1"),
			attribute.String("video_id", "video-1"),
			attribute.String("prompt_version", "v2"),
		)
		obs.RecordJobResult(ctx, "job-1", "succeeded", 2*time.Second, attribute.String("worker_id", "worker-1"))
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/index-jobs/job-1", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("unexpected response status: %d", rr.Code)
	}

	spans := traceExporter.GetSpans()
	if len(spans) != 2 {
		t.Fatalf("expected 2 spans, got %d", len(spans))
	}
	assertSpan := func(name string, attrs map[string]string) {
		t.Helper()
		for _, span := range spans {
			if span.Name != name {
				continue
			}
			for k, v := range attrs {
				if got, ok := spanAttr(span.Attributes, k); !ok || got != v {
					t.Fatalf("span %s missing %s=%s; attrs=%v", name, k, v, span.Attributes)
				}
			}
			return
		}
		t.Fatalf("span %q not found", name)
	}
	assertSpan("http.server", map[string]string{"http.method": "POST", "http.route": "/api/v1/index-jobs/job-1"})
	assertSpan("vi.submit", map[string]string{"stage": "vi.submit", "job_id": "job-1", "asset_id": "asset-1", "video_id": "video-1", "prompt_version": "v2", "model_deployment": "gpt-5.4"})

	var rm metricdata.ResourceMetrics
	if err := metricReader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect metrics: %v", err)
	}
	metrics := collectMetrics(rm)
	for _, name := range []string{
		"ai_video_indexer_stage_duration_seconds",
		"ai_video_indexer_job_result_total",
		"ai_video_indexer_evidence_packet_bytes",
		"ai_video_indexer_evidence_scene_count",
		"ai_video_indexer_timeline_clip_count",
	} {
		if _, ok := metrics[name]; !ok {
			t.Fatalf("expected metric %q to be recorded", name)
		}
	}
	if !metricHasAttrs(t, metrics["ai_video_indexer_stage_duration_seconds"], map[string]string{"stage": "vi.submit", "job_id": "job-1"}) {
		t.Fatalf("expected a vi.submit stage metric with job_id=job-1")
	}
	if attrs := metricAttrs(t, metrics["ai_video_indexer_job_result_total"]); attrs["result"] != "succeeded" {
		t.Fatalf("unexpected job result attrs: %#v", attrs)
	}
}

func TestReadyFailureIsActionable(t *testing.T) {
	server := &Server{
		cfg: Config{
			ListenAddr:                 ":8080",
			APIKey:                     "test-api-key",
			StorageURL:                 "https://storage.example.com",
			StagingContainer:           "staging",
			JobContainer:               "jobs",
			GraphBaseURL:               "https://graph.microsoft.com/v1.0",
			VideoIndexerSubscriptionID: "sub-1",
			VideoIndexerResourceGroup:  "rg-1",
			VideoIndexerAccountName:    "acct-1",
			VideoIndexerTimeout:        time.Minute,
		},
		readiness: &fakeReadinessReporter{
			report: readinessReport{
				Status: readinessStatusNotReady,
				Checks: map[string]string{"ffmpeg": readinessStatusNotReady},
				Errors: []string{"ffmpeg"},
			},
		},
	}
	rr := httptest.NewRecorder()
	server.handleReady(rr, httptest.NewRequest(http.MethodGet, "/ready", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("unexpected status: %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "ffmpeg") {
		t.Fatalf("unexpected body: %s", rr.Body.String())
	}
}

func collectMetrics(rm metricdata.ResourceMetrics) map[string]metricdata.Metrics {
	out := map[string]metricdata.Metrics{}
	for _, scope := range rm.ScopeMetrics {
		for _, metric := range scope.Metrics {
			out[metric.Name] = metric
		}
	}
	return out
}

func metricAttrs(t *testing.T, metric metricdata.Metrics) map[string]string {
	t.Helper()
	switch data := metric.Data.(type) {
	case metricdata.Sum[int64]:
		if len(data.DataPoints) == 0 {
			t.Fatalf("metric %s had no datapoints", metric.Name)
		}
		return kvsToMap(data.DataPoints[0].Attributes.ToSlice())
	case metricdata.Histogram[float64]:
		if len(data.DataPoints) == 0 {
			t.Fatalf("metric %s had no datapoints", metric.Name)
		}
		return kvsToMap(data.DataPoints[0].Attributes.ToSlice())
	case metricdata.Histogram[int64]:
		if len(data.DataPoints) == 0 {
			t.Fatalf("metric %s had no datapoints", metric.Name)
		}
		return kvsToMap(data.DataPoints[0].Attributes.ToSlice())
	default:
		t.Fatalf("unexpected metric type for %s: %T", metric.Name, metric.Data)
		return nil
	}
}

func metricHasAttrs(t *testing.T, metric metricdata.Metrics, expected map[string]string) bool {
	t.Helper()
	switch data := metric.Data.(type) {
	case metricdata.Sum[int64]:
		for _, point := range data.DataPoints {
			if attrsContain(point.Attributes.ToSlice(), expected) {
				return true
			}
		}
	case metricdata.Histogram[float64]:
		for _, point := range data.DataPoints {
			if attrsContain(point.Attributes.ToSlice(), expected) {
				return true
			}
		}
	case metricdata.Histogram[int64]:
		for _, point := range data.DataPoints {
			if attrsContain(point.Attributes.ToSlice(), expected) {
				return true
			}
		}
	default:
		t.Fatalf("unexpected metric type for %s: %T", metric.Name, metric.Data)
	}
	return false
}

func attrsContain(attrs []attribute.KeyValue, expected map[string]string) bool {
	got := kvsToMap(attrs)
	for key, want := range expected {
		if got[key] != want {
			return false
		}
	}
	return true
}

func spanAttr(attrs []attribute.KeyValue, key string) (string, bool) {
	for _, attr := range attrs {
		if string(attr.Key) == key {
			return attr.Value.AsString(), true
		}
	}
	return "", false
}

func kvsToMap(attrs []attribute.KeyValue) map[string]string {
	out := make(map[string]string, len(attrs))
	for _, attr := range attrs {
		out[string(attr.Key)] = attr.Value.AsString()
	}
	return out
}
