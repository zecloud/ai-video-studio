//go:build windows

package dji

import (
	"context"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	"tinygo.org/x/bluetooth"
)

type WindowsBLEAdapter struct {
	adapter          *bluetooth.Adapter
	profile          ProtocolProfile
	scanDuration     time.Duration
	notificationWait time.Duration
	mu               sync.Mutex
}

func NewWindowsBLEAdapter() *WindowsBLEAdapter {
	return &WindowsBLEAdapter{
		adapter:          bluetooth.DefaultAdapter,
		profile:          WindowsBLEProtocolProfile(),
		scanDuration:     8 * time.Second,
		notificationWait: 5 * time.Second,
	}
}

func WindowsBLEProtocolProfile() ProtocolProfile {
	profile := DefaultProtocolProfile()
	profile.Implementation = "windows-tinygo-ble"
	for i := range profile.CommandCatalog {
		switch profile.CommandCatalog[i].ID {
		case "ble-scan", "ble-pair":
			profile.CommandCatalog[i].Implemented = true
		}
	}
	return profile
}

func (a *WindowsBLEAdapter) Status(ctx context.Context) (ControlStatus, error) {
	if err := ctx.Err(); err != nil {
		return ControlStatus{}, err
	}
	if err := a.enable(); err != nil {
		return ControlStatus{}, fmt.Errorf("enable Windows BLE adapter: %w", err)
	}
	return ControlStatus{
		BLEAvailable:      true,
		WiFiAvailable:     false,
		AdapterConfigured: true,
		BLEAdapterName:    "Windows Bluetooth LE",
		Protocol:          a.profileCopy(),
		Message:           "Windows Bluetooth LE adapter is configured. Scan/pair can be tested with a nearby Osmo Action 4.",
	}, nil
}

func (a *WindowsBLEAdapter) Scan(ctx context.Context) ([]BLEDevice, error) {
	if err := a.enable(); err != nil {
		return nil, fmt.Errorf("enable Windows BLE adapter: %w", err)
	}
	a.mu.Lock()
	defer a.mu.Unlock()

	scanCtx := ctx
	cancel := func() {}
	if _, ok := ctx.Deadline(); !ok {
		scanCtx, cancel = context.WithTimeout(ctx, a.scanDuration)
	}
	defer cancel()

	devicesByID := map[string]BLEDevice{}
	var devicesMu sync.Mutex
	done := make(chan error, 1)
	go func() {
		done <- a.adapter.Scan(func(adapter *bluetooth.Adapter, result bluetooth.ScanResult) {
			device := bleDeviceFromScan(result)
			devicesMu.Lock()
			previous, exists := devicesByID[device.ID]
			if !exists || device.RSSI > previous.RSSI {
				devicesByID[device.ID] = device
			}
			devicesMu.Unlock()
		})
	}()

	select {
	case <-scanCtx.Done():
		_ = a.adapter.StopScan()
		if err := <-done; err != nil && !strings.Contains(err.Error(), "stopped") {
			return nil, fmt.Errorf("stop Windows BLE scan: %w", err)
		}
	case err := <-done:
		if err != nil {
			return nil, fmt.Errorf("Windows BLE scan: %w", err)
		}
	}

	devicesMu.Lock()
	defer devicesMu.Unlock()
	devices := make([]BLEDevice, 0, len(devicesByID))
	for _, device := range devicesByID {
		devices = append(devices, device)
	}
	return devices, nil
}

func (a *WindowsBLEAdapter) Pair(ctx context.Context, req PairingRequest) (PairingResult, error) {
	deviceID := strings.TrimSpace(req.DeviceID)
	if deviceID == "" {
		return PairingResult{Paired: false, RequiresUserConfirmation: true, Message: "Select a scanned BLE device before pairing."}, fmt.Errorf("device ID is required")
	}
	pin := strings.TrimSpace(req.PIN)
	if pin == "" {
		pin = a.profile.BLE.DefaultPIN
	}
	if err := a.enable(); err != nil {
		return PairingResult{DeviceID: deviceID, Paired: false, RequiresUserConfirmation: true}, fmt.Errorf("enable Windows BLE adapter: %w", err)
	}
	a.mu.Lock()
	defer a.mu.Unlock()

	if _, err := bluetooth.ParseMAC(deviceID); err != nil {
		return PairingResult{DeviceID: deviceID, Paired: false, RequiresUserConfirmation: true}, fmt.Errorf("invalid BLE address %q: %w", deviceID, err)
	}
	address := bluetooth.Address{}
	address.Set(deviceID)
	device, err := a.adapter.Connect(address, bluetooth.ConnectionParams{})
	if err != nil {
		return PairingResult{DeviceID: deviceID, Paired: false, RequiresUserConfirmation: true}, fmt.Errorf("connect to BLE device %s: %w", deviceID, err)
	}
	defer device.Disconnect()

	serviceUUID := bluetooth.New16BitUUID(0xfff0)
	writeUUID := bluetooth.New16BitUUID(0xfff3)
	pairingUUID := bluetooth.New16BitUUID(0xfff4)
	statusUUID := bluetooth.New16BitUUID(0xfff5)

	services, err := device.DiscoverServices([]bluetooth.UUID{serviceUUID})
	if err != nil {
		return PairingResult{DeviceID: deviceID, Paired: false, RequiresUserConfirmation: true}, fmt.Errorf("discover DJI GATT service fff0: %w", err)
	}
	if len(services) == 0 {
		return PairingResult{DeviceID: deviceID, Paired: false, RequiresUserConfirmation: true}, fmt.Errorf("DJI GATT service fff0 was not found on %s", deviceID)
	}
	chars, err := services[0].DiscoverCharacteristics([]bluetooth.UUID{writeUUID, pairingUUID, statusUUID})
	if err != nil {
		return PairingResult{DeviceID: deviceID, Paired: false, RequiresUserConfirmation: true}, fmt.Errorf("discover DJI characteristics fff3/fff4/fff5: %w", err)
	}
	if len(chars) < 3 {
		return PairingResult{DeviceID: deviceID, Paired: false, RequiresUserConfirmation: true}, fmt.Errorf("expected DJI characteristics fff3/fff4/fff5, found %d", len(chars))
	}

	writeChar := chars[0]
	notifyCh := make(chan bleNotification, 8)
	notify := func(source string) func([]byte) {
		return func(buf []byte) {
			copied := append([]byte(nil), buf...)
			select {
			case notifyCh <- bleNotification{source: source, packet: copied}:
			default:
			}
		}
	}
	if err := chars[1].EnableNotifications(notify(BLEPairingCharUUID)); err != nil {
		return PairingResult{DeviceID: deviceID, Paired: false, RequiresUserConfirmation: true}, fmt.Errorf("enable DJI pairing notifications fff4: %w", err)
	}
	if err := chars[2].EnableNotifications(notify(BLEStatusCharUUID)); err != nil {
		return PairingResult{DeviceID: deviceID, Paired: false, RequiresUserConfirmation: true}, fmt.Errorf("enable DJI status notifications fff5: %w", err)
	}

	initial, initialErr := waitForNotification(ctx, notifyCh, BLEPairingCharUUID, minDuration(1500*time.Millisecond, a.notificationWait))
	if initialErr != nil && ctx.Err() != nil {
		return PairingResult{DeviceID: deviceID, Paired: false, RequiresUserConfirmation: true, Message: "Pairing was cancelled before writing the DJI pair message."}, ctx.Err()
	}

	payload, err := encodePairMessage(pin)
	if err != nil {
		return PairingResult{DeviceID: deviceID, Paired: false, RequiresUserConfirmation: true}, err
	}
	if _, err := writeChar.WriteWithoutResponse(payload); err != nil {
		if _, writeErr := writeChar.Write(payload); writeErr != nil {
			return PairingResult{DeviceID: deviceID, Paired: false, RequiresUserConfirmation: true}, fmt.Errorf("write DJI pair message to fff3: %w; fallback write: %w", err, writeErr)
		}
	}

	response, err := waitForNotification(ctx, notifyCh, "", a.notificationWait)
	if err != nil {
		return PairingResult{
			DeviceID:                 deviceID,
			Paired:                   false,
			RequiresUserConfirmation: true,
			Message:                  fmt.Sprintf("DJI pair message was written to fff3 %s, but no fff4/fff5 response was received yet; keep the camera awake and retry if needed. Detail: %v", describeInitialPairingNotification(initial, initialErr), err),
		}, nil
	}

	parsed, parseErr := parseDjiMessage(response.packet)
	if parseErr != nil {
		return PairingResult{
			DeviceID:                 deviceID,
			Paired:                   false,
			RequiresUserConfirmation: true,
			Message:                  fmt.Sprintf("Received %s notification after pair write, but it is not a valid DJI message yet: %s (%v).", response.source, strings.ToUpper(hex.EncodeToString(response.packet)), parseErr),
		}, nil
	}
	paired := parsed.ID == pairTransactionID && len(parsed.Payload) == 2 && parsed.Payload[0] == 0x00 && parsed.Payload[1] == 0x01
	return PairingResult{
		DeviceID:                 deviceID,
		Paired:                   paired,
		RequiresUserConfirmation: !paired,
		Message:                  fmt.Sprintf("Received %s DJI response target=0x%04X id=0x%04X type=0x%06X payload=%s after pair write %s.", response.source, parsed.Target, parsed.ID, parsed.Type, strings.ToUpper(hex.EncodeToString(parsed.Payload)), describeInitialPairingNotification(initial, initialErr)),
	}, nil
}

type bleNotification struct {
	source string
	packet []byte
}

func waitForNotification(ctx context.Context, notifyCh <-chan bleNotification, source string, wait time.Duration) (bleNotification, error) {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for {
		select {
		case packet := <-notifyCh:
			if source == "" || packet.source == source {
				return packet, nil
			}
		case <-ctx.Done():
			return bleNotification{}, ctx.Err()
		case <-timer.C:
			if source == "" {
				return bleNotification{}, fmt.Errorf("timeout waiting for fff4/fff5 notification")
			}
			return bleNotification{}, fmt.Errorf("timeout waiting for %s notification", source)
		}
	}
}

func describeInitialPairingNotification(initial bleNotification, err error) string {
	if err != nil {
		return "(no initial fff4 notification observed before write)"
	}
	return fmt.Sprintf("(after initial %s notification %s)", initial.source, strings.ToUpper(hex.EncodeToString(initial.packet)))
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

func (a *WindowsBLEAdapter) enable() error {
	if a == nil || a.adapter == nil {
		return fmt.Errorf("%w: Windows BLE adapter is nil", ErrAdapterNotConfigured)
	}
	if err := a.adapter.Enable(); err != nil && !isBenignWindowsRuntimeInitError(err) {
		return err
	}
	return nil
}

func isBenignWindowsRuntimeInitError(err error) bool {
	type hresultError interface {
		Code() uintptr
	}
	if typed, ok := err.(hresultError); ok {
		switch typed.Code() {
		case 0x00000001, // S_FALSE: runtime was already initialized on this thread.
			0x80010106, // RPC_E_CHANGED_MODE: host already selected a COM apartment model.
			0x80070001: // HRESULT_FROM_WIN32(ERROR_INVALID_FUNCTION), observed as "Fonction incorrecte".
			return true
		}
	}
	message := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(message, "incorrect function") ||
		strings.Contains(message, "fonction incorrecte") ||
		strings.Contains(message, "changed mode") ||
		strings.Contains(message, "already initialized")
}

func (a *WindowsBLEAdapter) profileCopy() ProtocolProfile {
	if a == nil || a.profile.ModelHint == "" {
		return WindowsBLEProtocolProfile()
	}
	return cloneProtocolProfile(a.profile)
}

func bleDeviceFromScan(result bluetooth.ScanResult) BLEDevice {
	name := strings.TrimSpace(result.LocalName())
	serviceUUIDs := make([]string, 0, len(result.ServiceUUIDs()))
	for _, uuid := range result.ServiceUUIDs() {
		serviceUUIDs = append(serviceUUIDs, strings.ToLower(uuid.String()))
	}
	manufacturerHex := manufacturerDataHex(result.ManufacturerData())
	model := "BLE peripheral"
	if strings.Contains(strings.ToLower(name), "osmo") || strings.Contains(strings.ToLower(name), "dji") || hasDJIService(serviceUUIDs) {
		model = OsmoAction4Model + " candidate"
	}
	return BLEDevice{
		ID:                  strings.ToUpper(result.Address.String()),
		Name:                name,
		Model:               model,
		RSSI:                int(result.RSSI),
		Address:             strings.ToUpper(result.Address.String()),
		ManufacturerDataHex: manufacturerHex,
		ServiceUUIDs:        serviceUUIDs,
		Paired:              false,
		LastSeen:            time.Now().UTC(),
	}
}

func manufacturerDataHex(elements []bluetooth.ManufacturerDataElement) string {
	if len(elements) == 0 {
		return ""
	}
	parts := make([]string, 0, len(elements))
	for _, element := range elements {
		parts = append(parts, fmt.Sprintf("%04X:%s", element.CompanyID, strings.ToUpper(hex.EncodeToString(element.Data))))
	}
	return strings.Join(parts, ";")
}

func hasDJIService(serviceUUIDs []string) bool {
	for _, uuid := range serviceUUIDs {
		normalized := strings.ToLower(strings.TrimSpace(uuid))
		if normalized == BLEGATTServiceUUID || strings.HasPrefix(normalized, "0000"+BLEGATTServiceUUID+"-") {
			return true
		}
	}
	return false
}
