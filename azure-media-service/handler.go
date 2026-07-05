package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// Server bundles the dependencies HTTP handlers need.
type Server struct {
	cfg      *Config
	blobs    *BlobStore
	oneDrive *OneDriveDownloader
	logger   *slog.Logger
}

// NewServer wires together configuration and clients into a Server ready to
// register HTTP routes.
func NewServer(cfg *Config, blobs *BlobStore, oneDrive *OneDriveDownloader, logger *slog.Logger) *Server {
	return &Server{cfg: cfg, blobs: blobs, oneDrive: oneDrive, logger: logger}
}

// apiError is the JSON shape returned for any failed request. It never
// includes stack traces or internal implementation details.
type apiError struct {
	Error string `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, apiError{Error: message})
}

// handleHealth reports basic liveness. No auth is required so orchestrators
// (Container Apps health probes) can call it freely.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// copyRequest is the payload for POST /api/v1/copy.
type copyRequest struct {
	OneDriveItemID string `json:"oneDriveItemID"`
	OneDriveToken  string `json:"oneDriveToken"`
	BlobName       string `json:"blobName"`
	BlobContainer  string `json:"blobContainer"`
}

type copyResponse struct {
	BlobURL string `json:"blobUrl"`
	SASURL  string `json:"sasUrl"`
}

// handleCopy streams a OneDrive drive item straight into Azure Blob Storage,
// never buffering the full file to local disk, then returns a canonical
// blob URL plus a short-lived read SAS URL for downstream consumers such as
// Azure Content Understanding.
func (s *Server) handleCopy(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req copyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	if req.OneDriveItemID == "" || req.OneDriveToken == "" || req.BlobName == "" {
		writeError(w, http.StatusBadRequest, "oneDriveItemID, oneDriveToken and blobName are required")
		return
	}

	body, _, err := s.oneDrive.DownloadItem(ctx, req.OneDriveItemID, req.OneDriveToken)
	if err != nil {
		s.logger.Error("OneDrive download failed", "error", err, "itemID", req.OneDriveItemID)
		writeError(w, http.StatusBadGateway, "failed to download source item from OneDrive")
		return
	}
	defer body.Close()

	if err := s.blobs.UploadStream(ctx, req.BlobContainer, req.BlobName, body); err != nil {
		s.logger.Error("blob upload failed", "error", err, "blobName", req.BlobName)
		writeError(w, http.StatusInternalServerError, "failed to upload media to blob storage")
		return
	}

	sasURL, err := s.blobs.GenerateSASURL(ctx, req.BlobContainer, req.BlobName)
	if err != nil {
		s.logger.Error("SAS generation failed", "error", err, "blobName", req.BlobName)
		writeError(w, http.StatusInternalServerError, "failed to generate SAS URL")
		return
	}

	writeJSON(w, http.StatusOK, copyResponse{
		BlobURL: s.blobs.BlobURL(req.BlobContainer, req.BlobName),
		SASURL:  sasURL,
	})
}

// deleteRequest is the payload for POST /api/v1/delete.
type deleteRequest struct {
	BlobName      string `json:"blobName"`
	BlobContainer string `json:"blobContainer"`
}

// handleDelete removes a previously staged blob.
func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req deleteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	if req.BlobName == "" {
		writeError(w, http.StatusBadRequest, "blobName is required")
		return
	}

	if err := s.blobs.DeleteBlob(ctx, req.BlobContainer, req.BlobName); err != nil {
		s.logger.Error("blob delete failed", "error", err, "blobName", req.BlobName)
		if isBlobNotFound(err) {
			writeError(w, http.StatusNotFound, "blob not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to delete blob")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// isBlobNotFound performs a best-effort check for "not found" style errors
// without depending on internal SDK error types leaking into the API
// surface.
func isBlobNotFound(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "BlobNotFound") || strings.Contains(msg, "404")
}

// ---- Analysis pipeline types ----

// analyzeRequest is the payload for POST /api/v1/analyze.
type analyzeRequest struct {
	OneDriveItemID string `json:"oneDriveItemID"`
	OneDriveToken  string `json:"oneDriveToken"`
	AssetID        string `json:"assetID"`
	AssetName      string `json:"assetName"`
}

// analyzeResponse is returned by POST /api/v1/analyze.
type analyzeResponse struct {
	JobID       string             `json:"jobId"`
	Status      string             `json:"status"`
	Scenes      []analysisScene    `json:"scenes"`
	Transcript  []analysisTranscript `json:"transcript"`
	Highlights  []analysisHighlight  `json:"highlights"`
	Suggestions []analysisSuggestion `json:"suggestions"`
}

type analysisScene struct {
	ID        string   `json:"id"`
	StartMS   int64    `json:"startMs"`
	EndMS     int64    `json:"endMs"`
	Labels    []string `json:"labels"`
	Summary   string   `json:"summary,omitempty"`
	Highlight bool     `json:"highlight"`
}

type analysisTranscript struct {
	StartMS int64   `json:"startMs"`
	EndMS   int64   `json:"endMs"`
	Text    string  `json:"text"`
	Speaker string  `json:"speaker,omitempty"`
	Score   float64 `json:"score,omitempty"`
}

type analysisHighlight struct {
	ID      string  `json:"id"`
	StartMS int64   `json:"startMs"`
	EndMS   int64   `json:"endMs"`
	Reason  string  `json:"reason"`
	Score   float64 `json:"score"`
}

type analysisSuggestion struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	SceneIDs    []string `json:"sceneIds"`
}

const mediaStagingContainer = "media-staging"

// handleAnalyze runs the full analysis pipeline server-side:
// OneDrive download → Blob upload → SAS → CU submit → CU poll → blob cleanup.
func (s *Server) handleAnalyze(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	if !s.cfg.CUConfigured() {
		writeError(w, http.StatusServiceUnavailable, "Content Understanding is not configured on this service")
		return
	}

	var req analyzeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.OneDriveItemID == "" || req.OneDriveToken == "" || req.AssetID == "" || req.AssetName == "" {
		writeError(w, http.StatusBadRequest, "oneDriveItemID, oneDriveToken, assetID and assetName are required")
		return
	}

	container := mediaStagingContainer
	blobName := fmt.Sprintf("analysis/%s/%s", req.AssetID, req.AssetName)

	// Step 1: Download from OneDrive (streaming, no disk).
	body, _, err := s.oneDrive.DownloadItem(ctx, req.OneDriveItemID, req.OneDriveToken)
	if err != nil {
		s.logger.Error("analyze: OneDrive download failed", "error", err, "assetID", req.AssetID)
		writeError(w, http.StatusBadGateway, "failed to download source item from OneDrive")
		return
	}
	defer body.Close()

	// Step 2: Upload to Azure Blob.
	if err := s.blobs.UploadStream(ctx, container, blobName, body); err != nil {
		s.logger.Error("analyze: blob upload failed", "error", err, "blobName", blobName)
		writeError(w, http.StatusInternalServerError, "failed to upload media to blob storage")
		return
	}

	// Step 3: Generate SAS URL for CU.
	sasURL, err := s.blobs.GenerateSASURL(ctx, container, blobName)
	if err != nil {
		s.logger.Error("analyze: SAS generation failed", "error", err, "blobName", blobName)
		writeError(w, http.StatusInternalServerError, "failed to generate SAS URL")
		return
	}

	// Always attempt cleanup on exit.
	defer func() {
		if delErr := s.blobs.DeleteBlob(context.Background(), container, blobName); delErr != nil {
			s.logger.Error("analyze: blob cleanup failed", "error", delErr, "blobName", blobName)
		}
	}()

	// Step 4: Submit to Content Understanding.
	jobID, opLocation, err := s.submitToCU(ctx, sasURL)
	if err != nil {
		s.logger.Error("analyze: CU submit failed", "error", err, "assetID", req.AssetID)
		writeError(w, http.StatusBadGateway, "failed to submit to Content Understanding")
		return
	}

	// Step 5: Poll CU until terminal state.
	result, err := s.pollCU(ctx, opLocation)
	if err != nil {
		s.logger.Error("analyze: CU poll failed", "error", err, "jobID", jobID)
		writeError(w, http.StatusBadGateway, "Content Understanding analysis failed")
		return
	}

	writeJSON(w, http.StatusOK, analyzeResponse{
		JobID:       jobID,
		Status:      result.Status,
		Scenes:      result.Scenes,
		Transcript:  result.Transcript,
		Highlights:  result.Highlights,
		Suggestions: result.Suggestions,
	})
}

// submitToCU sends a SAS URL to Azure Content Understanding and returns the
// job ID and Operation-Location polling URL.
func (s *Server) submitToCU(ctx context.Context, sasURL string) (jobID, opLocation string, err error) {
	cuURL := fmt.Sprintf("%s/contentunderstanding/analyzers/prebuilt-videoSearch:analyze?api-version=2025-11-01",
		strings.TrimRight(s.cfg.CUEndpoint, "/"))

	reqBody, _ := json.Marshal(map[string]string{"url": sasURL})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cuURL, bytes.NewReader(reqBody))
	if err != nil {
		return "", "", fmt.Errorf("building CU submit request: %w", err)
	}
	req.Header.Set("Ocp-Apim-Subscription-Key", s.cfg.CUAPIKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("CU submit request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", "", fmt.Errorf("CU submit returned %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var submitResp struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&submitResp); err != nil {
		return "", "", fmt.Errorf("decoding CU submit response: %w", err)
	}

	opLocation = resp.Header.Get("Operation-Location")
	if opLocation == "" && submitResp.ID != "" {
		opLocation = fmt.Sprintf("%s/contentunderstanding/analyzerResults/%s?api-version=2025-11-01",
			strings.TrimRight(s.cfg.CUEndpoint, "/"), submitResp.ID)
	}
	if opLocation == "" {
		return "", "", fmt.Errorf("CU submit: no Operation-Location returned")
	}

	return submitResp.ID, opLocation, nil
}

// cuOperationResponse matches the Azure CU long-running operation envelope.
type cuOperationResponse struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Result struct {
		AnalyzerID string          `json:"analyzerId"`
		Contents   []cuContentItem `json:"contents"`
	} `json:"result"`
}

type cuContentItem struct {
	Markdown string                       `json:"markdown"`
	Fields   map[string]json.RawMessage   `json:"fields"`
}

// pollCU polls the Operation-Location URL with 3 retries and exponential
// backoff (2s, 4s, 6s) until CU returns a terminal status.
func (s *Server) pollCU(ctx context.Context, opLocation string) (analyzeResponse, error) {
	const maxRetries = 3

	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			backoff := time.Duration(attempt) * 2 * time.Second
			timer := time.NewTimer(backoff)
			select {
			case <-ctx.Done():
				timer.Stop()
				return analyzeResponse{}, ctx.Err()
			case <-timer.C:
			}
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, opLocation, nil)
		if err != nil {
			return analyzeResponse{}, err
		}
		req.Header.Set("Ocp-Apim-Subscription-Key", s.cfg.CUAPIKey)
		req.Header.Set("Accept", "application/json")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			continue
		}

		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			continue
		}

		var op cuOperationResponse
		if err := json.NewDecoder(resp.Body).Decode(&op); err != nil {
			resp.Body.Close()
			continue
		}
		resp.Body.Close()

		switch strings.ToLower(op.Status) {
		case "succeeded":
			return normalizeCUResult(op), nil
		case "failed", "canceled", "cancelled":
			return analyzeResponse{Status: op.Status}, nil
		default:
			// Still running — retry.
		}
	}

	return analyzeResponse{}, fmt.Errorf("CU polling exhausted after %d attempts", maxRetries)
}

// normalizeCUResult converts a raw CU operation response into a normalized
// analyzeResponse. Scenes, transcripts, and highlights are parsed from the
// fields map when present; markdown content becomes edit suggestions.
func normalizeCUResult(op cuOperationResponse) analyzeResponse {
	result := analyzeResponse{
		JobID:       op.ID,
		Status:      op.Status,
		Scenes:      []analysisScene{},
		Transcript:  []analysisTranscript{},
		Highlights:  []analysisHighlight{},
		Suggestions: []analysisSuggestion{},
	}

	for i, content := range op.Result.Contents {
		if strings.TrimSpace(content.Markdown) != "" {
			result.Suggestions = append(result.Suggestions, analysisSuggestion{
				ID:          fmt.Sprintf("summary-%d", i+1),
				Title:       "Content Understanding summary",
				Description: content.Markdown,
			})
		}
		// Future: parse scenes, transcripts, highlights from content.Fields
	}

	return result
}
