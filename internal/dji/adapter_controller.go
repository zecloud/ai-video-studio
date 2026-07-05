package dji

import (
	"context"
	"fmt"
	"strings"
	"time"
)

type AdapterController struct {
	profile ProtocolProfile
	ble     BLEAdapter
	wifi    WiFiConfigurator
}

func NewAdapterController(profile ProtocolProfile, ble BLEAdapter, wifi WiFiConfigurator) *AdapterController {
	if profile.ModelHint == "" {
		profile = DefaultProtocolProfile()
	}
	return &AdapterController{
		profile: cloneProtocolProfile(profile),
		ble:     ble,
		wifi:    wifi,
	}
}

func (c *AdapterController) Status(ctx context.Context) (ControlStatus, error) {
	if c == nil || c.ble == nil {
		return NewNoopController().Status(ctx)
	}
	status, err := c.ble.Status(ctx)
	if err != nil {
		return ControlStatus{}, err
	}
	status.Protocol = c.protocol()
	return status, nil
}

func (c *AdapterController) ScanBLE(ctx context.Context) ([]BLEDevice, error) {
	if c == nil || c.ble == nil {
		return nil, fmt.Errorf("%w: BLE scan requires a platform BLE adapter", ErrAdapterNotConfigured)
	}
	return c.ble.Scan(ctx)
}

func (c *AdapterController) Pair(ctx context.Context, req PairingRequest) (PairingResult, error) {
	if c == nil || c.ble == nil {
		return PairingResult{
			DeviceID:                 strings.TrimSpace(req.DeviceID),
			Paired:                   false,
			RequiresUserConfirmation: true,
			Message:                  "BLE pairing requires a configured DJI BLE adapter and real Osmo Action hardware.",
		}, fmt.Errorf("%w: BLE pairing requires a platform BLE adapter", ErrAdapterNotConfigured)
	}
	if strings.TrimSpace(req.PIN) == "" {
		req.PIN = c.protocol().BLE.DefaultPIN
	}
	return c.ble.Pair(ctx, req)
}

func (c *AdapterController) SetupWiFi(ctx context.Context, req WiFiSetupRequest) (WiFiProfile, error) {
	profile := c.protocol()
	if c == nil || c.wifi == nil {
		return WiFiProfile{
			SSID:       strings.TrimSpace(req.SSID),
			IPAddress:  profile.DefaultIP,
			GatewayIP:  profile.DefaultIP,
			Port:       DefaultMediaPort,
			UDPPort:    profile.UDPPort,
			MediaPorts: append([]int(nil), profile.MediaPorts...),
			Message:    "BLE scan/pairing is configured on Windows, but the Osmo Wi-Fi setup DUML command is still pending hardware validation.",
		}, fmt.Errorf("%w: Wi-Fi/AP setup command is not implemented yet", ErrAdapterNotConfigured)
	}
	return c.wifi.SetupWiFi(ctx, req)
}

func (c *AdapterController) RunDiagnostics(ctx context.Context, deviceID string) (DiagnosticResult, error) {
	deviceID = strings.TrimSpace(deviceID)
	if deviceID == "" {
		deviceID = "unselected-device"
	}

	status, err := c.Status(ctx)
	if err != nil {
		return DiagnosticResult{}, err
	}

	diagnostics := DiagnosticResult{
		Steps: []DiagnosticStep{
			{ID: "ble-adapter", Label: "BLE adapter", Status: DiagnosticBlocked, Transport: TransportBLE, Description: status.Message},
			{ID: "ble-scan", Label: "Scan BLE advertisements", Status: DiagnosticPending, Transport: TransportBLE, Description: "Passive Windows BLE scan for Osmo/DJI advertisements and the fff0 service."},
			{ID: "ble-service", Label: "DJI GATT service", Status: DiagnosticPending, Transport: TransportBLE, Description: "Expected service fff0 with write characteristic fff3 and pairing/status notifications on fff4/fff5."},
			{ID: "pairing", Label: "Pairing flow", Status: DiagnosticPending, Transport: TransportBLE, Description: "Pairing is only attempted when explicitly requested from the UI."},
			{ID: "wifi-ap", Label: "Camera Wi-Fi/AP", Status: DiagnosticPending, Transport: TransportBLE, Description: "Expected camera gateway is 192.168.2.1 after AP setup or manual Wi-Fi join."},
			{ID: "udp-9004", Label: "DUML UDP diagnostics", Status: DiagnosticPending, Transport: TransportUDP, Description: "UDP 9004 remains reserved for future diagnostics/file-list experiments after BLE/AP validation."},
			{ID: "http-media", Label: "HTTP media probe", Status: DiagnosticAvailable, Transport: TransportHTTP, Description: "The camera package can already probe /v2 media paths on candidate ports with HEAD and byte ranges."},
		},
		Message: fmt.Sprintf("Windows BLE diagnostics ready for %s; pairing/write is only sent from the explicit Pair action.", deviceID),
	}
	if status.BLEAvailable {
		diagnostics.Steps[0].Status = DiagnosticAvailable
		diagnostics.Steps[0].Description = status.Message
	}

	scanCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	devices, err := c.ScanBLE(scanCtx)
	if err != nil {
		diagnostics.Steps[1].Status = DiagnosticBlocked
		diagnostics.Steps[1].Description = fmt.Sprintf("BLE scan failed: %v", err)
		return diagnostics, nil
	}

	diagnostics.BLEScanOK = true
	diagnostics.Steps[1].Status = DiagnosticAvailable
	diagnostics.Steps[1].Description = fmt.Sprintf("Windows BLE scan completed and returned %d peripheral(s).", len(devices))
	for _, device := range devices {
		if isOsmoBLEDevice(device) {
			diagnostics.Steps[2].Status = DiagnosticAvailable
			diagnostics.Steps[2].Description = fmt.Sprintf("Found DJI/Osmo candidate %q at %s.", device.Name, device.ID)
			break
		}
	}
	return diagnostics, nil
}

func (c *AdapterController) ProtocolProfile(_ context.Context) (ProtocolProfile, error) {
	return c.protocol(), nil
}

func (c *AdapterController) protocol() ProtocolProfile {
	if c == nil || c.profile.ModelHint == "" {
		return DefaultProtocolProfile()
	}
	return cloneProtocolProfile(c.profile)
}

func cloneProtocolProfile(profile ProtocolProfile) ProtocolProfile {
	profile.MediaPorts = append([]int(nil), profile.MediaPorts...)
	profile.CommandCatalog = append([]DUMLCommandDescriptor(nil), profile.CommandCatalog...)
	return profile
}

func isOsmoBLEDevice(device BLEDevice) bool {
	name := strings.ToLower(device.Name)
	model := strings.ToLower(device.Model)
	if strings.Contains(name, "osmo") || strings.Contains(name, "dji") || strings.Contains(name, "action") {
		return true
	}
	if strings.Contains(model, "osmo") || strings.Contains(model, "dji") {
		return true
	}
	for _, uuid := range device.ServiceUUIDs {
		normalized := strings.ToLower(strings.TrimSpace(uuid))
		if normalized == BLEGATTServiceUUID || strings.HasPrefix(normalized, "0000"+BLEGATTServiceUUID+"-") {
			return true
		}
	}
	return false
}
