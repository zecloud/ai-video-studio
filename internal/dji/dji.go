package dji

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	DefaultCameraIP     = "192.168.2.1"
	DefaultMediaPort    = 80
	AlternateMediaPort  = 7001
	DefaultUDPPort      = 9004
	DefaultPairingPIN   = "love"
	OsmoAction4Model    = "Osmo Action 4"
	BLEGATTServiceUUID  = "fff0"
	BLEWriteCharUUID    = "fff3"
	BLEPairingCharUUID  = "fff4"
	BLEStatusCharUUID   = "fff5"
	TransportBLE        = "ble"
	TransportUDP        = "udp"
	TransportHTTP       = "http"
	DiagnosticPending   = "pending"
	DiagnosticBlocked   = "blocked"
	DiagnosticAvailable = "available"
)

var ErrAdapterNotConfigured = errors.New("dji hardware adapter is not configured")

type BLEDevice struct {
	ID                  string    `json:"id"`
	Name                string    `json:"name"`
	Model               string    `json:"model"`
	RSSI                int       `json:"rssi"`
	Address             string    `json:"address,omitempty"`
	ManufacturerDataHex string    `json:"manufacturerDataHex,omitempty"`
	ServiceUUIDs        []string  `json:"serviceUuids,omitempty"`
	Paired              bool      `json:"paired"`
	LastSeen            time.Time `json:"lastSeen,omitempty"`
	PairingPIN          string    `json:"-"`
}

type WiFiProfile struct {
	SSID       string `json:"ssid"`
	IPAddress  string `json:"ipAddress"`
	GatewayIP  string `json:"gatewayIp,omitempty"`
	Port       int    `json:"port,omitempty"`
	UDPPort    int    `json:"udpPort,omitempty"`
	MediaPorts []int  `json:"mediaPorts,omitempty"`
	Message    string `json:"message,omitempty"`
}

type ControlStatus struct {
	BLEAvailable      bool            `json:"bleAvailable"`
	WiFiAvailable     bool            `json:"wifiAvailable"`
	AdapterConfigured bool            `json:"adapterConfigured"`
	BLEAdapterName    string          `json:"bleAdapterName,omitempty"`
	Protocol          ProtocolProfile `json:"protocol"`
	Message           string          `json:"message"`
}

type PairingRequest struct {
	DeviceID string `json:"deviceId"`
	PIN      string `json:"pin,omitempty"`
}

type PairingResult struct {
	DeviceID                 string `json:"deviceId,omitempty"`
	Paired                   bool   `json:"paired"`
	RequiresUserConfirmation bool   `json:"requiresUserConfirmation"`
	Message                  string `json:"message"`
}

type WiFiSetupRequest struct {
	DeviceID string `json:"deviceId"`
	SSID     string `json:"ssid,omitempty"`
}

type DiagnosticStep struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Status      string `json:"status"`
	Transport   string `json:"transport,omitempty"`
	Description string `json:"description"`
}

type DiagnosticResult struct {
	BLEScanOK      bool             `json:"bleScanOk"`
	PairingOK      bool             `json:"pairingOk"`
	WiFiSetupOK    bool             `json:"wifiSetupOk"`
	UDP9004OK      bool             `json:"udp9004Ok"`
	TCPMediaPortOK bool             `json:"tcpMediaPortOk"`
	Steps          []DiagnosticStep `json:"steps"`
	Message        string           `json:"message"`
}

type BLEProfile struct {
	ServiceUUID     string `json:"serviceUuid"`
	WriteCharUUID   string `json:"writeCharUuid"`
	PairingCharUUID string `json:"pairingCharUuid"`
	StatusCharUUID  string `json:"statusCharUuid"`
	DefaultPIN      string `json:"defaultPin"`
}

type DUMLCommandDescriptor struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Transport   string `json:"transport"`
	Purpose     string `json:"purpose"`
	Source      string `json:"source"`
	Implemented bool   `json:"implemented"`
}

type ProtocolProfile struct {
	ModelHint       string                  `json:"modelHint"`
	BLE             BLEProfile              `json:"ble"`
	DefaultIP       string                  `json:"defaultIp"`
	MediaPorts      []int                   `json:"mediaPorts"`
	UDPPort         int                     `json:"udpPort"`
	CommandCatalog  []DUMLCommandDescriptor `json:"commandCatalog"`
	Implementation  string                  `json:"implementation"`
	ReferencePolicy string                  `json:"referencePolicy"`
}

type BLEAdapter interface {
	Status(context.Context) (ControlStatus, error)
	Scan(context.Context) ([]BLEDevice, error)
	Pair(context.Context, PairingRequest) (PairingResult, error)
}

type DUMLTransport interface {
	Exchange(context.Context, DUMLCommandDescriptor, []byte) ([]byte, error)
}

type WiFiConfigurator interface {
	SetupWiFi(context.Context, WiFiSetupRequest) (WiFiProfile, error)
}

type Controller interface {
	Status(context.Context) (ControlStatus, error)
	ScanBLE(context.Context) ([]BLEDevice, error)
	Pair(context.Context, PairingRequest) (PairingResult, error)
	SetupWiFi(context.Context, WiFiSetupRequest) (WiFiProfile, error)
	RunDiagnostics(context.Context, string) (DiagnosticResult, error)
	ProtocolProfile(context.Context) (ProtocolProfile, error)
}

type NoopController struct {
	profile ProtocolProfile
}

func NewNoopController() *NoopController {
	return &NoopController{profile: DefaultProtocolProfile()}
}

func DefaultProtocolProfile() ProtocolProfile {
	return ProtocolProfile{
		ModelHint: OsmoAction4Model,
		BLE: BLEProfile{
			ServiceUUID:     BLEGATTServiceUUID,
			WriteCharUUID:   BLEWriteCharUUID,
			PairingCharUUID: BLEPairingCharUUID,
			StatusCharUUID:  BLEStatusCharUUID,
			DefaultPIN:      DefaultPairingPIN,
		},
		DefaultIP:  DefaultCameraIP,
		MediaPorts: []int{DefaultMediaPort, AlternateMediaPort},
		UDPPort:    DefaultUDPPort,
		CommandCatalog: []DUMLCommandDescriptor{
			{ID: "ble-scan", Label: "Scan BLE advertisements", Transport: TransportBLE, Purpose: "Detect Osmo devices and collect model/RSSI/manufacturer metadata.", Source: "datagutt/node-osmo MIT and xaionaro-go/djictl CC0 concepts", Implemented: false},
			{ID: "ble-pair", Label: "Pair over DJI BLE service", Transport: TransportBLE, Purpose: "Pair against the DJI BLE service before Wi-Fi/AP control.", Source: "datagutt/node-osmo MIT Action 3/4/5 behavior", Implemented: false},
			{ID: "wifi-setup", Label: "Enable or read camera Wi-Fi profile", Transport: TransportBLE, Purpose: "Request or diagnose camera AP profile before HTTP media access.", Source: "datagutt/node-osmo MIT and xaionaro-go/djictl CC0 concepts", Implemented: false},
			{ID: "udp-9004-diagnostics", Label: "DUML/UDP 9004 diagnostics", Transport: TransportUDP, Purpose: "Probe firmware-dependent diagnostics or file-list behavior on the camera network.", Source: "xaionaro-go/djictl CC0 concepts", Implemented: false},
			{ID: "http-media-probe", Label: "HTTP media endpoint probe", Transport: TransportHTTP, Purpose: "Validate /v2 media path with HEAD and byte ranges before transfer.", Source: "locally implemented fresh from observed protocol notes", Implemented: true},
		},
		Implementation:  "safe-noop",
		ReferencePolicy: "Protocol constants and command intents are documented from MIT/CC0 references; unlicensed osmo-download remains inspiration only and is not copied.",
	}
}

func (c *NoopController) Status(_ context.Context) (ControlStatus, error) {
	return ControlStatus{
		BLEAvailable:      false,
		WiFiAvailable:     false,
		AdapterConfigured: false,
		Protocol:          c.protocol(),
		Message:           "DJI BLE/DUML adapter boundary is ready, but no OS BLE adapter implementation is configured; no camera commands are issued.",
	}, nil
}

func (c *NoopController) ScanBLE(_ context.Context) ([]BLEDevice, error) {
	return nil, fmt.Errorf("%w: BLE scan requires a platform BLE adapter", ErrAdapterNotConfigured)
}

func (c *NoopController) Pair(_ context.Context, req PairingRequest) (PairingResult, error) {
	return PairingResult{
		DeviceID:                 strings.TrimSpace(req.DeviceID),
		Paired:                   false,
		RequiresUserConfirmation: true,
		Message:                  "BLE pairing requires a configured DJI BLE adapter and real Osmo Action hardware.",
	}, fmt.Errorf("%w: BLE pairing is disabled until hardware validation", ErrAdapterNotConfigured)
}

func (c *NoopController) SetupWiFi(_ context.Context, req WiFiSetupRequest) (WiFiProfile, error) {
	profile := c.protocol()
	return WiFiProfile{
		SSID:       strings.TrimSpace(req.SSID),
		IPAddress:  profile.DefaultIP,
		GatewayIP:  profile.DefaultIP,
		Port:       DefaultMediaPort,
		UDPPort:    profile.UDPPort,
		MediaPorts: append([]int(nil), profile.MediaPorts...),
		Message:    "Wi-Fi/AP setup requires a configured DJI BLE/DUML adapter; returned values are protocol defaults for diagnostics only.",
	}, fmt.Errorf("%w: Wi-Fi setup is disabled until hardware validation", ErrAdapterNotConfigured)
}

func (c *NoopController) RunDiagnostics(_ context.Context, deviceID string) (DiagnosticResult, error) {
	deviceID = strings.TrimSpace(deviceID)
	if deviceID == "" {
		deviceID = "unselected-device"
	}
	return DiagnosticResult{
		Steps: []DiagnosticStep{
			{ID: "ble-adapter", Label: "BLE adapter", Status: DiagnosticBlocked, Transport: TransportBLE, Description: "No platform BLE adapter has been selected for Windows/macOS/Linux Wails yet."},
			{ID: "ble-service", Label: "DJI GATT service", Status: DiagnosticPending, Transport: TransportBLE, Description: "Expected service fff0 with write characteristic fff3 and pairing/status notifications on fff4/fff5."},
			{ID: "pairing", Label: "Pairing flow", Status: DiagnosticPending, Transport: TransportBLE, Description: "Pairing intent is modeled, but no command is sent until real hardware tests confirm behavior."},
			{ID: "wifi-ap", Label: "Camera Wi-Fi/AP", Status: DiagnosticPending, Transport: TransportBLE, Description: "Expected camera gateway is 192.168.2.1 after AP setup or manual Wi-Fi join."},
			{ID: "udp-9004", Label: "DUML UDP diagnostics", Status: DiagnosticPending, Transport: TransportUDP, Description: "UDP 9004 is reserved for future diagnostics/file-list experiments after BLE/AP validation."},
			{ID: "http-media", Label: "HTTP media probe", Status: DiagnosticAvailable, Transport: TransportHTTP, Description: "The camera package can already probe /v2 media paths on candidate ports with HEAD and byte ranges."},
		},
		Message: fmt.Sprintf("Diagnostics plan built for %s; no BLE/DUML packets were sent.", deviceID),
	}, nil
}

func (c *NoopController) ProtocolProfile(_ context.Context) (ProtocolProfile, error) {
	return c.protocol(), nil
}

func (c *NoopController) protocol() ProtocolProfile {
	if c == nil || c.profile.ModelHint == "" {
		return DefaultProtocolProfile()
	}
	profile := c.profile
	profile.MediaPorts = append([]int(nil), c.profile.MediaPorts...)
	profile.CommandCatalog = append([]DUMLCommandDescriptor(nil), c.profile.CommandCatalog...)
	return profile
}
