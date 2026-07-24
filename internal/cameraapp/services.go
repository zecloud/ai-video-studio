package cameraapp

import (
	"context"
	"time"

	"github.com/zecloud/ai-video-studio/internal/camera"
	"github.com/zecloud/ai-video-studio/internal/dji"
)

// CameraService is the Wails-facing camera facade. It deliberately contains
// no cloud, library, or editing dependencies so the camera binary stays local.
type CameraService struct{ mediaConnector *camera.OsmoHTTPConnector }

func NewCameraService() *CameraService {
	return &CameraService{mediaConnector: camera.NewOsmoHTTPConnector()}
}
func (s *CameraService) GetConnectionStatus(context.Context) (camera.ConnectionStatus, error) {
	return camera.ConnectionStatus{Available: false, Message: "Camera connector can construct and probe Osmo HTTP media requests; BLE/Wi-Fi pairing and real hardware validation are still pending."}, nil
}
func (s *CameraService) Discover(context.Context) ([]camera.CameraDevice, error) {
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
func (s *CameraService) ListMedia(context.Context, string) ([]camera.CameraMediaItem, error) {
	return []camera.CameraMediaItem{}, nil
}
func (s *CameraService) ValidationChecklist(context.Context) ([]camera.ValidationStep, error) {
	return []camera.ValidationStep{
		{ID: "ble-scan", Label: "BLE scan", Status: "pending", Description: "Detect Osmo Action 4 advertisements and model metadata before pairing."},
		{ID: "ble-pair", Label: "BLE pairing", Status: "pending", Description: "Pair without persisting secrets; confirm pairing failure and retry states."},
		{ID: "wifi-ap", Label: "Wi-Fi/AP setup", Status: "pending", Description: "Enable or join the camera AP and record SSID, IP, and diagnostic state."},
		{ID: "media-endpoint", Label: "HTTP /v2 endpoint", Status: "pending", Description: "Probe the Osmo HTTP media endpoint."},
		{ID: "head-range", Label: "HEAD and Range", Status: "pending", Description: "Validate content length, type, byte ranges, 206 responses, and retry behavior."},
		{ID: "media-enumeration", Label: "Media discovery", Status: "pending", Description: "Validate storage IDs, paths, file names, thumbnails, and fallback enumeration."},
	}, nil
}

type DJIControlService struct{ controller dji.Controller }

func NewDJIControlService(controller ...dji.Controller) *DJIControlService {
	s := &DJIControlService{}
	if len(controller) > 0 {
		s.controller = controller[0]
	}
	if s.controller == nil {
		s.controller = dji.NewDefaultController()
	}
	return s
}
func (s *DJIControlService) controllerOrDefault() dji.Controller {
	if s == nil || s.controller == nil {
		return dji.NewDefaultController()
	}
	return s.controller
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
func (s *DJIControlService) RunDiagnostics(ctx context.Context, id string) (dji.DiagnosticResult, error) {
	return s.controllerOrDefault().RunDiagnostics(ctx, id)
}
func (s *DJIControlService) ProtocolProfile(ctx context.Context) (dji.ProtocolProfile, error) {
	return s.controllerOrDefault().ProtocolProfile(ctx)
}
