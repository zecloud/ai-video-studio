package main

import (
	"crypto/subtle"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	cfg, err := LoadConfig()
	if err != nil {
		logger.Error("configuration error", "error", err)
		os.Exit(1)
	}

	blobs, err := NewBlobStore(cfg)
	if err != nil {
		logger.Error("failed to initialize blob store", "error", err)
		os.Exit(1)
	}

	oneDrive := NewOneDriveDownloader()
	server := NewServer(cfg, blobs, oneDrive, logger)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", server.handleHealth)
	mux.Handle("POST /api/v1/copy", requireAPIKey(cfg, http.HandlerFunc(server.handleCopy)))
	mux.Handle("DELETE /api/v1/blobs/{name}", requireAPIKey(cfg, http.HandlerFunc(server.handleDeleteByPath)))
	mux.Handle("POST /api/v1/analyze", requireAPIKey(cfg, http.HandlerFunc(server.handleAnalyze)))
	mux.Handle("POST /api/v1/render", requireAPIKey(cfg, http.HandlerFunc(server.handleRender)))

	httpServer := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           logRequests(logger, mux),
		ReadHeaderTimeout: 10 * time.Second,
	}

	logger.Info("azure-media-service starting", "port", cfg.Port, "container", cfg.ContainerName)
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Error("server exited", "error", err)
		os.Exit(1)
	}
}

// requireAPIKey enforces a Bearer token match against the configured
// API_KEY shared secret. Health checks intentionally bypass this
// middleware so container orchestrators can probe liveness without
// credentials.
func requireAPIKey(cfg *Config, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		const prefix = "Bearer "
		if !strings.HasPrefix(authHeader, prefix) {
			writeError(w, http.StatusUnauthorized, "missing or malformed Authorization header")
			return
		}

		token := strings.TrimPrefix(authHeader, prefix)
		if token == "" || subtle.ConstantTimeCompare([]byte(token), []byte(cfg.APIKey)) != 1 {
			writeError(w, http.StatusUnauthorized, "invalid API key")
			return
		}

		next.ServeHTTP(w, r)
	})
}

// logRequests emits a structured access log line for every request handled.
func logRequests(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rw, r)
		logger.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rw.status,
			"duration_ms", time.Since(start).Milliseconds(),
		)
	})
}

// statusRecorder captures the HTTP status code written by downstream
// handlers so it can be included in access logs.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}
