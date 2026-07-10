package backend

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/zecloud/ai-video-studio/internal/camera"
	cu "github.com/zecloud/ai-video-studio/internal/contentunderstanding"
	"github.com/zecloud/ai-video-studio/internal/dji"
	"github.com/zecloud/ai-video-studio/internal/editing"
	"github.com/zecloud/ai-video-studio/internal/library"
	"github.com/zecloud/ai-video-studio/internal/mediaservice"
	"github.com/zecloud/ai-video-studio/internal/onedrive"
	"github.com/zecloud/ai-video-studio/internal/settings"
	"github.com/zecloud/ai-video-studio/internal/transfer"
	"github.com/zecloud/ai-video-studio/internal/videoindexerstudio"
	"github.com/zecloud/ai-video-studio/internal/videoprocessing"
)

var errNotConfigured = errors.New("service is scaffolded but not configured")

var errOneDriveSignInRequired = errors.New("OneDrive sign-in required. Open Settings and sign in to create the secure Windows token cache.")

type AppOverview struct {
	Name        string    `json:"name"`
	Version     string    `json:"version"`
	StartedAt   time.Time `json:"startedAt"`
	Description string    `json:"description"`
}

type AppService struct {
	startedAt time.Time
}

func NewAppService() *AppService {
	return &AppService{startedAt: time.Now().UTC()}
}

func (s *AppService) GetOverview(_ context.Context) (AppOverview, error) {
	return AppOverview{
		Name:        "AI Video Studio",
		Version:     "0.1.0-scaffold",
		StartedAt:   s.startedAt,
		Description: "Wails v3 scaffold for DJI Osmo Action 4 import, OneDrive transfer, Azure Content Understanding, and non-destructive editing.",
	}, nil
}

type CameraService struct {
	mediaConnector *camera.OsmoHTTPConnector
}

func NewCameraService() *CameraService {
	return &CameraService{mediaConnector: camera.NewOsmoHTTPConnector()}
}

func (s *CameraService) GetConnectionStatus(_ context.Context) (camera.ConnectionStatus, error) {
	return camera.ConnectionStatus{Available: false, Message: "Camera connector can construct and probe Osmo HTTP media requests; BLE/Wi-Fi pairing and real hardware validation are still pending."}, nil
}

func (s *CameraService) Discover(_ context.Context) ([]camera.CameraDevice, error) {
	return []camera.CameraDevice{}, nil
}

func (s *CameraService) ProbeEndpoint(ctx context.Context, req camera.EndpointProbeRequest) (camera.EndpointProbeResult, error) {
	ctx, cancel := context.WithTimeout(ctx, camera.DefaultOsmoHTTPTimeout)
	defer cancel()
	if s.mediaConnector == nil {
		s.mediaConnector = camera.NewOsmoHTTPConnector()
	}
	return s.mediaConnector.ProbeEndpoint(ctx, req)
}

func (s *CameraService) ProbeEndpointCandidates(ctx context.Context, req camera.EndpointProbeRequest) (camera.EndpointProbePlan, error) {
	ctx, cancel := context.WithTimeout(ctx, camera.DefaultOsmoHTTPTimeout*time.Duration(len(camera.ProbePorts(req.Port))))
	defer cancel()
	if s.mediaConnector == nil {
		s.mediaConnector = camera.NewOsmoHTTPConnector()
	}
	return s.mediaConnector.ProbeEndpointCandidates(ctx, req)
}

func (s *CameraService) ListMedia(_ context.Context, _ string) ([]camera.CameraMediaItem, error) {
	return []camera.CameraMediaItem{}, nil
}

func (s *CameraService) ValidationChecklist(_ context.Context) ([]camera.ValidationStep, error) {
	return []camera.ValidationStep{
		{ID: "ble-scan", Label: "BLE scan", Status: "pending", Description: "Detect Osmo Action 4 advertisements and model metadata before pairing."},
		{ID: "ble-pair", Label: "BLE pairing", Status: "pending", Description: "Pair without persisting secrets; confirm pairing failure and retry states."},
		{ID: "wifi-ap", Label: "Wi-Fi/AP setup", Status: "pending", Description: "Enable or join the camera AP and record SSID, IP, and diagnostic state."},
		{ID: "media-endpoint", Label: "HTTP /v2 endpoint", Status: "pending", Description: "Probe http://192.168.2.1/v2?storage={0|1}&path=<path> and any confirmed port variants."},
		{ID: "head-range", Label: "HEAD and Range", Status: "pending", Description: "Validate content length, type, byte ranges, 206 responses, and retry behavior."},
		{ID: "media-enumeration", Label: "Media discovery", Status: "pending", Description: "Validate storage IDs, paths, file names, thumbnails, and fallback enumeration."},
	}, nil
}

type DJIControlService struct {
	controller dji.Controller
}

func NewDJIControlService(controller ...dji.Controller) *DJIControlService {
	service := &DJIControlService{}
	if len(controller) > 0 {
		service.controller = controller[0]
	}
	if service.controller == nil {
		service.controller = dji.NewDefaultController()
	}
	return service
}

func (s *DJIControlService) Status(ctx context.Context) (dji.ControlStatus, error) {
	return s.controllerOrDefault().Status(ctx)
}

func (s *DJIControlService) ScanBLE(ctx context.Context) ([]dji.BLEDevice, error) {
	return s.controllerOrDefault().ScanBLE(ctx)
}

func (s *DJIControlService) Pair(ctx context.Context, req dji.PairingRequest) (dji.PairingResult, error) {
	return s.controllerOrDefault().Pair(ctx, req)
}

func (s *DJIControlService) SetupWiFi(ctx context.Context, req dji.WiFiSetupRequest) (dji.WiFiProfile, error) {
	return s.controllerOrDefault().SetupWiFi(ctx, req)
}

func (s *DJIControlService) RunDiagnostics(ctx context.Context, deviceID string) (dji.DiagnosticResult, error) {
	return s.controllerOrDefault().RunDiagnostics(ctx, deviceID)
}

func (s *DJIControlService) ProtocolProfile(ctx context.Context) (dji.ProtocolProfile, error) {
	return s.controllerOrDefault().ProtocolProfile(ctx)
}

func (s *DJIControlService) controllerOrDefault() dji.Controller {
	if s == nil || s.controller == nil {
		return dji.NewDefaultController()
	}
	return s.controller
}

type TransferService struct {
	oneDrive              *OneDriveService
	libraryStore          library.Store
	orchestrator          *transfer.Orchestrator
	mu                    sync.Mutex
	localTransferInFlight bool
}

func NewTransferService(oneDrive *OneDriveService, libStore library.Store) *TransferService {
	var target *OneDriveService
	target = oneDrive
	if target == nil {
		target = NewOneDriveService()
	}
	return &TransferService{
		oneDrive:     target,
		libraryStore: libStore,
		orchestrator: transfer.NewOrchestrator(nil, oneDriveUploadTarget{service: target}),
	}
}

func (s *TransferService) ListJobs(_ context.Context) ([]transfer.TransferJob, error) {
	if s == nil {
		return []transfer.TransferJob{}, nil
	}
	s.mu.Lock()
	orchestrator := s.orchestrator
	s.mu.Unlock()
	if orchestrator == nil {
		return []transfer.TransferJob{}, nil
	}
	return orchestrator.ListJobs(), nil
}

func (s *TransferService) StartCameraToOneDrive(_ context.Context, req transfer.StartTransferRequest) (transfer.TransferJob, error) {
	return transfer.TransferJob{
		ID:          fmt.Sprintf("stub-%d", time.Now().UnixNano()),
		SourcePath:  fmt.Sprintf("camera:%s", req.CameraDeviceID),
		Destination: req.DestinationPath,
		Status:      transfer.TransferBlocked,
		CreatedAt:   time.Now().UTC(),
		Message:     "Transfer orchestration is scaffolded; configure camera and OneDrive implementations before uploading.",
	}, nil
}

func (s *TransferService) DescribeLocalFiles(_ context.Context, req transfer.DescribeLocalFilesRequest) ([]transfer.LocalMediaFile, error) {
	if len(req.Paths) == 0 {
		return []transfer.LocalMediaFile{}, nil
	}
	files := make([]transfer.LocalMediaFile, 0, len(req.Paths))
	for _, rawPath := range req.Paths {
		file, err := describeLocalMediaFile(rawPath)
		if err != nil {
			return nil, err
		}
		files = append(files, file)
	}
	return files, nil
}

func (s *TransferService) StartLocalToOneDrive(ctx context.Context, req transfer.LocalToOneDriveRequest) (transfer.TransferJob, error) {
	if s == nil || s.oneDrive == nil {
		return transfer.TransferJob{}, fmt.Errorf("%w: OneDrive service is not configured", transfer.ErrInvalidTransferRequest)
	}
	s.mu.Lock()
	if s.localTransferInFlight {
		s.mu.Unlock()
		return transfer.TransferJob{}, fmt.Errorf("%w: a local OneDrive upload is already running", transfer.ErrInvalidTransferRequest)
	}
	if s.orchestrator == nil {
		s.orchestrator = transfer.NewOrchestrator(nil, oneDriveUploadTarget{service: s.oneDrive})
	}
	orchestrator := s.orchestrator
	s.localTransferInFlight = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.localTransferInFlight = false
		s.mu.Unlock()
	}()

	if req.ChunkSizeBytes == 0 {
		cfg, err := s.oneDrive.currentSettings(ctx)
		if err != nil {
			return transfer.TransferJob{}, err
		}
		req.ChunkSizeBytes = cfg.ChunkSizeBytes
	}
	if strings.TrimSpace(req.DestinationPath) == "" {
		req.DestinationPath = "Imports"
	}
	job, err := orchestrator.TransferLocalToOneDrive(ctx, req)
	if err != nil {
		return job, err
	}
	// After a successful upload, register the asset in the project library.
	if job.Status == transfer.TransferCompleted && job.DriveItemID != "" && s.libraryStore != nil {
		asset := library.ProjectAsset{
			Name:         job.FileName,
			CloudAssetID: job.DriveItemID,
			SizeBytes:    job.BytesTotal,
		}
		if asset.Name == "" {
			asset.Name = filepath.Base(job.SourcePath)
		}
		ext := strings.ToLower(filepath.Ext(asset.Name))
		if ct, ok := fileExtensionToMIME(ext); ok {
			asset.ContentType = ct
		}
		asset.CreatedAt = time.Now()
		if addErr := s.libraryStore.AddAsset(ctx, asset); addErr != nil {
			job.Message = fmt.Sprintf("Uploaded but library registration failed: %v", addErr)
		}
	}
	return job, nil
}

func (s *TransferService) Cancel(_ context.Context, jobID string) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	orchestrator := s.orchestrator
	s.mu.Unlock()
	if orchestrator == nil {
		return nil
	}
	return orchestrator.Cancel(jobID)
}

// fileExtensionToMIME returns the MIME type for a video extension, or false.
func fileExtensionToMIME(ext string) (string, bool) {
	switch ext {
	case ".mp4":
		return "video/mp4", true
	case ".mov":
		return "video/quicktime", true
	case ".m4v":
		return "video/x-m4v", true
	case ".lrv":
		return "video/mp4", true
	case ".mpg", ".mpeg":
		return "video/mpeg", true
	case ".avi":
		return "video/x-msvideo", true
	}
	return "", false
}

func describeLocalMediaFile(rawPath string) (transfer.LocalMediaFile, error) {
	cleanPath := filepath.Clean(strings.TrimSpace(rawPath))
	if cleanPath == "." || cleanPath == "" {
		return transfer.LocalMediaFile{}, fmt.Errorf("%w: local file path is required", transfer.ErrInvalidTransferRequest)
	}
	info, err := os.Stat(cleanPath)
	if err != nil {
		return transfer.LocalMediaFile{}, err
	}
	if info.IsDir() || !info.Mode().IsRegular() {
		return transfer.LocalMediaFile{}, fmt.Errorf("%w: %q is not a regular file", transfer.ErrInvalidTransferRequest, cleanPath)
	}
	ext := strings.ToLower(filepath.Ext(cleanPath))
	if !isSupportedLocalVideoExtension(ext) {
		return transfer.LocalMediaFile{}, fmt.Errorf("%w: %q is not a supported video file", transfer.ErrInvalidTransferRequest, cleanPath)
	}
	contentType := mime.TypeByExtension(ext)
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	return transfer.LocalMediaFile{
		ID:          cleanPath,
		Name:        filepath.Base(cleanPath),
		Path:        cleanPath,
		SizeBytes:   info.Size(),
		ModifiedAt:  info.ModTime().UTC(),
		ContentType: contentType,
	}, nil
}

func isSupportedLocalVideoExtension(ext string) bool {
	switch ext {
	case ".mp4", ".mov", ".m4v", ".lrv":
		return true
	default:
		return false
	}
}

type OneDriveService struct {
	mu         sync.Mutex
	store      settings.Store
	tokenStore oneDriveTokenStore
	authClient *onedrive.AuthClient
	pending    onedrive.DeviceCodeSession
	token      onedrive.TokenSet
}

func NewOneDriveService(store ...settings.Store) *OneDriveService {
	service := &OneDriveService{authClient: onedrive.NewAuthClient(http.DefaultClient)}
	if len(store) > 0 {
		service.store = store[0]
	}
	if service.store == nil {
		service.store = newDefaultSettingsStore()
	}
	service.tokenStore = newDefaultOneDriveTokenStore()
	return service
}

func (s *OneDriveService) Status(ctx context.Context) (onedrive.Status, error) {
	cfg := defaultSettings()
	if s != nil && s.store != nil {
		loaded, err := s.store.Load(ctx)
		if err != nil {
			return onedrive.Status{}, err
		}
		cfg = loaded
	}
	authStatus := onedrive.AuthSignedOut
	message := "Microsoft Graph device-code auth is configured but not signed in. No client secrets are stored."
	tokenCacheAvailable := s != nil && s.tokenStore != nil && s.tokenStore.Available()
	authState := onedrive.AuthState{
		Status:              onedrive.AuthSignedOut,
		TenantID:            cfg.GraphAuth.TenantID,
		GrantedScopes:       []string{},
		TokenCacheAvailable: tokenCacheAvailable,
		Message:             message,
	}
	if strings.TrimSpace(cfg.GraphAuth.ClientID) == "" {
		authStatus = onedrive.AuthNotConfigured
		message = "Set the Microsoft Entra application client ID before starting OneDrive sign-in."
		authState.Status = onedrive.AuthNotConfigured
		authState.Message = message
	} else if s != nil {
		token, tokenErr := s.ensureToken(ctx, cfg.GraphAuth, cfg.GraphAuth.Scopes)
		if tokenErr == nil && strings.TrimSpace(token.AccessToken) != "" && time.Now().UTC().Before(token.ExpiresAt) {
			authStatus = onedrive.AuthSignedIn
			message = "Signed in with Microsoft Graph delegated auth. Refresh token is stored securely with the OS user profile."
			authState.Status = onedrive.AuthSignedIn
			authState.AccountName = "Microsoft Graph account"
			authState.GrantedScopes = token.Scopes
			authState.ExpiresAt = token.ExpiresAt.Format(time.RFC3339)
			authState.Message = message
		} else if tokenErr != nil && !errors.Is(tokenErr, errOneDriveTokenNotCached) {
			message = "Cached OneDrive sign-in could not be refreshed. Sign in again to continue."
			authState.Message = message
		}
	}
	return onedrive.Status{
		AuthStatus:  authStatus,
		Scope:       onedrive.GraphScopeFilesReadWriteAppFolder,
		Scopes:      cfg.GraphAuth.Scopes,
		Auth:        authState,
		Destination: cfg.OneDriveDestination,
		Message:     message,
	}, nil
}

func (s *OneDriveService) StartDeviceCodeAuth(ctx context.Context) (onedrive.DeviceCodeSession, error) {
	cfg, err := s.currentSettings(ctx)
	if err != nil {
		return onedrive.DeviceCodeSession{}, err
	}
	session, err := s.auth().StartDeviceCode(ctx, cfg.GraphAuth)
	if err != nil {
		return onedrive.DeviceCodeSession{}, err
	}
	s.mu.Lock()
	s.pending = session
	s.mu.Unlock()
	return session, nil
}

func (s *OneDriveService) PollDeviceCodeAuth(ctx context.Context) (onedrive.AuthState, error) {
	s.mu.Lock()
	pending := s.pending
	s.mu.Unlock()
	token, err := s.auth().PollDeviceCode(ctx, pending)
	if err != nil {
		if errors.Is(err, onedrive.ErrAuthorizationPending) {
			return onedrive.AuthState{
				Status:        onedrive.AuthSignedOut,
				TenantID:      pending.TenantID,
				GrantedScopes: pending.Scopes,
				Message:       "Waiting for Microsoft sign-in confirmation.",
			}, nil
		}
		return onedrive.AuthState{}, err
	}
	cfg, err := s.currentSettings(ctx)
	if err != nil {
		return onedrive.AuthState{}, err
	}
	if err := s.saveToken(ctx, cfg.GraphAuth, token); err != nil {
		return onedrive.AuthState{}, err
	}
	s.mu.Lock()
	s.token = token
	s.pending = onedrive.DeviceCodeSession{}
	s.mu.Unlock()
	return onedrive.AuthState{
		Status:              onedrive.AuthSignedIn,
		AccountName:         "Microsoft Graph account",
		TenantID:            pending.TenantID,
		GrantedScopes:       token.Scopes,
		TokenCacheAvailable: s.tokenStore != nil && s.tokenStore.Available(),
		ExpiresAt:           token.ExpiresAt.Format(time.RFC3339),
		Message:             "Signed in to Microsoft Graph. Refresh token is stored securely with the OS user profile.",
	}, nil
}

func (s *OneDriveService) SignOut(ctx context.Context) error {
	s.mu.Lock()
	s.pending = onedrive.DeviceCodeSession{}
	s.token = onedrive.TokenSet{}
	tokenStore := s.tokenStore
	s.mu.Unlock()
	if tokenStore == nil {
		return nil
	}
	return tokenStore.Delete(ctx)
}

// DriveClient returns a configured onedrive.Client that can be used for
// non-upload Graph operations like listing folder contents. It shares the
// same token provider (and cache) as the upload methods.
func (s *OneDriveService) DriveClient() *onedrive.Client {
	scopes := []string{"Files.ReadWrite"}
	var dest onedrive.OneDriveDestination
	cfg, err := s.currentSettings(context.Background())
	if err == nil {
		if len(cfg.GraphAuth.Scopes) > 0 {
			scopes = cfg.GraphAuth.Scopes
		}
		dest = cfg.OneDriveDestination
	}
	return &onedrive.Client{
		HTTPClient:    http.DefaultClient,
		TokenProvider: s,
		GraphBaseURL:  "https://graph.microsoft.com/v1.0",
		Scopes:        scopes,
		Destination:   dest,
	}
}

func (s *OneDriveService) CreateUploadSession(ctx context.Context, destinationPath string, fileSizeBytes int64) (onedrive.OneDriveUploadSession, error) {
	cfg, err := s.currentSettings(ctx)
	if err != nil {
		return onedrive.OneDriveUploadSession{}, err
	}
	client := &onedrive.Client{
		HTTPClient:    http.DefaultClient,
		TokenProvider: graphTokenProvider{service: s},
		Scopes:        cfg.GraphAuth.Scopes,
		Destination:   cfg.OneDriveDestination,
	}
	return client.CreateUploadSession(ctx, destinationPath, fileSizeBytes)
}

type oneDriveUploadTarget struct {
	service *OneDriveService
}

func (t oneDriveUploadTarget) CreateUploadSession(ctx context.Context, destinationPath string, fileSizeBytes int64) (onedrive.OneDriveUploadSession, error) {
	if t.service == nil {
		return onedrive.OneDriveUploadSession{}, errors.New("OneDrive service is not configured")
	}
	return t.service.CreateUploadSession(ctx, destinationPath, fileSizeBytes)
}

func (t oneDriveUploadTarget) UploadChunk(ctx context.Context, session onedrive.OneDriveUploadSession, chunk onedrive.ChunkRange, body io.Reader) (onedrive.OneDriveUploadSession, error) {
	if t.service == nil {
		return onedrive.OneDriveUploadSession{}, errors.New("OneDrive service is not configured")
	}
	cfg, err := t.service.currentSettings(ctx)
	if err != nil {
		return onedrive.OneDriveUploadSession{}, err
	}
	client := &onedrive.Client{
		HTTPClient:    http.DefaultClient,
		TokenProvider: graphTokenProvider{service: t.service},
		Scopes:        cfg.GraphAuth.Scopes,
		Destination:   cfg.OneDriveDestination,
	}
	return client.UploadChunk(ctx, session, chunk, body)
}

type graphTokenProvider struct {
	service *OneDriveService
}

func (p graphTokenProvider) AccessToken(ctx context.Context, scopes []string) (string, error) {
	return p.service.accessToken(ctx, scopes)
}

func (s *OneDriveService) accessToken(ctx context.Context, scopes []string) (string, error) {
	cfg, err := s.currentSettings(ctx)
	if err != nil {
		return "", err
	}
	token, err := s.ensureToken(ctx, cfg.GraphAuth, scopes)
	if err != nil {
		if errors.Is(err, errOneDriveTokenNotCached) {
			return "", errOneDriveSignInRequired
		}
		return "", err
	}
	return token.AccessToken, nil
}

// AccessToken implements onedrive.TokenProvider.
func (s *OneDriveService) AccessToken(ctx context.Context, scopes []string) (string, error) {
	return s.accessToken(ctx, scopes)
}

func (s *OneDriveService) ensureToken(ctx context.Context, cfg onedrive.GraphAuthConfig, scopes []string) (onedrive.TokenSet, error) {
	s.mu.Lock()
	token := s.token
	s.mu.Unlock()
	if strings.TrimSpace(token.AccessToken) != "" && time.Now().UTC().Before(token.ExpiresAt.Add(-1*time.Minute)) {
		return token, nil
	}
	if strings.TrimSpace(token.RefreshToken) == "" && s.tokenStore != nil && s.tokenStore.Available() {
		cached, err := s.tokenStore.Load(ctx, cfg)
		if err == nil {
			token = cached
		} else if !errors.Is(err, errOneDriveTokenNotCached) {
			return onedrive.TokenSet{}, err
		}
	}
	if strings.TrimSpace(token.RefreshToken) == "" {
		return onedrive.TokenSet{}, errOneDriveTokenNotCached
	}
	next, err := s.auth().Refresh(ctx, cfg, token.RefreshToken)
	if err != nil {
		if s.tokenStore != nil {
			_ = s.tokenStore.Delete(ctx)
		}
		return onedrive.TokenSet{}, err
	}
	if len(next.Scopes) == 0 {
		next.Scopes = append([]string(nil), scopes...)
	}
	if strings.TrimSpace(next.RefreshToken) == "" {
		next.RefreshToken = token.RefreshToken
	}
	if err := s.saveToken(ctx, cfg, next); err != nil {
		return onedrive.TokenSet{}, err
	}
	s.mu.Lock()
	s.token = next
	s.mu.Unlock()
	return next, nil
}

func (s *OneDriveService) saveToken(ctx context.Context, cfg onedrive.GraphAuthConfig, token onedrive.TokenSet) error {
	if s == nil || s.tokenStore == nil || !s.tokenStore.Available() {
		return nil
	}
	return s.tokenStore.Save(ctx, cfg, token)
}

func (s *OneDriveService) currentSettings(ctx context.Context) (settings.AppSettings, error) {
	if s == nil || s.store == nil {
		return defaultSettings(), nil
	}
	return s.store.Load(ctx)
}

func (s *OneDriveService) auth() *onedrive.AuthClient {
	if s.authClient == nil {
		s.authClient = onedrive.NewAuthClient(http.DefaultClient)
	}
	return s.authClient
}

type ContentUnderstandingService struct {
	client cu.Service
}

func NewContentUnderstandingService(config ...cu.Config) *ContentUnderstandingService {
	var cfg cu.Config
	if len(config) > 0 {
		cfg = config[0]
	}
	return &ContentUnderstandingService{client: cu.NewClient(cfg, nil)}
}

func (s *ContentUnderstandingService) Status(ctx context.Context) (cu.ServiceStatus, error) {
	if s.client == nil {
		s.client = cu.NewClient(cu.Config{}, nil)
	}
	return s.client.Status(ctx)
}

func (s *ContentUnderstandingService) Submit(ctx context.Context, asset cu.VideoAsset) (string, error) {
	if s.client == nil {
		s.client = cu.NewClient(cu.Config{}, nil)
	}
	return s.client.Submit(ctx, asset)
}

func (s *ContentUnderstandingService) GetResult(ctx context.Context, jobID string) (cu.AnalysisResult, error) {
	if s.client == nil {
		s.client = cu.NewClient(cu.Config{}, nil)
	}
	return s.client.GetResult(ctx, jobID)
}

func (s *ContentUnderstandingService) PollResult(ctx context.Context, operationLocation string) (cu.AnalysisResult, error) {
	if s.client == nil {
		s.client = cu.NewClient(cu.Config{}, nil)
	}
	return s.client.PollResult(ctx, operationLocation)
}

// NewContentUnderstandingServiceFromSettings builds a ContentUnderstandingService
// using the AzureContentUnderstanding config persisted in the settings store, if
// available. It falls back to an unconfigured service when the store is nil or
// settings cannot be loaded.
func NewContentUnderstandingServiceFromSettings(store settings.Store) *ContentUnderstandingService {
	if store == nil {
		return NewContentUnderstandingService()
	}
	loaded, err := store.Load(context.Background())
	if err != nil {
		return NewContentUnderstandingService()
	}
	return NewContentUnderstandingService(loaded.AzureContentUnderstanding)
}

// NewMediaServiceClient builds a mediaservice.Client using the media staging
// endpoint/API key persisted in the settings store, if available. The client
// is safe to construct even when settings are missing; calls will fail with
// a descriptive error until configured.
func NewMediaServiceClient(store settings.Store) *mediaservice.Client {
	var cfg mediaservice.Config
	if store != nil {
		if loaded, err := store.Load(context.Background()); err == nil {
			cfg = mediaservice.Config{
				Endpoint: loaded.MediaServiceEndpoint,
				APIKey:   loaded.MediaServiceAPIKey,
			}
		}
	}
	return mediaservice.NewClient(cfg, nil)
}

type EditingService struct {
	mu            sync.Mutex
	projects      map[string]editing.EditProject
	jobs          map[string]*editing.RenderJob
	store         library.Store
	renderBackend RenderBackend
	odClient      *onedrive.Client
	projectStore  editingProjectStore
	loaded        bool
}

// RenderBackend is the subset of mediaservice.Client that editing needs.
type RenderBackend interface {
	Render(ctx context.Context, req mediaservice.RenderRequest) (*mediaservice.RenderResult, error)
}

func NewEditingService(store library.Store, renderBackend RenderBackend, odClient *onedrive.Client, projectStore ...editingProjectStore) *EditingService {
	service := &EditingService{
		projects:      map[string]editing.EditProject{},
		jobs:          map[string]*editing.RenderJob{},
		store:         store,
		renderBackend: renderBackend,
		odClient:      odClient,
	}
	if len(projectStore) > 0 {
		service.projectStore = projectStore[0]
	} else {
		service.projectStore = newDefaultEditingProjectStore()
	}
	if service.projectStore == nil {
		service.loaded = true
	}
	return service
}

func (s *EditingService) ListProjects(_ context.Context) ([]editing.EditProject, error) {
	if err := s.ensureProjectsLoaded(context.Background()); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	projects := make([]editing.EditProject, 0, len(s.projects))
	for _, project := range s.projects {
		projects = append(projects, project)
	}
	sort.Slice(projects, func(i, j int) bool {
		if projects[i].Name == projects[j].Name {
			return projects[i].ID < projects[j].ID
		}
		return projects[i].Name < projects[j].Name
	})
	return projects, nil
}

func (s *EditingService) CreateDraftProject(ctx context.Context, name string) (editing.EditProject, error) {
	if name == "" {
		name = "Untitled edit"
	}
	return s.SaveProject(ctx, editing.EditProject{
		ID:       fmt.Sprintf("draft-%d", time.Now().UnixNano()),
		Name:     name,
		Timeline: editing.Timeline{Tracks: []editing.Track{{ID: "video-1", Kind: "video"}}},
	})
}

func (s *EditingService) SaveProject(_ context.Context, project editing.EditProject) (editing.EditProject, error) {
	if project.ID == "" {
		project.ID = fmt.Sprintf("draft-%d", time.Now().UnixNano())
	}
	if project.Name == "" {
		project.Name = "Untitled edit"
	}
	if len(project.Timeline.Tracks) == 0 {
		project.Timeline.Tracks = []editing.Track{{ID: "video-1", Kind: "video"}}
	}
	if err := s.ensureProjectsLoaded(context.Background()); err != nil {
		return editing.EditProject{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.projects == nil {
		s.projects = map[string]editing.EditProject{}
	}
	s.projects[project.ID] = project
	if err := s.persistProjectsLocked(context.Background()); err != nil {
		return editing.EditProject{}, err
	}
	return project, nil
}

func (s *EditingService) DeleteProject(_ context.Context, projectID string) error {
	if err := s.ensureProjectsLoaded(context.Background()); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.projects, projectID)
	return s.persistProjectsLocked(context.Background())
}

// Render dispatches a render job to the Azure Container App. It blocks until
// the remote service completes or fails, then stores the result.
// Emits wails events "editing:render:progress" and "editing:render:completed".
func (s *EditingService) Render(projectID string) (*editing.RenderJob, error) {
	if err := s.ensureProjectsLoaded(context.Background()); err != nil {
		return nil, err
	}
	s.mu.Lock()
	project, ok := s.projects[projectID]
	s.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("project %q not found", projectID)
	}

	// Build the render request from the project timeline.
	clips := make([]mediaservice.RenderClip, 0)
	for _, track := range project.Timeline.Tracks {
		for _, clip := range track.Clips {
			clips = append(clips, mediaservice.RenderClip{
				ID:    clip.ID,
				Input: clip.SourceAssetID,
				InMS:  clip.InMS,
				OutMS: clip.OutMS,
			})
		}
	}

	if len(clips) == 0 {
		return nil, fmt.Errorf("project %q has no clips", projectID)
	}

	preset := project.RenderPreset
	if preset == "" {
		preset = "h264-1080p"
	}

	// Obtain a OneDrive access token.
	var token string
	if s.odClient != nil && s.odClient.TokenProvider != nil {
		tok, err := s.odClient.TokenProvider.AccessToken(context.Background(), s.odClient.Scopes)
		if err != nil {
			return nil, fmt.Errorf("editing: OneDrive token: %w", err)
		}
		token = tok
	}

	jobID := fmt.Sprintf("render-%d", time.Now().UnixNano())
	job := &editing.RenderJob{
		ID:        jobID,
		ProjectID: projectID,
		Status:    "submitted",
	}

	s.mu.Lock()
	s.jobs[jobID] = job
	s.mu.Unlock()

	if s.renderBackend == nil {
		job.Status = "failed"
		job.ErrorDetail = "render backend is not configured"
		return job, fmt.Errorf("editing: render backend not configured")
	}

	// Emit progress: submitted
	emitRenderEvent("editing:render:progress", *job)

	result, err := s.renderBackend.Render(context.Background(), mediaservice.RenderRequest{
		ProjectID:     projectID,
		OneDriveToken: token,
		Clips:         clips,
		Preset:        preset,
		OutputName:    project.Name + ".mp4",
	})
	if err != nil {
		job.Status = "failed"
		job.ErrorDetail = err.Error()
		s.mu.Lock()
		s.jobs[jobID] = job
		s.mu.Unlock()
		emitRenderEvent("editing:render:completed", *job)
		return job, fmt.Errorf("editing: render: %w", err)
	}

	job.Status = "completed"
	job.OutputURL = result.OutputURL
	job.Message = result.Log
	s.mu.Lock()
	s.jobs[jobID] = job
	s.mu.Unlock()

	emitRenderEvent("editing:render:completed", *job)
	return job, nil
}

// emitRenderEvent notifies the frontend of a render job update via a
// Wails event. It is a no-op when no application instance is running (e.g.
// during unit tests), so it is always safe to call.
func emitRenderEvent(name string, job editing.RenderJob) {
	app := application.Get()
	if app == nil || app.Event == nil {
		return
	}
	app.Event.Emit(name, job)
}

// RenderJob returns the current state of a render job.
func (s *EditingService) RenderJob(jobID string) (*editing.RenderJob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[jobID]
	if !ok {
		return nil, fmt.Errorf("render job %q not found", jobID)
	}
	return job, nil
}

// RenderJobs returns all render jobs.
func (s *EditingService) RenderJobs() ([]*editing.RenderJob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	jobs := make([]*editing.RenderJob, 0, len(s.jobs))
	for _, job := range s.jobs {
		jobs = append(jobs, job)
	}
	return jobs, nil
}

func (s *EditingService) ensureProjectsLoaded(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	if s.loaded {
		s.mu.Unlock()
		return nil
	}
	store := s.projectStore
	s.mu.Unlock()
	if store == nil {
		s.mu.Lock()
		s.loaded = true
		s.mu.Unlock()
		return nil
	}
	projects, err := store.Load(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loaded {
		return nil
	}
	if s.projects == nil {
		s.projects = map[string]editing.EditProject{}
	}
	for _, project := range projects {
		if project.ID == "" {
			continue
		}
		s.projects[project.ID] = project
	}
	s.loaded = true
	return nil
}

func (s *EditingService) persistProjectsLocked(ctx context.Context) error {
	if s == nil || s.projectStore == nil {
		return nil
	}
	projects := make([]editing.EditProject, 0, len(s.projects))
	for _, project := range s.projects {
		projects = append(projects, project)
	}
	sort.Slice(projects, func(i, j int) bool {
		if projects[i].Name == projects[j].Name {
			return projects[i].ID < projects[j].ID
		}
		return projects[i].Name < projects[j].Name
	})
	return s.projectStore.Save(ctx, projects)
}

type VideoProcessingService struct {
	processor videoprocessing.VideoProcessor
}

func NewVideoProcessingService(config ...videoprocessing.FFmpegRuntimeConfig) *VideoProcessingService {
	var cfg videoprocessing.FFmpegRuntimeConfig
	if len(config) > 0 {
		cfg = config[0]
	}
	return &VideoProcessingService{processor: videoprocessing.NewFFmpegCLIProcessor(cfg)}
}

func (s *VideoProcessingService) RuntimeStatus(ctx context.Context) (videoprocessing.RuntimeStatus, error) {
	if s.processor == nil {
		s.processor = videoprocessing.NewFFmpegCLIProcessor(videoprocessing.FFmpegRuntimeConfig{})
	}
	return s.processor.RuntimeStatus(ctx)
}

func (s *VideoProcessingService) CompareBindings(_ context.Context) ([]videoprocessing.BindingEvaluation, error) {
	return videoprocessing.CompareBindings(), nil
}

func (s *VideoProcessingService) Probe(ctx context.Context, input string) (videoprocessing.ProbeResult, error) {
	if s.processor == nil {
		s.processor = videoprocessing.NewFFmpegCLIProcessor(videoprocessing.FFmpegRuntimeConfig{})
	}
	return s.processor.Probe(ctx, input)
}

func (s *VideoProcessingService) Thumbnail(ctx context.Context, req videoprocessing.ThumbnailRequest) error {
	if s.processor == nil {
		s.processor = videoprocessing.NewFFmpegCLIProcessor(videoprocessing.FFmpegRuntimeConfig{})
	}
	return s.processor.Thumbnail(ctx, req)
}

func (s *VideoProcessingService) Render(ctx context.Context, req videoprocessing.RenderRequest) (videoprocessing.RenderJob, error) {
	if s.processor == nil {
		s.processor = videoprocessing.NewFFmpegCLIProcessor(videoprocessing.FFmpegRuntimeConfig{})
	}
	return s.processor.Render(ctx, req)
}

type ProjectLibraryService struct {
	store       library.Store
	oneDrive    *OneDriveService
	mediaClient *mediaservice.Client
	engine      *library.AnalysisEngine
	mu          sync.Mutex
}

// NewProjectLibraryService constructs the project library service and wires
// up its analysis pipeline via the Azure Media Service. mediaClient may be
// nil (e.g. when the media staging service is not yet configured);
// SubmitForAnalysis will return a descriptive error in that case.
func NewProjectLibraryService(store library.Store, oneDrive *OneDriveService, mediaClient *mediaservice.Client) *ProjectLibraryService {
	if store == nil {
		defaultPath, err := library.DefaultLibraryPath()
		if err != nil {
			defaultPath = ""
		}
		store = library.NewFileStore("")
		_ = defaultPath
	}
	var odClient *onedrive.Client
	if oneDrive != nil {
		odClient = oneDrive.DriveClient()
	}
	engine := library.NewAnalysisEngine(store, mediaClient, odClient)
	return &ProjectLibraryService{store: store, oneDrive: oneDrive, mediaClient: mediaClient, engine: engine}
}

// NewLibraryStore creates the shared library store for auto-registration and persistence.
// It reads/writes to the canonical library.json path in app config directory.
func NewLibraryStore() library.Store {
	defaultPath, err := library.DefaultLibraryPath()
	if err != nil {
		defaultPath = ""
	}
	if defaultPath != "" {
		return library.NewFileStore(defaultPath)
	}
	return library.NewFileStore("")
}

func (s *ProjectLibraryService) Current(ctx context.Context) (library.ProjectLibrary, error) {
	assets, err := s.ListAssets(ctx)
	if err != nil {
		return library.ProjectLibrary{}, err
	}
	now := time.Now().UTC()
	return library.ProjectLibrary{ID: "local-scaffold", Name: "AI Video Studio Library", Assets: assets, UpdatedAt: now}, nil
}

func (s *ProjectLibraryService) ListAssets(ctx context.Context) ([]library.ProjectAsset, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.store == nil {
		return []library.ProjectAsset{}, nil
	}
	return s.store.LoadAssets(ctx)
}

func (s *ProjectLibraryService) AddAsset(ctx context.Context, asset library.ProjectAsset) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.store == nil {
		return nil
	}
	return s.store.AddAsset(ctx, asset)
}

// SyncWithOneDrive scans the configured OneDrive folder and registers any
// assets that are not yet in the local store. It returns the number of new
// assets discovered. This is idempotent — duplicates are skipped by DriveItemID.
func (s *ProjectLibraryService) SyncWithOneDrive(ctx context.Context, folderPath string) (added int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.store == nil {
		return 0, fmt.Errorf("library store is not available")
	}
	if s.oneDrive == nil {
		return 0, fmt.Errorf("onedrive service is not available")
	}
	client := s.oneDrive.DriveClient()
	if client == nil {
		return 0, fmt.Errorf("onedrive client is not available")
	}
	return library.SyncAssetsWithOneDrive(ctx, s.store, client, folderPath)
}

// emitAnalysisEvent notifies the frontend of an analysis job update via a
// Wails event. It is a no-op when no application instance is running (e.g.
// during unit tests), so it is always safe to call.
func emitAnalysisEvent(name string, job library.AnalysisJob) {
	app := application.Get()
	if app == nil || app.Event == nil {
		return
	}
	app.Event.Emit(name, job)
}

// SubmitForAnalysis starts an Azure Content Understanding analysis for the
// given asset. It delegates to the Azure Media Service which handles the
// full pipeline (OneDrive → Blob → CU → result). Since the operation can be
// long-running, it's executed in a goroutine and progresses are reported via
// the 'analysis:completed' Wails event with the final result.
func (s *ProjectLibraryService) SubmitForAnalysis(assetID string) (*library.AnalysisJob, error) {
	s.mu.Lock()
	store := s.store
	engine := s.engine
	s.mu.Unlock()

	if store == nil {
		return nil, fmt.Errorf("library store is not available")
	}
	if engine == nil {
		return nil, fmt.Errorf("analysis engine is not available")
	}

	ctx := context.Background()
	assets, err := store.LoadAssets(ctx)
	if err != nil {
		return nil, fmt.Errorf("load assets: %w", err)
	}
	var asset *library.ProjectAsset
	for i := range assets {
		if assets[i].ID == assetID {
			asset = &assets[i]
			break
		}
	}
	if asset == nil {
		return nil, fmt.Errorf("asset %q not found", assetID)
	}

	// Execute asynchronously so the UI stays responsive.
	go func() {
		job, err := engine.SubmitAsset(ctx, *asset)
		if err != nil {
			_ = err
		}
		emitAnalysisEvent("analysis:completed", *job)
	}()
	// Return a pending placeholder immediately — the real job status is
	// accessible through AnalysisJobs/AnalysisResult once the goroutine
	// completes.
	return engine.GetAssetAnalysis(context.Background(), assetID)
}

// AnalysisJobs returns all analysis jobs, ordered by creation time
// (newest first).
func (s *ProjectLibraryService) AnalysisJobs() ([]library.AnalysisJob, error) {
	s.mu.Lock()
	engine := s.engine
	s.mu.Unlock()
	if engine == nil {
		return nil, fmt.Errorf("analysis engine is not available")
	}
	jobs, err := engine.GetJobs(context.Background())
	if err != nil {
		return nil, err
	}
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].CreatedAt.After(jobs[j].CreatedAt) })
	return jobs, nil
}

// AnalysisResult returns the analysis result for a job, or nil if the job is
// still running, failed, or has not yet produced a result.
func (s *ProjectLibraryService) AnalysisResult(jobID string) (*library.AnalysisJob, error) {
	s.mu.Lock()
	engine := s.engine
	s.mu.Unlock()
	if engine == nil {
		return nil, fmt.Errorf("analysis engine is not available")
	}
	return engine.GetJob(context.Background(), jobID)
}

// GetAssetAnalysis returns the most recent analysis job for an asset, or nil
// if the asset has never been submitted for analysis.
func (s *ProjectLibraryService) GetAssetAnalysis(assetID string) (*library.AnalysisJob, error) {
	s.mu.Lock()
	engine := s.engine
	s.mu.Unlock()
	if engine == nil {
		return nil, fmt.Errorf("analysis engine is not available")
	}
	job, err := engine.GetAssetAnalysis(context.Background(), assetID)
	if err != nil {
		return nil, nil
	}
	return job, nil
}

type ProtectedEndpointStatus struct {
	Configured bool   `json:"configured"`
	Endpoint   string `json:"endpoint,omitempty"`
	HasAPIKey  bool   `json:"hasApiKey"`
	Message    string `json:"message"`
}

func protectedEndpointStatus(label, endpoint, apiKey string) ProtectedEndpointStatus {
	status := ProtectedEndpointStatus{
		Endpoint:  strings.TrimRight(strings.TrimSpace(endpoint), "/"),
		HasAPIKey: strings.TrimSpace(apiKey) != "",
	}
	if status.Endpoint != "" {
		normalized, err := videoindexerstudio.NormalizeEndpoint(status.Endpoint)
		if err != nil {
			status.Message = label + " endpoint is invalid."
			return status
		}
		status.Endpoint = normalized
	}
	switch {
	case status.Endpoint == "" && !status.HasAPIKey:
		status.Message = label + " is not configured."
	case status.Endpoint == "":
		status.Message = "Set the " + label + " endpoint."
	case !status.HasAPIKey:
		status.Message = "Set the " + label + " API key."
	default:
		status.Configured = true
		status.Message = label + " is configured."
	}
	return status
}

type SettingsService struct {
	mu      sync.Mutex
	current settings.AppSettings
	store   settings.Store
}

func NewSettingsService(store ...settings.Store) *SettingsService {
	service := &SettingsService{current: defaultSettings()}
	if len(store) > 0 {
		service.store = store[0]
	}
	if service.store == nil {
		service.store = newDefaultSettingsStore()
	}
	return service
}

func (s *SettingsService) Get(ctx context.Context) (settings.AppSettings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.store == nil {
		s.current = settings.Normalize(s.current)
		return s.current, nil
	}
	loaded, err := s.store.Load(ctx)
	if err != nil {
		return settings.AppSettings{}, err
	}
	s.current = loaded
	return loaded, nil
}

func (s *SettingsService) Save(ctx context.Context, next settings.AppSettings) (settings.AppSettings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if next.TenantID == "" {
		next.TenantID = next.GraphAuth.TenantID
	}
	if next.ClientID == "" {
		next.ClientID = next.GraphAuth.ClientID
	}
	next = settings.Normalize(next)
	if s.store == nil {
		s.current = next
		return next, nil
	}
	saved, err := s.store.Save(ctx, next)
	if err != nil {
		return settings.AppSettings{}, err
	}
	s.current = saved
	return s.current, nil
}

// GetMediaServiceEndpoint returns the currently configured Media Service
// endpoint. The API key is intentionally never returned since it is
// json:"-" and must not be exposed to the frontend.
func (s *SettingsService) GetMediaServiceEndpoint(ctx context.Context) (string, error) {
	current, err := s.Get(ctx)
	if err != nil {
		return "", err
	}
	return current.MediaServiceEndpoint, nil
}

// SetMediaServiceEndpoint saves the Media Service endpoint and, optionally, a
// new API key. The API key is passed separately from AppSettings because the
// field is json:"-" and never round-trips through Get/Save. Passing an empty
// apiKey leaves the previously stored key untouched.
func (s *SettingsService) SetMediaServiceEndpoint(ctx context.Context, endpoint string, apiKey string) error {
	current, err := s.Get(ctx)
	if err != nil {
		return err
	}
	current.MediaServiceEndpoint = strings.TrimSpace(endpoint)
	if trimmed := strings.TrimSpace(apiKey); trimmed != "" {
		current.MediaServiceAPIKey = trimmed
	}
	_, err = s.Save(ctx, current)
	return err
}

func (s *SettingsService) GetMediaServiceStatus(ctx context.Context) (ProtectedEndpointStatus, error) {
	current, err := s.Get(ctx)
	if err != nil {
		return ProtectedEndpointStatus{}, err
	}
	return protectedEndpointStatus("Media Service", current.MediaServiceEndpoint, current.MediaServiceAPIKey), nil
}

func (s *SettingsService) GetVideoIndexerServiceEndpoint(ctx context.Context) (string, error) {
	current, err := s.Get(ctx)
	if err != nil {
		return "", err
	}
	return current.VideoIndexerServiceEndpoint, nil
}

func (s *SettingsService) GetVideoIndexerServiceStatus(ctx context.Context) (ProtectedEndpointStatus, error) {
	current, err := s.Get(ctx)
	if err != nil {
		return ProtectedEndpointStatus{}, err
	}
	return protectedEndpointStatus("Video Indexer Studio", current.VideoIndexerServiceEndpoint, current.VideoIndexerServiceAPIKey), nil
}

func (s *SettingsService) SetVideoIndexerServiceEndpoint(ctx context.Context, endpoint string, apiKey string) error {
	current, err := s.Get(ctx)
	if err != nil {
		return err
	}
	normalized, err := videoindexerstudio.NormalizeEndpoint(endpoint)
	if err != nil {
		return err
	}
	current.VideoIndexerServiceEndpoint = normalized
	if trimmed := strings.TrimSpace(apiKey); trimmed != "" {
		current.VideoIndexerServiceAPIKey = trimmed
	}
	_, err = s.Save(ctx, current)
	return err
}

func defaultSettings() settings.AppSettings {
	return settings.AppSettings{
		OneDriveFolder: "AI Video Studio",
		GraphAuth: onedrive.GraphAuthConfig{
			TenantID: onedrive.DefaultTenantID,
			AuthFlow: onedrive.AuthFlowDeviceCode,
			Scopes:   onedrive.DefaultGraphScopes,
		},
		OneDriveDestination: onedrive.OneDriveDestination{
			Mode:        "app_folder",
			DisplayName: "AI Video Studio app folder",
			Path:        "/Apps/AI Video Studio",
		},
		AzureContentUnderstanding: cu.Config{
			AnalyzerID: cu.PrebuiltVideoAnalyzerID,
			APIVersion: cu.DefaultAPIVersion,
			SourceMode: "https_url",
		},
		ChunkSizeBytes:       10 * 1024 * 1024,
		MaxConcurrentImports: 1,
	}
}

func newDefaultSettingsStore() settings.Store {
	path, err := settings.DefaultPath()
	if err != nil {
		return nil
	}
	return settings.NewFileStore(path, defaultSettings())
}

func NewSettingsStore() settings.Store {
	return newDefaultSettingsStore()
}
