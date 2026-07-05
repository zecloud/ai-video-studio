package dji

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"testing"
)

func TestDefaultProtocolProfileIncludesOsmoBLEAndDiagnostics(t *testing.T) {
	profile := DefaultProtocolProfile()
	if profile.ModelHint != OsmoAction4Model {
		t.Fatalf("ModelHint = %q", profile.ModelHint)
	}
	if profile.BLE.ServiceUUID != BLEGATTServiceUUID || profile.BLE.WriteCharUUID != BLEWriteCharUUID {
		t.Fatalf("unexpected BLE profile: %+v", profile.BLE)
	}
	if profile.BLE.PairingCharUUID != BLEPairingCharUUID || profile.BLE.StatusCharUUID != BLEStatusCharUUID {
		t.Fatalf("unexpected BLE notify characteristics: %+v", profile.BLE)
	}
	if profile.DefaultIP != DefaultCameraIP || profile.UDPPort != DefaultUDPPort {
		t.Fatalf("unexpected network defaults: %+v", profile)
	}
	if len(profile.MediaPorts) != 2 || profile.MediaPorts[0] != DefaultMediaPort || profile.MediaPorts[1] != AlternateMediaPort {
		t.Fatalf("unexpected media ports: %+v", profile.MediaPorts)
	}
	if len(profile.CommandCatalog) == 0 {
		t.Fatal("expected command catalog")
	}
	var hasHTTPProbe bool
	for _, command := range profile.CommandCatalog {
		if command.ID == "http-media-probe" && command.Implemented {
			hasHTTPProbe = true
		}
	}
	if !hasHTTPProbe {
		t.Fatalf("expected implemented HTTP media probe command in catalog: %+v", profile.CommandCatalog)
	}
}

func TestNoopControllerBlocksHardwareCommands(t *testing.T) {
	controller := NewNoopController()
	status, err := controller.Status(context.Background())
	if err != nil {
		t.Fatalf("Status returned error: %v", err)
	}
	if status.AdapterConfigured || status.BLEAvailable || status.WiFiAvailable {
		t.Fatalf("safe noop status must not claim hardware availability: %+v", status)
	}
	if _, err := controller.ScanBLE(context.Background()); !errors.Is(err, ErrAdapterNotConfigured) {
		t.Fatalf("ScanBLE error = %v, want ErrAdapterNotConfigured", err)
	}
	pairing, err := controller.Pair(context.Background(), PairingRequest{DeviceID: "camera-1"})
	if !errors.Is(err, ErrAdapterNotConfigured) {
		t.Fatalf("Pair error = %v, want ErrAdapterNotConfigured", err)
	}
	if pairing.Paired {
		t.Fatalf("Pair returned success-shaped result: %+v", pairing)
	}
	wifi, err := controller.SetupWiFi(context.Background(), WiFiSetupRequest{DeviceID: "camera-1"})
	if !errors.Is(err, ErrAdapterNotConfigured) {
		t.Fatalf("SetupWiFi error = %v, want ErrAdapterNotConfigured", err)
	}
	if wifi.IPAddress != DefaultCameraIP || len(wifi.MediaPorts) != 2 {
		t.Fatalf("SetupWiFi should return diagnostic defaults: %+v", wifi)
	}
}

func TestNoopControllerDiagnosticsAreNonHardware(t *testing.T) {
	controller := NewNoopController()
	diagnostics, err := controller.RunDiagnostics(context.Background(), "camera-1")
	if err != nil {
		t.Fatalf("RunDiagnostics returned error: %v", err)
	}
	if diagnostics.BLEScanOK || diagnostics.PairingOK || diagnostics.WiFiSetupOK || diagnostics.UDP9004OK {
		t.Fatalf("diagnostics must not report unverified success: %+v", diagnostics)
	}
	if len(diagnostics.Steps) < 5 {
		t.Fatalf("expected detailed diagnostics steps: %+v", diagnostics.Steps)
	}
	var hasHTTPAvailable bool
	for _, step := range diagnostics.Steps {
		if step.ID == "http-media" && step.Status == DiagnosticAvailable {
			hasHTTPAvailable = true
		}
	}
	if !hasHTTPAvailable {
		t.Fatalf("expected HTTP media step to be available: %+v", diagnostics.Steps)
	}
}

func TestNoopControllerProfileDefensiveCopies(t *testing.T) {
	controller := NewNoopController()
	profile, err := controller.ProtocolProfile(context.Background())
	if err != nil {
		t.Fatalf("ProtocolProfile returned error: %v", err)
	}
	profile.MediaPorts[0] = 9999
	profile.CommandCatalog[0].ID = "mutated"

	next, err := controller.ProtocolProfile(context.Background())
	if err != nil {
		t.Fatalf("ProtocolProfile returned error on second call: %v", err)
	}
	if next.MediaPorts[0] == 9999 || next.CommandCatalog[0].ID == "mutated" {
		t.Fatalf("ProtocolProfile returned shared mutable data: %+v", next)
	}
}

func TestAdapterControllerUsesBLEAdapterWithoutFalseWiFiSuccess(t *testing.T) {
	controller := NewAdapterController(DefaultProtocolProfile(), fakeBLEAdapter{}, nil)

	status, err := controller.Status(context.Background())
	if err != nil {
		t.Fatalf("Status returned error: %v", err)
	}
	if !status.AdapterConfigured || !status.BLEAvailable {
		t.Fatalf("adapter controller should report fake BLE availability: %+v", status)
	}

	devices, err := controller.ScanBLE(context.Background())
	if err != nil {
		t.Fatalf("ScanBLE returned error: %v", err)
	}
	if len(devices) != 1 || !isOsmoBLEDevice(devices[0]) {
		t.Fatalf("expected one Osmo-like BLE device, got %+v", devices)
	}

	pairing, err := controller.Pair(context.Background(), PairingRequest{DeviceID: "AA:BB:CC:DD:EE:FF"})
	if err != nil {
		t.Fatalf("Pair returned error: %v", err)
	}
	if pairing.Paired {
		t.Fatalf("fake adapter intentionally does not report unverified pairing success: %+v", pairing)
	}

	wifi, err := controller.SetupWiFi(context.Background(), WiFiSetupRequest{DeviceID: devices[0].ID})
	if !errors.Is(err, ErrAdapterNotConfigured) {
		t.Fatalf("SetupWiFi error = %v, want ErrAdapterNotConfigured", err)
	}
	if wifi.IPAddress != DefaultCameraIP {
		t.Fatalf("SetupWiFi should only return diagnostic defaults: %+v", wifi)
	}
}

func TestEncodePairMessageUsesDjiFraming(t *testing.T) {
	packet, err := encodePairMessage(DefaultPairingPIN)
	if err != nil {
		t.Fatalf("encodePairMessage returned error: %v", err)
	}
	if string(packet) == DefaultPairingPIN {
		t.Fatal("pairing packet must not be the raw PIN")
	}
	if len(packet) != 13+len(pairPayloadPrefix)+1+len(DefaultPairingPIN) {
		t.Fatalf("encoded packet length = %d", len(packet))
	}
	if packet[0] != 0x55 || packet[2] != 0x04 {
		t.Fatalf("unexpected DJI message header: %s", hex.EncodeToString(packet[:4]))
	}
	if int(packet[1]) != len(packet) {
		t.Fatalf("length byte = %d, want %d", packet[1], len(packet))
	}

	decoded, err := parseDjiMessage(packet)
	if err != nil {
		t.Fatalf("parseDjiMessage returned error: %v", err)
	}
	if decoded.Target != pairTarget || decoded.ID != pairTransactionID || decoded.Type != pairType {
		t.Fatalf("unexpected pair message identifiers: %+v", decoded)
	}
	if got, want := decoded.Payload[:len(pairPayloadPrefix)], pairPayloadPrefix; !equalBytes(got, want) {
		t.Fatalf("unexpected pair payload prefix: got=%s want=%s", hex.EncodeToString(got), hex.EncodeToString(want))
	}
	packedPin := decoded.Payload[len(pairPayloadPrefix):]
	if len(packedPin) != 1+len(DefaultPairingPIN) || int(packedPin[0]) != len(DefaultPairingPIN) || string(packedPin[1:]) != DefaultPairingPIN {
		t.Fatalf("unexpected packed PIN payload: %s", hex.EncodeToString(packedPin))
	}
}

func TestParseDjiMessageRejectsBadCRC(t *testing.T) {
	packet, err := encodePairMessage(DefaultPairingPIN)
	if err != nil {
		t.Fatalf("encodePairMessage returned error: %v", err)
	}
	packet[len(packet)-1] ^= 0xff
	if _, err := parseDjiMessage(packet); err == nil {
		t.Fatal("parseDjiMessage accepted a message with a corrupted CRC")
	}
}

func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

type fakeBLEAdapter struct{}

func (fakeBLEAdapter) Status(context.Context) (ControlStatus, error) {
	return ControlStatus{
		BLEAvailable:      true,
		AdapterConfigured: true,
		Protocol:          DefaultProtocolProfile(),
		Message:           "fake BLE adapter ready",
	}, nil
}

func (fakeBLEAdapter) Scan(context.Context) ([]BLEDevice, error) {
	return []BLEDevice{{
		ID:           "AA:BB:CC:DD:EE:FF",
		Name:         "DJI Osmo Action 4",
		Model:        OsmoAction4Model,
		RSSI:         -42,
		Address:      "AA:BB:CC:DD:EE:FF",
		ServiceUUIDs: []string{"0000fff0-0000-1000-8000-00805f9b34fb"},
	}}, nil
}

func (fakeBLEAdapter) Pair(_ context.Context, req PairingRequest) (PairingResult, error) {
	if req.DeviceID == "" {
		return PairingResult{}, fmt.Errorf("device ID is required")
	}
	return PairingResult{
		DeviceID:                 req.DeviceID,
		Paired:                   false,
		RequiresUserConfirmation: true,
		Message:                  "fake GATT readiness only",
	}, nil
}
