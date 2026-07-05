package settings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	DefaultAppDirName       = "AI Video Studio"
	DefaultSettingsName     = "settings.json"
	DefaultOneDriveFolder   = "AI Video Studio"
	DefaultChunkSizeBytes   = int64(10 * 1024 * 1024)
	DefaultMaxConcurrency   = 1
	DefaultDestinationMode  = "app_folder"
	mediaServiceKeyFileName = "mediaservice.key"
)

var ErrSettingsPathUnavailable = errors.New("settings path is unavailable")

type Store interface {
	Load(context.Context) (AppSettings, error)
	Save(context.Context, AppSettings) (AppSettings, error)
	Path() string
}

type FileStore struct {
	path     string
	defaults AppSettings
}

func NewFileStore(path string, defaults AppSettings) *FileStore {
	return &FileStore{path: strings.TrimSpace(path), defaults: Normalize(defaults)}
}

func DefaultPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrSettingsPathUnavailable, err)
	}
	if strings.TrimSpace(dir) == "" {
		return "", ErrSettingsPathUnavailable
	}
	return filepath.Join(dir, DefaultAppDirName, DefaultSettingsName), nil
}

func (s *FileStore) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

// keyPath returns the sidecar file path used to persist the media service API
// key. It is stored outside settings.json because MediaServiceAPIKey is
// json:"-" and must never be serialized alongside the rest of AppSettings.
func (s *FileStore) keyPath() string {
	if s == nil || s.path == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(s.path), mediaServiceKeyFileName)
}

func (s *FileStore) Load(_ context.Context) (AppSettings, error) {
	if s == nil || s.path == "" {
		return AppSettings{}, ErrSettingsPathUnavailable
	}
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		loaded := s.defaults
		loaded.MediaServiceAPIKey = s.readAPIKey()
		return loaded, nil
	}
	if err != nil {
		return AppSettings{}, err
	}
	var loaded AppSettings
	if err := json.Unmarshal(data, &loaded); err != nil {
		return AppSettings{}, fmt.Errorf("read settings: %w", err)
	}
	normalized := Normalize(mergeDefaults(s.defaults, loaded))
	normalized.MediaServiceAPIKey = s.readAPIKey()
	return normalized, nil
}

func (s *FileStore) Save(_ context.Context, next AppSettings) (AppSettings, error) {
	if s == nil || s.path == "" {
		return AppSettings{}, ErrSettingsPathUnavailable
	}
	normalized := Normalize(mergeDefaults(s.defaults, next))
	apiKey := strings.TrimSpace(normalized.MediaServiceAPIKey)
	normalized.MediaServiceAPIKey = ""
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return AppSettings{}, err
	}
	data, err := json.MarshalIndent(normalized, "", "  ")
	if err != nil {
		return AppSettings{}, err
	}
	if err := os.WriteFile(s.path, append(data, '\n'), 0o600); err != nil {
		return AppSettings{}, err
	}
	// Only overwrite the stored API key when a new one was provided so that
	// leaving the field blank in the UI does not clear a previously saved key.
	if apiKey != "" {
		if err := os.WriteFile(s.keyPath(), []byte(apiKey), 0o600); err != nil {
			return AppSettings{}, err
		}
		normalized.MediaServiceAPIKey = apiKey
	} else {
		normalized.MediaServiceAPIKey = s.readAPIKey()
	}
	return normalized, nil
}

func (s *FileStore) readAPIKey() string {
	path := s.keyPath()
	if path == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func Normalize(next AppSettings) AppSettings {
	next.TenantID = strings.TrimSpace(next.TenantID)
	next.ClientID = strings.TrimSpace(next.ClientID)
	next.OneDriveFolder = strings.Trim(strings.TrimSpace(next.OneDriveFolder), `/\`)
	if next.OneDriveFolder == "" {
		next.OneDriveFolder = DefaultOneDriveFolder
	}
	if next.GraphAuth.TenantID == "" {
		next.GraphAuth.TenantID = next.TenantID
	}
	if next.GraphAuth.ClientID == "" {
		next.GraphAuth.ClientID = next.ClientID
	}
	if next.GraphAuth.AuthFlow == "" {
		next.GraphAuth.AuthFlow = "device_code"
	}
	if len(next.GraphAuth.Scopes) == 0 {
		next.GraphAuth.Scopes = []string{"Files.ReadWrite.AppFolder"}
	}
	if next.OneDriveDestination.Mode == "" {
		next.OneDriveDestination.Mode = DefaultDestinationMode
	}
	if next.OneDriveDestination.DisplayName == "" {
		next.OneDriveDestination.DisplayName = "AI Video Studio app folder"
	}
	next.OneDriveDestination.Path = strings.TrimSpace(next.OneDriveDestination.Path)
	if next.OneDriveDestination.Path == "" {
		next.OneDriveDestination.Path = "/Apps/" + next.OneDriveFolder
	}
	if next.ChunkSizeBytes <= 0 {
		next.ChunkSizeBytes = DefaultChunkSizeBytes
	}
	if next.MaxConcurrentImports <= 0 {
		next.MaxConcurrentImports = DefaultMaxConcurrency
	}
	return next
}

func mergeDefaults(defaults, loaded AppSettings) AppSettings {
	if loaded.OneDriveFolder == "" {
		loaded.OneDriveFolder = defaults.OneDriveFolder
	}
	if loaded.GraphAuth.AuthFlow == "" {
		loaded.GraphAuth.AuthFlow = defaults.GraphAuth.AuthFlow
	}
	if len(loaded.GraphAuth.Scopes) == 0 {
		loaded.GraphAuth.Scopes = append([]string(nil), defaults.GraphAuth.Scopes...)
	}
	if loaded.OneDriveDestination.Mode == "" {
		loaded.OneDriveDestination.Mode = defaults.OneDriveDestination.Mode
	}
	if loaded.OneDriveDestination.DisplayName == "" {
		loaded.OneDriveDestination.DisplayName = defaults.OneDriveDestination.DisplayName
	}
	if loaded.OneDriveDestination.Path == "" {
		loaded.OneDriveDestination.Path = defaults.OneDriveDestination.Path
	}
	if loaded.AzureContentUnderstanding.AnalyzerID == "" {
		loaded.AzureContentUnderstanding.AnalyzerID = defaults.AzureContentUnderstanding.AnalyzerID
	}
	if loaded.AzureContentUnderstanding.APIVersion == "" {
		loaded.AzureContentUnderstanding.APIVersion = defaults.AzureContentUnderstanding.APIVersion
	}
	if loaded.AzureContentUnderstanding.SourceMode == "" {
		loaded.AzureContentUnderstanding.SourceMode = defaults.AzureContentUnderstanding.SourceMode
	}
	if loaded.ChunkSizeBytes <= 0 {
		loaded.ChunkSizeBytes = defaults.ChunkSizeBytes
	}
	if loaded.MaxConcurrentImports <= 0 {
		loaded.MaxConcurrentImports = defaults.MaxConcurrentImports
	}
	return loaded
}
