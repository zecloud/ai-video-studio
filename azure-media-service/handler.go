package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
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
	ffmpeg := "absent"
	if _, err := exec.LookPath("ffmpeg"); err == nil {
		ffmpeg = "available"
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"status": "ok",
		"ffmpeg": ffmpeg,
	})
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

// ---- Render pipeline types ----

// clipPath tracks a downloaded clip local file during render prep.
type clipPath struct {
	ID    string
	Path  string
	InMS  int64
	OutMS int64
}

// renderClip mirrors the desktop's mediaservice.RenderClip.
type renderClip struct {
	ID    string `json:"id"`
	Input string `json:"input"` // OneDrive item ID of the source asset
	InMS  int64  `json:"inMs"`
	OutMS int64  `json:"outMs"`
	Muted bool   `json:"muted,omitempty"`
}

// renderTransition mirrors the desktop's mediaservice.RenderTransition.
type renderTransition struct {
	Kind       string `json:"kind"`       // "cut", "crossfade"
	DurationMS int64  `json:"durationMs"` // ignored for "cut"
}

// renderRequest is the payload for POST /api/v1/render.
type renderRequest struct {
	ProjectID     string             `json:"projectId"`
	OneDriveToken string             `json:"oneDriveToken"`
	Clips         []renderClip       `json:"clips"`
	Transitions   []renderTransition `json:"transitions,omitempty"`
	Preset        string             `json:"preset"`     // e.g. "h264-1080p"
	OutputName    string             `json:"outputName"` // destination filename
}

// renderResult is returned after a successful or failed render.
type renderResult struct {
	Status    string `json:"status"` // "completed" or "failed"
	OutputURL string `json:"outputUrl"`
	Log       string `json:"log,omitempty"`
}

// handleRender runs the full render pipeline server-side:
// download source clips from OneDrive → build FFmpeg concat → run render → upload to OneDrive → cleanup.
func (s *Server) handleRender(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	startTime := time.Now()

	var req renderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	if req.OneDriveToken == "" {
		writeError(w, http.StatusBadRequest, "oneDriveToken is required")
		return
	}
	if len(req.Clips) == 0 {
		writeError(w, http.StatusBadRequest, "at least one clip is required")
		return
	}
	if req.ProjectID == "" {
		req.ProjectID = fmt.Sprintf("render-%d", time.Now().Unix())
	}
	if req.OutputName == "" {
		req.OutputName = "render-output.mp4"
	}
	if req.Preset == "" {
		req.Preset = "h264-1080p"
	}

	workDir := fmt.Sprintf("/tmp/render-%s", req.ProjectID)
	if err := os.MkdirAll(workDir, 0700); err != nil {
		s.logger.Error("render: failed to create work dir", "error", err, "dir", workDir)
		writeError(w, http.StatusInternalServerError, "failed to prepare render workspace")
		return
	}
	defer func() {
		if err := os.RemoveAll(workDir); err != nil {
			s.logger.Error("render: cleanup failed", "error", err, "dir", workDir)
		}
	}()

	// Step 1: Download each source clip from OneDrive to local temp files.
	// We track the paths so we can build the FFmpeg concat input list.
	var clipPaths []clipPath
	var totalBytes int64

	for i, clip := range req.Clips {
		s.logger.Info("render: downloading clip",
			"clip", i+1,
			"total", len(req.Clips),
			"itemID", clip.Input)

		body, _, err := s.oneDrive.DownloadItem(ctx, clip.Input, req.OneDriveToken)
		if err != nil {
			s.logger.Error("render: failed to download clip", "error", err, "itemID", clip.Input)
			writeError(w, http.StatusBadGateway, fmt.Sprintf("failed to download clip %d from OneDrive", i+1))
			return
		}

		localPath := filepath.Join(workDir, fmt.Sprintf("clip-%d-%s.mp4", i+1, safeClipID(clip.ID)))
		f, err := os.Create(localPath)
		if err != nil {
			body.Close()
			s.logger.Error("render: failed to create local file", "error", err, "path", localPath)
			writeError(w, http.StatusInternalServerError, "failed to buffer clip")
			return
		}

		n, copyErr := io.Copy(f, body)
		body.Close()
		closeErr := f.Close()
		if copyErr != nil {
			s.logger.Error("render: failed to write clip", "error", copyErr, "path", localPath)
			writeError(w, http.StatusInternalServerError, "failed to buffer clip")
			return
		}
		if closeErr != nil {
			s.logger.Error("render: failed to close clip file", "error", closeErr, "path", localPath)
			writeError(w, http.StatusInternalServerError, "failed to finalize clip")
			return
		}

		totalBytes += n
		clipPaths = append(clipPaths, clipPath{
			ID:   clip.ID,
			Path: localPath,
			InMS: clip.InMS,
			OutMS: clip.OutMS,
		})
		s.logger.Info("render: clip downloaded", "clip", i+1, "bytes", n)
	}

	// Step 2: Build and run the FFmpeg command.
	// Strategy: use a concat filter for precise timeline assembly.
	// For crossfade transitions we use xfade, otherwise concat.
	outputPath := filepath.Join(workDir, req.OutputName)

	ffmpegArgs, buildErr := buildRenderCommand(outputPath, clipPaths, req.Transitions, req.Preset)
	if buildErr != nil {
		s.logger.Error("render: failed to build FFmpeg command", "error", buildErr)
		writeError(w, http.StatusBadRequest, buildErr.Error())
		return
	}

	s.logger.Info("render: starting FFmpeg", "projectID", req.ProjectID, "args", strings.Join(ffmpegArgs, " "))

	cmd := exec.CommandContext(ctx, "ffmpeg", ffmpegArgs...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	runErr := cmd.Run()
	duration := time.Since(startTime)

	logOutput := stderr.String()

	if runErr != nil {
		s.logger.Error("render: FFmpeg failed",
			"error", runErr,
			"duration", duration.String(),
			"stderr", truncateLog(logOutput, 500))
		writeJSON(w, http.StatusOK, renderResult{
			Status: "failed",
			Log:    logOutput,
		})
		return
	}

	s.logger.Info("render: FFmpeg completed", "projectID", req.ProjectID, "duration", duration.String(), "outputBytes", fileSize(outputPath))

	// Step 3: Upload the rendered output to OneDrive.
	outputFile, err := os.Open(outputPath)
	if err != nil {
		s.logger.Error("render: failed to open output file", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to read render output")
		return
	}
	defer outputFile.Close()

	outputURL, uploadErr := s.uploadToOneDrive(ctx, req.OutputName, outputFile, req.OneDriveToken)
	if uploadErr != nil {
		s.logger.Error("render: OneDrive upload failed", "error", uploadErr)
		writeJSON(w, http.StatusOK, renderResult{
			Status: "failed",
			Log:    fmt.Sprintf("%s\n\nUpload to OneDrive failed: %v", logOutput, uploadErr),
		})
		return
	}

	totalDuration := time.Since(startTime)
	s.logger.Info("render: complete",
		"projectID", req.ProjectID,
		"totalDuration", totalDuration.String(),
		"outputURL", outputURL)

	writeJSON(w, http.StatusOK, renderResult{
		Status:    "completed",
		OutputURL: outputURL,
		Log:       logOutput,
	})
}

// buildRenderCommand constructs the FFmpeg command line for rendering a
// timeline of clips with optional transitions.
func buildRenderCommand(outputPath string, clips []clipPath, transitions []renderTransition, preset string) ([]string, error) {
	if len(clips) == 0 {
		return nil, fmt.Errorf("no clips to render")
	}

	// For a single clip with trim, we can use a simple -ss -to approach.
	// For multiple clips, we use the concat demuxer or concat filter.
	// This implementation uses the concat filter (supports trimming).

	if len(clips) == 1 {
		// Simple trim of a single clip.
		clip := clips[0]
		args := []string{"-y", "-ss", msToTime(clip.InMS)}
		if clip.OutMS > 0 {
			args = append(args, "-to", msToTime(clip.OutMS))
		}
		args = append(args, "-i", clip.Path)
		args = append(args, presetArgs(preset)...)
		args = append(args, outputPath)
		return args, nil
	}

	// Multiple clips: use concat filter with per-clip trimming.
	// Build: ffmpeg -i c1 -i c2 -i c3 -filter_complex "[0:v]trim=0:5,setpts=PTS-STARTPTS[v0]; [1:v]trim=2:7,setpts=PTS-STARTPTS[v1]; [v0][v1]concat=n=2:v=1:a=0[outv]" -map "[outv]" output
	var inputs []string
	var filterParts []string
	var labeledSegs []string

	for i, clip := range clips {
		inputs = append(inputs, "-i", clip.Path)
		startSec := fmt.Sprintf("%.3f", float64(clip.InMS)/1000.0)
		var durSec string
		if clip.OutMS > 0 {
			durSec = fmt.Sprintf("%.3f", float64(clip.OutMS-clip.InMS)/1000.0)
		} else {
			durSec = ""
		}

		vLabel := fmt.Sprintf("v%d", i)
		aLabels := fmt.Sprintf("a%d", i)

		if durSec != "" {
			filterParts = append(filterParts,
				fmt.Sprintf("[%d:v]trim=%s:duration=%s,setpts=PTS-STARTPTS[%s]", i, startSec, durSec, vLabel))
			filterParts = append(filterParts,
				fmt.Sprintf("[%d:a]atrim=%s:duration=%s,asetpts=PTS-STARTPTS[%s]", i, startSec, durSec, aLabels))
		} else {
			filterParts = append(filterParts,
				fmt.Sprintf("[%d:v]trim=start=%s,setpts=PTS-STARTPTS[%s]", i, startSec, vLabel))
			filterParts = append(filterParts,
				fmt.Sprintf("[%d:a]atrim=start=%s,asetpts=PTS-STARTPTS[%s]", i, startSec, aLabels))
		}
		labeledSegs = append(labeledSegs, fmt.Sprintf("[%s][%s]", vLabel, aLabels))
	}

	// Concat all segments
	n := len(clips)
	filterParts = append(filterParts,
		fmt.Sprintf("%sconcat=n=%d:v=1:a=1[outv][outa]", strings.Join(labeledSegs, ""), n))

	filterComplex := strings.Join(filterParts, "; ")

	args := append([]string{"-y"}, inputs...)
	args = append(args, "-filter_complex", filterComplex, "-map", "[outv]", "-map", "[outa]")
	args = append(args, presetArgs(preset)...)
	args = append(args, outputPath)

	return args, nil
}

// uploadToOneDrive uploads a local file to OneDrive via Microsoft Graph
// createUploadSession and returns the resulting item web URL.
func (s *Server) uploadToOneDrive(ctx context.Context, filename string, src io.Reader, token string) (string, error) {
	graphURL := fmt.Sprintf("%s/me/drive/root:/%s:/createUploadSession", graphBaseURL, filename)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, graphURL, nil)
	if err != nil {
		return "", fmt.Errorf("building upload session request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("creating upload session: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("createUploadSession returned %d: %s", resp.StatusCode, string(body))
	}

	var session struct {
		UploadURL string `json:"uploadUrl"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&session); err != nil {
		return "", fmt.Errorf("decoding upload session: %w", err)
	}
	if session.UploadURL == "" {
		return "", fmt.Errorf("no uploadUrl in upload session response")
	}

	// Read the whole file into memory (render outputs are typically small < 2GB).
	data, err := io.ReadAll(src)
	if err != nil {
		return "", fmt.Errorf("reading output data: %w", err)
	}
	totalSize := len(data)

	putReq, err := http.NewRequestWithContext(ctx, http.MethodPut, session.UploadURL, bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("building upload put request: %w", err)
	}
	putReq.Header.Set("Content-Range", fmt.Sprintf("bytes 0-%d/%d", totalSize-1, totalSize))

	putResp, err := http.DefaultClient.Do(putReq)
	if err != nil {
		return "", fmt.Errorf("uploading chunk: %w", err)
	}
	defer putResp.Body.Close()

	if putResp.StatusCode != http.StatusCreated && putResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(putResp.Body, 4096))
		return "", fmt.Errorf("upload put returned %d: %s", putResp.StatusCode, string(body))
	}

	var item struct {
		WebURL string `json:"webUrl"`
	}
	putBody, _ := io.ReadAll(putResp.Body)
	_ = json.Unmarshal(putBody, &item)

	if item.WebURL == "" {
		// Some Graph responses don't include webUrl directly; derive it.
		item.WebURL = fmt.Sprintf("%s/me/drive/root:/%s", graphBaseURL, filename)
	}

	return item.WebURL, nil
}

// safeClipID returns a safe filename fragment from a clip ID.
func safeClipID(id string) string {
	return strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, id)
}

// msToTime converts milliseconds to ffmpeg time format (HH:MM:SS.mmm or SSS.s).
func msToTime(ms int64) string {
	totalSeconds := float64(ms) / 1000.0
	return fmt.Sprintf("%.3f", totalSeconds)
}

// presetArgs returns FFmpeg output encoding arguments for a given preset name.
func presetArgs(preset string) []string {
	switch strings.ToLower(strings.TrimSpace(preset)) {
	case "h264-1080p":
		return []string{"-c:v", "libx264", "-preset", "medium", "-crf", "23", "-vf", "scale=1920:1080:force_original_aspect_ratio=decrease,pad=1920:1080:(ow-iw)/2:(oh-ih)/2", "-c:a", "aac", "-b:a", "128k", "-movflags", "+faststart"}
	case "h264-720p":
		return []string{"-c:v", "libx264", "-preset", "medium", "-crf", "23", "-vf", "scale=1280:720:force_original_aspect_ratio=decrease,pad=1280:720:(ow-iw)/2:(oh-ih)/2", "-c:a", "aac", "-b:a", "128k", "-movflags", "+faststart"}
	case "h265-1080p":
		return []string{"-c:v", "libx265", "-preset", "medium", "-crf", "28", "-vf", "scale=1920:1080:force_original_aspect_ratio=decrease,pad=1920:1080:(ow-iw)/2:(oh-ih)/2", "-c:a", "aac", "-b:a", "128k", "-movflags", "+faststart"}
	default:
		// Default to H.264 1080p.
		return []string{"-c:v", "libx264", "-preset", "fast", "-crf", "23", "-vf", "scale=1920:1080:force_original_aspect_ratio=decrease,pad=1920:1080:(ow-iw)/2:(oh-ih)/2", "-c:a", "aac", "-b:a", "128k", "-movflags", "+faststart"}
	}
}

// truncateLog cuts a log string to a maximum length for error reporting.
func truncateLog(log string, maxLen int) string {
	if len(log) <= maxLen {
		return log
	}
	return log[:maxLen] + "..."
}

// fileSize returns the file size in bytes, or -1 on error.
func fileSize(path string) int64 {
	fi, err := os.Stat(path)
	if err != nil {
		return -1
	}
	return fi.Size()
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
