package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"sync"
)

type Server struct {
	cfg           Config
	jobs          JobService
	obs           *Observability
	blobSvc       *AzureBlobService
	videoIndexer  videoIndexerReadiness
	planner       EditPlanner
	lookPath      func(string) (string, error)
	readiness     readinessReporter
	readinessOnce sync.Once
}

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
	mux.Handle("POST /api/v1/jobs", s.requireAPIKey(http.HandlerFunc(s.handleCreateJob)))
	mux.Handle("GET /api/v1/jobs", s.requireAPIKey(http.HandlerFunc(s.handleListJobs)))
	mux.Handle("GET /api/v1/jobs/{jobID}", s.requireAPIKey(http.HandlerFunc(s.handleGetJob)))
	mux.Handle("POST /api/v1/jobs/{jobID}/cancel", s.requireAPIKey(http.HandlerFunc(s.handleCancelJob)))
	if s.obs != nil {
		return s.obs.HTTPMiddleware(mux)
	}
	return mux
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
