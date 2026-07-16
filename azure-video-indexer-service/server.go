package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"sync"

	"github.com/zecloud/ai-video-studio/internal/videoindexerstudio"
)

type Server struct {
	cfg                       Config
	jobs                      JobService
	renderJobs                RenderJobService
	obs                       *Observability
	blobSvc                   *AzureBlobService
	renderOutputs             RenderOutputStreamer
	videoIndexer              videoIndexerReadiness
	planner                   EditPlanner
	narrativeRanker           NarrativeRanker
	narrativeIntentClassifier NarrativeIntentClassifier
	narrativeSegmentPlanner   NarrativeSegmentPlanner
	lookPath                  func(string) (string, error)
	readiness                 readinessReporter
	readinessOnce             sync.Once
}

type RenderOutputStreamer interface {
	OpenDownload(context.Context, StagedAsset) (BlobDownload, error)
}

func (s *Server) SetNarrativeRanker(ranker NarrativeRanker) { s.narrativeRanker = ranker }
func (s *Server) SetNarrativeIntentClassifier(classifier NarrativeIntentClassifier) {
	s.narrativeIntentClassifier = classifier
}
func (s *Server) SetNarrativeSegmentPlanner(planner NarrativeSegmentPlanner) {
	s.narrativeSegmentPlanner = planner
}
func (s *Server) SetRenderJobs(jobs RenderJobService)                  { s.renderJobs = jobs }
func (s *Server) SetRenderOutputStreamer(outputs RenderOutputStreamer) { s.renderOutputs = outputs }

func NewServer(cfg Config, jobs JobService) *Server {
	return &Server{
		cfg:      cfg.Normalize(),
		jobs:     jobs,
		lookPath: exec.LookPath,
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("GET /ready", s.handleReady)
	mux.Handle("POST /api/v1/index-jobs", s.requireAPIKey(http.HandlerFunc(s.handleCreateJob)))
	mux.Handle("GET /api/v1/index-jobs", s.requireAPIKey(http.HandlerFunc(s.handleListJobs)))
	mux.Handle("GET /api/v1/index-jobs/{jobID}", s.requireAPIKey(http.HandlerFunc(s.handleGetJob)))
	mux.Handle("POST /api/v1/index-jobs/{jobID}/cancel", s.requireAPIKey(http.HandlerFunc(s.handleCancelJob)))
	mux.Handle("POST /api/v1/render-jobs", s.requireAPIKey(http.HandlerFunc(s.handleCreateRenderJob)))
	mux.Handle("GET /api/v1/render-jobs", s.requireAPIKey(http.HandlerFunc(s.handleListRenderJobs)))
	mux.Handle("GET /api/v1/render-jobs/{jobID}", s.requireAPIKey(http.HandlerFunc(s.handleGetRenderJob)))
	mux.Handle("GET /api/v1/render-jobs/{jobID}/output", s.requireAPIKey(http.HandlerFunc(s.handleGetRenderOutput)))
	mux.Handle("POST /api/v1/render-jobs/{jobID}/cancel", s.requireAPIKey(http.HandlerFunc(s.handleCancelRenderJob)))
	mux.Handle("POST /api/v1/jobs", s.requireAPIKey(http.HandlerFunc(s.handleCreateJob)))
	mux.Handle("POST /api/v1/narrative-rankings", s.requireAPIKey(http.HandlerFunc(s.handleNarrativeRanking)))
	mux.Handle("POST /api/v1/narrative-intent-classifications", s.requireAPIKey(http.HandlerFunc(s.handleNarrativeIntentClassification)))
	mux.Handle("POST /api/v1/narrative-segment-plans", s.requireAPIKey(http.HandlerFunc(s.handleNarrativeSegmentPlanning)))
	mux.Handle("GET /api/v1/jobs", s.requireAPIKey(http.HandlerFunc(s.handleListJobs)))
	mux.Handle("GET /api/v1/jobs/{jobID}", s.requireAPIKey(http.HandlerFunc(s.handleGetJob)))
	mux.Handle("POST /api/v1/jobs/{jobID}/cancel", s.requireAPIKey(http.HandlerFunc(s.handleCancelJob)))
	if s.obs != nil {
		return s.obs.HTTPMiddleware(mux)
	}

	return mux
}

func (s *Server) handleNarrativeSegmentPlanning(w http.ResponseWriter, r *http.Request) {
	if s.narrativeSegmentPlanner == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "narrative segment planner is not configured", "narrative_segment_planner_unavailable", true)
		return
	}
	var req videoindexerstudio.NarrativeSegmentPlanningRequest
	if err := decodeStrictJSON(r.Body, &req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid JSON request body", "bad_request", false)
		return
	}
	if err := req.Validate(); err != nil {
		writeAPIError(w, http.StatusUnprocessableEntity, "invalid narrative segment planning request", "narrative_segment_planning_invalid", false)
		return
	}
	response, err := s.narrativeSegmentPlanner.Plan(r.Context(), req)
	if err == nil {
		err = response.Validate()
	}
	if err != nil {
		status, code, retryable := http.StatusBadGateway, "narrative_segment_planning_failed", true
		if errors.Is(err, context.DeadlineExceeded) {
			status, code = http.StatusGatewayTimeout, "narrative_segment_planning_timeout"
		} else if strings.Contains(err.Error(), "invalid") {
			status, code, retryable = http.StatusUnprocessableEntity, "narrative_segment_planning_invalid", false
		}
		writeAPIError(w, status, "narrative segment planning failed", code, retryable)
		return
	}
	writeJSON(w, http.StatusOK, response)
}
func (s *Server) handleNarrativeIntentClassification(w http.ResponseWriter, r *http.Request) {
	if s.narrativeIntentClassifier == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "narrative intent classifier is not configured", "narrative_intent_classifier_unavailable", true)
		return
	}
	var req videoindexerstudio.NarrativeIntentClassificationRequest
	if err := decodeStrictJSON(r.Body, &req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid JSON request body", "bad_request", false)
		return
	}
	if err := req.Validate(); err != nil {
		writeAPIError(w, http.StatusUnprocessableEntity, "invalid narrative intent classification request", "narrative_intent_classification_invalid", false)
		return
	}
	response, err := s.narrativeIntentClassifier.Classify(r.Context(), req)
	if err == nil {
		err = response.Validate()
		if err != nil {
			err = fmt.Errorf("invalid classifier response: %w", err)
		}
	}
	if err != nil {
		status, code, retryable := http.StatusBadGateway, "narrative_intent_classification_failed", true
		if errors.Is(err, context.DeadlineExceeded) {
			status, code = http.StatusGatewayTimeout, "narrative_intent_classification_timeout"
		} else if strings.Contains(err.Error(), "invalid classifier response") {
			status, code, retryable = http.StatusBadGateway, "narrative_intent_classification_invalid_response", false
		}
		writeAPIError(w, status, "narrative intent classification failed", code, retryable)
		return
	}
	writeJSON(w, http.StatusOK, response)
}
func (s *Server) handleNarrativeRanking(w http.ResponseWriter, r *http.Request) {
	if s.narrativeRanker == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "narrative ranker is not configured", "narrative_ranker_unavailable", true)
		return
	}
	var req videoindexerstudio.NarrativeRankingRequest
	if err := decodeStrictJSON(r.Body, &req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid JSON request body", "bad_request", false)
		return
	}
	if err := req.Validate(); err != nil {
		writeAPIError(w, http.StatusUnprocessableEntity, "invalid narrative ranking request", "narrative_ranking_invalid", false)
		return
	}
	response, err := s.narrativeRanker.Rank(r.Context(), req)
	if err != nil {
		status, code, retryable := http.StatusBadGateway, "narrative_ranking_failed", false
		if errors.Is(err, context.DeadlineExceeded) {
			status, code, retryable = http.StatusGatewayTimeout, "narrative_ranking_timeout", true
		}
		if strings.Contains(err.Error(), "invalid") || strings.Contains(err.Error(), "limit") || strings.Contains(err.Error(), "unique") {
			status, code = http.StatusUnprocessableEntity, "narrative_ranking_invalid"
		}
		writeAPIError(w, status, "narrative ranking failed", code, retryable)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) handleCreateRenderJob(w http.ResponseWriter, r *http.Request) {
	if s.renderJobs == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "render job service is not configured", "service_unavailable", true)
		return
	}
	var req CreateRenderJobRequest
	if err := decodeStrictJSON(r.Body, &req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid JSON request body", "bad_request", false)
		return
	}
	job, err := s.renderJobs.CreateRenderJob(r.Context(), req)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	w.Header().Set("Location", "/api/v1/render-jobs/"+job.ID)
	writeJSON(w, http.StatusAccepted, RenderJobResponse{Job: job})
}

func (s *Server) handleListRenderJobs(w http.ResponseWriter, r *http.Request) {
	if s.renderJobs == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "render job service is not configured", "service_unavailable", true)
		return
	}
	var status RenderJobStatus
	if raw := strings.TrimSpace(r.URL.Query().Get("status")); raw != "" {
		status = RenderJobStatus(strings.ToLower(raw))
		if !status.Valid() {
			writeAPIError(w, http.StatusBadRequest, "invalid render job status", "validation_failed", false)
			return
		}
	}
	jobs, err := s.renderJobs.ListRenderJobs(r.Context(), status)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, RenderJobListResponse{Jobs: jobs})
}

func (s *Server) handleGetRenderJob(w http.ResponseWriter, r *http.Request) {
	if s.renderJobs == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "render job service is not configured", "service_unavailable", true)
		return
	}
	id := strings.TrimSpace(r.PathValue("jobID"))
	if err := validateID(id, "jobID"); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error(), "validation_failed", false)
		return
	}
	job, err := s.renderJobs.GetRenderJob(r.Context(), id)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, RenderJobResponse{Job: job})
}

func (s *Server) handleGetRenderOutput(w http.ResponseWriter, r *http.Request) {
	if s.renderJobs == nil || s.renderOutputs == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "render output service is not configured", "service_unavailable", true)
		return
	}
	id := strings.TrimSpace(r.PathValue("jobID"))
	if err := validateID(id, "jobID"); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error(), "validation_failed", false)
		return
	}
	job, err := s.renderJobs.GetRenderJob(r.Context(), id)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	if job.Status != RenderJobStatusSucceeded {
		writeAPIError(w, http.StatusConflict, "render output is not ready", "render_output_not_ready", false)
		return
	}
	if !validRenderOutputIdentity(job) || job.Output.Container != s.cfg.StagingContainer {
		writeAPIError(w, http.StatusInternalServerError, "render output identity is invalid", "render_output_identity_invalid", false)
		return
	}
	download, err := s.renderOutputs.OpenDownload(r.Context(), StagedAsset{Container: job.Output.Container, BlobName: job.Output.BlobName})
	if err != nil {
		writeServiceError(w, err)
		return
	}
	defer func() {
		if closeErr := download.Body.Close(); closeErr != nil && s.obs != nil {
			s.obs.logger.WarnContext(r.Context(), "closing render output stream", "job_id", job.ID, "error", redactURLsInText(closeErr.Error()))
		}
	}()
	if job.Output.Size > 0 && download.ContentLength > 0 && job.Output.Size != download.ContentLength {
		writeAPIError(w, http.StatusBadGateway, "render output size does not match durable metadata", "render_output_size_mismatch", true)
		return
	}
	contentType := strings.TrimSpace(download.ContentType)
	if contentType == "" {
		contentType = strings.TrimSpace(job.Output.MediaType)
	}
	if contentType == "" {
		contentType = "video/mp4"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": job.OutputName}))
	w.Header().Set("Cache-Control", "no-store")
	if download.ContentLength > 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(download.ContentLength, 10))
	}
	w.WriteHeader(http.StatusOK)
	if _, copyErr := io.Copy(w, download.Body); copyErr != nil && s.obs != nil {
		s.obs.logger.WarnContext(r.Context(), "streaming render output", "job_id", job.ID, "error", redactURLsInText(copyErr.Error()))
	}
}
func (s *Server) handleCancelRenderJob(w http.ResponseWriter, r *http.Request) {
	if s.renderJobs == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "render job service is not configured", "service_unavailable", true)
		return
	}
	id := strings.TrimSpace(r.PathValue("jobID"))
	if err := validateID(id, "jobID"); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error(), "validation_failed", false)
		return
	}
	job, err := s.renderJobs.CancelRenderJob(r.Context(), id)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, RenderJobResponse{Job: job})
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	report := s.readinessReport(r.Context())
	status := http.StatusOK
	if report.Status != readinessStatusReady {
		status = http.StatusServiceUnavailable
	}
	writeJSON(w, status, report)
}

func (s *Server) handleCreateJob(w http.ResponseWriter, r *http.Request) {
	if s.jobs == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "job service is not configured", "service_unavailable", true)
		return
	}
	var req CreateIndexJobRequest
	if err := decodeStrictJSON(r.Body, &req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid JSON request body", "bad_request", false)
		return
	}
	job, err := s.jobs.CreateJob(r.Context(), req)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	if s.obs != nil {
		s.obs.logger.InfoContext(r.Context(), "job created", "request_id", requestIDFromContext(r.Context()), "job_id", job.ID, "status", job.Status)
	}
	w.Header().Set("Location", "/api/v1/index-jobs/"+job.ID)
	writeJSON(w, http.StatusAccepted, JobResponse{Job: job})
}

func (s *Server) handleListJobs(w http.ResponseWriter, r *http.Request) {
	if s.jobs == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "job service is not configured", "service_unavailable", true)
		return
	}
	filter := strings.TrimSpace(r.URL.Query().Get("status"))
	var status JobStatus
	if filter != "" {
		parsed, err := parseJobStatus(filter)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, err.Error(), "validation_failed", false)
			return
		}
		status = parsed
	}
	jobs, err := s.jobs.ListJobs(r.Context(), status)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, JobListResponse{Jobs: jobs})
}

func (s *Server) handleGetJob(w http.ResponseWriter, r *http.Request) {
	if s.jobs == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "job service is not configured", "service_unavailable", true)
		return
	}
	jobID := strings.TrimSpace(r.PathValue("jobID"))
	if err := validateID(jobID, "jobID"); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error(), "validation_failed", false)
		return
	}
	job, err := s.jobs.GetJob(r.Context(), jobID)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	if s.obs != nil {
		s.obs.logger.InfoContext(r.Context(), "job fetched", "request_id", requestIDFromContext(r.Context()), "job_id", jobID, "status", job.Status)
	}
	writeJSON(w, http.StatusOK, JobResponse{Job: job})
}

func (s *Server) handleCancelJob(w http.ResponseWriter, r *http.Request) {
	if s.jobs == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "job service is not configured", "service_unavailable", true)
		return
	}
	jobID := strings.TrimSpace(r.PathValue("jobID"))
	if err := validateID(jobID, "jobID"); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error(), "validation_failed", false)
		return
	}
	job, err := s.jobs.CancelJob(r.Context(), jobID)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	if s.obs != nil {
		s.obs.logger.InfoContext(r.Context(), "job canceled", "request_id", requestIDFromContext(r.Context()), "job_id", jobID, "status", job.Status)
	}
	writeJSON(w, http.StatusOK, JobResponse{Job: job})
}

func (s *Server) requireAPIKey(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := extractToken(r)
		if !ok {
			writeAPIError(w, http.StatusUnauthorized, "missing or malformed Authorization header", "unauthorized", false)
			return
		}
		if subtle.ConstantTimeCompare([]byte(token), []byte(s.cfg.APIKey)) != 1 {
			writeAPIError(w, http.StatusUnauthorized, "invalid API key", "unauthorized", false)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) readinessReport(ctx context.Context) readinessReport {
	if s == nil {
		return readinessReport{
			Status: readinessStatusNotReady,
			Checks: map[string]string{"server": readinessStatusNotReady},
			Errors: []string{"server"},
		}
	}
	s.readinessOnce.Do(func() {
		if s.readiness == nil {
			s.readiness = newDefaultReadinessChecker(s.cfg, s.blobSvc, s.videoIndexer, s.planner, s.lookPath)
		}
	})
	if s.readiness == nil {
		return readinessReport{
			Status: readinessStatusNotReady,
			Checks: map[string]string{"readiness": readinessStatusNotReady},
			Errors: []string{"readiness"},
		}
	}
	report := s.readiness.Check(ctx)
	if report.Checks == nil {
		report.Checks = map[string]string{}
	}
	if report.Status == "" {
		report.Status = readinessStatusNotReady
	}
	return report
}

func extractToken(r *http.Request) (string, bool) {
	if value := strings.TrimSpace(r.Header.Get("Authorization")); value != "" {
		const prefix = "Bearer "
		if strings.HasPrefix(value, prefix) {
			token := strings.TrimSpace(strings.TrimPrefix(value, prefix))
			if token != "" {
				return token, true
			}
		}
	}
	if value := strings.TrimSpace(r.Header.Get("X-API-Key")); value != "" {
		return value, true
	}
	return "", false
}

func parseJobStatus(raw string) (JobStatus, error) {
	status := JobStatus(strings.ToLower(strings.TrimSpace(raw)))
	if !status.Valid() {
		return "", errors.New("invalid job status")
	}
	return status, nil
}

func writeServiceError(w http.ResponseWriter, err error) {
	apiErr, status := toAPIError(err)
	writeAPIError(w, status, apiErr.Message, apiErr.Code, apiErr.Retryable)
}

func writeAPIError(w http.ResponseWriter, status int, message, code string, retryable bool) {
	writeJSON(w, status, APIErrorResponse{Code: code, Message: redactURLsInText(message), Retryable: retryable})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func decodeStrictJSON(r io.Reader, dst any) error {
	decoder := json.NewDecoder(io.LimitReader(r, maxRequestBodyBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return errors.New("unexpected trailing data")
		}
		return err
	}
	return nil
}

func statusText(status int) string {
	switch {
	case status >= 200 && status < 300:
		return "ready"
	case status == http.StatusServiceUnavailable:
		return "not_ready"
	default:
		return fmt.Sprintf("status_%d", status)
	}
}
