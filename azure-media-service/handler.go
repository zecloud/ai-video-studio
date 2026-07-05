package main

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
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
