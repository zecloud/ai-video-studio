package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	otlpmetrichttp "go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	otlptracehttp "go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	otelmetric "go.opentelemetry.io/otel/metric"
	otelnoopmetric "go.opentelemetry.io/otel/metric/noop"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

type observabilityContextKey struct{}

type Observability struct {
	logger *slog.Logger

	tracer trace.Tracer
	meter  otelmetric.Meter

	traceProvider traceProvider
	meterProvider meterProvider

	stageDuration       otelmetric.Float64Histogram
	jobResult           otelmetric.Int64Counter
	retryCounter        otelmetric.Int64Counter
	modelUsageCounter   otelmetric.Int64Counter
	evidencePacketBytes otelmetric.Int64Histogram
	evidenceSceneCount  otelmetric.Int64Histogram
	timelineClipCount   otelmetric.Int64Histogram

	shutdown     func(context.Context) error
	shutdownOnce sync.Once
	shutdownErr  error
	mode         string
}

type traceProvider interface {
	Shutdown(context.Context) error
}

type meterProvider interface {
	Shutdown(context.Context) error
}

func newObservability(ctx context.Context, logger *slog.Logger) *Observability {
	if logger == nil {
		logger = slog.New(slog.NewJSONHandler(os.Stdout, nil))
	}
	serviceName := "azure-video-indexer-service"
	serviceVersion := buildServiceVersion()
	base := &Observability{
		logger: logger.With("service", serviceName, "version", serviceVersion),
		tracer: trace.NewNoopTracerProvider().Tracer(serviceName),
		meter:  otelnoopmetric.NewMeterProvider().Meter(serviceName),
		mode:   "disabled",
	}
	base.initInstruments()

	resourceAttrs := []attribute.KeyValue{
		attribute.String("service.name", serviceName),
		attribute.String("service.version", serviceVersion),
	}

	switch {
	case strings.TrimSpace(os.Getenv("APPLICATIONINSIGHTS_CONNECTION_STRING")) != "":
		traceExp, metricExp, err := newAppInsightsOTLPExporters(ctx, os.Getenv("APPLICATIONINSIGHTS_CONNECTION_STRING"))
		if err != nil {
			base.logger.Warn("telemetry disabled", "mode", "applicationinsights", "error", redactURLsInText(err.Error()))
			return base
		}
		tp, mp, shutdown := newTelemetryProviders(ctx, traceExp, metricExp, resourceAttrs)
		base.traceProvider = tp
		base.meterProvider = mp
		base.tracer = tp.Tracer(serviceName)
		base.meter = mp.Meter(serviceName)
		base.shutdown = shutdown
		base.mode = "applicationinsights"
	case hasOTELHTTPEnv():
		traceExp, err := otlptracehttp.New(ctx)
		if err != nil {
			base.logger.Warn("telemetry disabled", "mode", "otlp", "error", redactURLsInText(err.Error()))
			return base
		}
		metricExp, err := otlpmetrichttp.New(ctx)
		if err != nil {
			_ = traceExp.Shutdown(ctx)
			base.logger.Warn("telemetry disabled", "mode", "otlp", "error", redactURLsInText(err.Error()))
			return base
		}
		tp, mp, shutdown := newTelemetryProviders(ctx, traceExp, metricExp, resourceAttrs)
		base.traceProvider = tp
		base.meterProvider = mp
		base.tracer = tp.Tracer(serviceName)
		base.meter = mp.Meter(serviceName)
		base.shutdown = shutdown
		base.mode = "otlp"
	}

	base.initInstruments()
	return base
}

func newTelemetryProviders(ctx context.Context, traceExp sdktrace.SpanExporter, metricExp sdkmetric.Exporter, attrs []attribute.KeyValue) (*sdktrace.TracerProvider, *sdkmetric.MeterProvider, func(context.Context) error) {
	res, err := resource.New(ctx, resource.WithAttributes(attrs...))
	if err != nil {
		res = resource.Empty()
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		sdktrace.WithBatcher(traceExp),
	)
	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExp)),
	)
	shutdown := func(ctx context.Context) error {
		var errs []error
		if mp != nil {
			if err := mp.Shutdown(ctx); err != nil {
				errs = append(errs, err)
			}
		}
		if tp != nil {
			if err := tp.Shutdown(ctx); err != nil {
				errs = append(errs, err)
			}
		}
		if len(errs) == 0 {
			return nil
		}
		return errors.Join(errs...)
	}
	return tp, mp, shutdown
}

func newAppInsightsOTLPExporters(ctx context.Context, connectionString string) (sdktrace.SpanExporter, sdkmetric.Exporter, error) {
	endpoint, headers, err := appInsightsOTLPConfig(connectionString)
	if err != nil {
		return nil, nil, err
	}
	traceExp, err := otlptracehttp.New(ctx,
		otlptracehttp.WithEndpointURL(endpoint),
		otlptracehttp.WithURLPath("/v1/traces"),
		otlptracehttp.WithHeaders(headers),
	)
	if err != nil {
		return nil, nil, err
	}
	metricExp, err := otlpmetrichttp.New(ctx,
		otlpmetrichttp.WithEndpointURL(endpoint),
		otlpmetrichttp.WithURLPath("/v1/metrics"),
		otlpmetrichttp.WithHeaders(headers),
	)
	if err != nil {
		_ = traceExp.Shutdown(ctx)
		return nil, nil, err
	}
	return traceExp, metricExp, nil
}

func appInsightsOTLPConfig(connectionString string) (string, map[string]string, error) {
	values := map[string]string{}
	for _, part := range strings.Split(connectionString, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		key, value, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		values[strings.ToLower(strings.TrimSpace(key))] = strings.TrimSpace(value)
	}
	ikey := values["instrumentationkey"]
	ingestionEndpoint := values["ingestionendpoint"]
	if ikey == "" || ingestionEndpoint == "" {
		return "", nil, fmt.Errorf("application insights connection string must include instrumentationkey and ingestionendpoint")
	}
	parsed, err := url.Parse(ingestionEndpoint)
	if err != nil {
		return "", nil, fmt.Errorf("parsing application insights ingestion endpoint: %w", err)
	}
	host := parsed.Hostname()
	if host == "" {
		return "", nil, fmt.Errorf("application insights ingestion endpoint is invalid")
	}
	if strings.Contains(host, ".in.") {
		host = strings.Replace(host, ".in.", ".dc.", 1)
	} else if !strings.Contains(host, ".dc.") {
		host = strings.Replace(host, "applicationinsights.azure.com", "dc.applicationinsights.azure.com", 1)
	}
	port := parsed.Port()
	endpointURL := parsed.Scheme + "://" + host
	if port != "" {
		endpointURL += ":" + port
	}
	headers := map[string]string{"x-ms-ikey": ikey}
	return endpointURL, headers, nil
}

func hasOTELHTTPEnv() bool {
	keys := []string{
		"OTEL_EXPORTER_OTLP_ENDPOINT",
		"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT",
		"OTEL_EXPORTER_OTLP_METRICS_ENDPOINT",
		"OTEL_EXPORTER_OTLP_HEADERS",
		"OTEL_EXPORTER_OTLP_PROTOCOL",
	}
	for _, key := range keys {
		if strings.TrimSpace(os.Getenv(key)) != "" {
			return true
		}
	}
	return false
}

func buildServiceVersion() string {
	if info, ok := debug.ReadBuildInfo(); ok {
		if info.Main.Version != "" && info.Main.Version != "(devel)" {
			return info.Main.Version
		}
		if info.Main.Path != "" {
			return info.Main.Path
		}
	}
	return "dev"
}

func (o *Observability) Shutdown(ctx context.Context) error {
	if o == nil || o.shutdown == nil {
		return nil
	}
	o.shutdownOnce.Do(func() {
		o.shutdownErr = o.shutdown(ctx)
	})
	return o.shutdownErr
}

func (o *Observability) ContextWithAttrs(ctx context.Context, attrs ...attribute.KeyValue) context.Context {
	if len(attrs) == 0 {
		return ctx
	}
	existing := attrsFromContext(ctx)
	merged := append(existing, attrs...)
	return context.WithValue(ctx, observabilityContextKey{}, merged)
}

func attrsFromContext(ctx context.Context) []attribute.KeyValue {
	if ctx == nil {
		return nil
	}
	value, _ := ctx.Value(observabilityContextKey{}).([]attribute.KeyValue)
	if len(value) == 0 {
		return nil
	}
	out := make([]attribute.KeyValue, len(value))
	copy(out, value)
	return out
}

func withRequestID(ctx context.Context, requestID string) context.Context {
	if requestID == "" {
		return ctx
	}
	return context.WithValue(ctx, requestIDContextKey{}, requestID)
}

func requestIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	requestID, _ := ctx.Value(requestIDContextKey{}).(string)
	return requestID
}

type requestIDContextKey struct{}

func (o *Observability) StartSpan(ctx context.Context, name string, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	if o == nil {
		return ctx, trace.SpanFromContext(ctx)
	}
	merged := append(attrsFromContext(ctx), attrs...)
	return o.tracer.Start(ctx, name, trace.WithAttributes(merged...))
}

func (o *Observability) HTTPMiddleware(next http.Handler) http.Handler {
	if o == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := requestIDFromContext(r.Context())
		if requestID == "" {
			requestID = randomRequestID()
		}
		ctx := o.ContextWithAttrs(r.Context(), attribute.String("request_id", requestID))
		ctx, span := o.StartSpan(ctx, "http.server", attribute.String("stage", "http.server"), attribute.String("http.method", r.Method), attribute.String("http.route", r.URL.Path))
		start := time.Now()
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(recorder, r.WithContext(withRequestID(ctx, requestID)))
		var spanErr error
		if recorder.status >= http.StatusInternalServerError {
			spanErr = fmt.Errorf("http status %d", recorder.status)
		}
		o.FinishSpan(ctx, span, "http.server", start, []attribute.KeyValue{
			attribute.String("stage", "http.server"),
			attribute.String("http.method", r.Method),
			attribute.String("http.route", r.URL.Path),
			attribute.Int("http.status_code", recorder.status),
		}, spanErr)
		o.logger.InfoContext(ctx, "http request",
			"request_id", requestID,
			"method", r.Method,
			"path", r.URL.Path,
			"status", recorder.status,
			"duration_ms", time.Since(start).Milliseconds(),
			"job_id", r.PathValue("jobID"),
		)
	})
}

func (o *Observability) FinishSpan(ctx context.Context, span trace.Span, stage string, start time.Time, attrs []attribute.KeyValue, err error) {
	if o == nil {
		return
	}
	duration := time.Since(start)
	if span != nil {
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, redactURLsInText(err.Error()))
		} else {
			span.SetStatus(codes.Ok, "")
		}
		span.End()
	}
	o.recordStage(ctx, stage, duration, attrs, err)
}

func (o *Observability) recordStage(ctx context.Context, stage string, duration time.Duration, attrs []attribute.KeyValue, err error) {
	if o == nil {
		return
	}
	allAttrs := append([]attribute.KeyValue(nil), attrs...)
	allAttrs = append(allAttrs,
		attribute.String("stage", stage),
		attribute.String("outcome", stageOutcome(err)),
	)
	if o.stageDuration != nil {
		o.stageDuration.Record(ctx, duration.Seconds(), otelmetric.WithAttributeSet(attribute.NewSet(allAttrs...)))
	}
	fields := []any{
		"stage", stage,
		"duration_ms", duration.Milliseconds(),
		"outcome", stageOutcome(err),
	}
	fields = append(fields, attrsToSlog(allAttrs)...)
	if err != nil {
		fields = append(fields, "error", redactURLsInText(err.Error()))
		o.logger.ErrorContext(ctx, "stage failed", fields...)
		return
	}
	o.logger.InfoContext(ctx, "stage completed", fields...)
}

func (o *Observability) RecordJobResult(ctx context.Context, jobID, result string, duration time.Duration, attrs ...attribute.KeyValue) {
	if o == nil {
		return
	}
	allAttrs := append(attrs, attribute.String("result", result))
	if o.jobResult != nil {
		o.jobResult.Add(ctx, 1, otelmetric.WithAttributeSet(attribute.NewSet(allAttrs...)))
	}
	fields := []any{"job_id", jobID, "result", result, "duration_ms", duration.Milliseconds()}
	fields = append(fields, attrsToSlog(allAttrs)...)
	o.logger.InfoContext(ctx, "job result", fields...)
}

func (o *Observability) RecordRetry(ctx context.Context, stage string, statusCode int, attrs ...attribute.KeyValue) {
	if o == nil {
		return
	}
	allAttrs := append(attrs,
		attribute.String("stage", stage),
		attribute.Int("status_code", statusCode),
	)
	if o.retryCounter != nil {
		o.retryCounter.Add(ctx, 1, otelmetric.WithAttributeSet(attribute.NewSet(allAttrs...)))
	}
}

func (o *Observability) RecordModelUsage(ctx context.Context, modelDeployment string, usage modelUsage, attrs ...attribute.KeyValue) {
	if o == nil {
		return
	}
	records := []struct {
		kind  string
		count int64
	}{
		{"input", usage.Input},
		{"output", usage.Output},
		{"total", usage.Total},
		{"reasoning", usage.Reasoning},
	}
	for _, record := range records {
		if record.count <= 0 {
			continue
		}
		allAttrs := append(attrs,
			attribute.String("model_deployment", modelDeployment),
			attribute.String("kind", record.kind),
		)
		if o.modelUsageCounter != nil {
			o.modelUsageCounter.Add(ctx, record.count, otelmetric.WithAttributeSet(attribute.NewSet(allAttrs...)))
		}
	}
}

func (o *Observability) RecordEvidencePacket(ctx context.Context, packetSizeBytes int, sceneCount int, attrs ...attribute.KeyValue) {
	if o == nil {
		return
	}
	allAttrs := append(attrs, attribute.String("stage", "agent.run"))
	if o.evidencePacketBytes != nil {
		o.evidencePacketBytes.Record(ctx, int64(packetSizeBytes), otelmetric.WithAttributeSet(attribute.NewSet(allAttrs...)))
	}
	if o.evidenceSceneCount != nil {
		o.evidenceSceneCount.Record(ctx, int64(sceneCount), otelmetric.WithAttributeSet(attribute.NewSet(allAttrs...)))
	}
}

func (o *Observability) RecordTimelineClips(ctx context.Context, clipCount int, attrs ...attribute.KeyValue) {
	if o == nil {
		return
	}
	allAttrs := append(attrs, attribute.String("stage", "timeline.build"))
	if o.timelineClipCount != nil {
		o.timelineClipCount.Record(ctx, int64(clipCount), otelmetric.WithAttributeSet(attribute.NewSet(allAttrs...)))
	}
}

func (o *Observability) initInstruments() {
	if o == nil || o.meter == nil {
		return
	}
	stageDuration, _ := o.meter.Float64Histogram("ai_video_indexer_stage_duration_seconds")
	jobResult, _ := o.meter.Int64Counter("ai_video_indexer_job_result_total")
	retryCounter, _ := o.meter.Int64Counter("ai_video_indexer_retry_total")
	modelUsage, _ := o.meter.Int64Counter("ai_video_indexer_model_usage_tokens_total")
	evidencePacketBytes, _ := o.meter.Int64Histogram("ai_video_indexer_evidence_packet_bytes")
	evidenceSceneCount, _ := o.meter.Int64Histogram("ai_video_indexer_evidence_scene_count")
	timelineClipCount, _ := o.meter.Int64Histogram("ai_video_indexer_timeline_clip_count")
	o.stageDuration = stageDuration
	o.jobResult = jobResult
	o.retryCounter = retryCounter
	o.modelUsageCounter = modelUsage
	o.evidencePacketBytes = evidencePacketBytes
	o.evidenceSceneCount = evidenceSceneCount
	o.timelineClipCount = timelineClipCount
}

func attrsToSlog(attrs []attribute.KeyValue) []any {
	if len(attrs) == 0 {
		return nil
	}
	out := make([]any, 0, len(attrs)*2)
	for _, attr := range attrs {
		key := string(attr.Key)
		if key == "" {
			continue
		}
		out = append(out, key, redactURLsInText(fmt.Sprint(attr.Value.AsInterface())))
	}
	return out
}

func stageOutcome(err error) string {
	if err != nil {
		return "error"
	}
	return "ok"
}

func randomRequestID() string {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "req-unknown"
	}
	return "req-" + hex.EncodeToString(buf[:])
}

type modelUsage struct {
	Input     int64
	Output    int64
	Total     int64
	Reasoning int64
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}
