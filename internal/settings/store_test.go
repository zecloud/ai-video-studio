package settings

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	cu "github.com/zecloud/ai-video-studio/internal/contentunderstanding"
)

var ctx = context.Background()

type testSecretProtector struct{}

func (testSecretProtector) Protect(_ string, plaintext []byte) ([]byte, error) {
	out := append([]byte("protected:"), plaintext...)
	for i := len("protected:"); i < len(out); i++ {
		out[i] ^= 0x5a
	}
	return out, nil
}

func (testSecretProtector) Unprotect(_ string, ciphertext []byte) ([]byte, error) {
	if !bytes.HasPrefix(ciphertext, []byte("protected:")) {
		return nil, nil
	}
	out := append([]byte(nil), ciphertext[len("protected:"):]...)
	for i := range out {
		out[i] ^= 0x5a
	}
	return out, nil
}

func TestSettingsJSONOmitsProtectedKeys(t *testing.T) {
	data, err := json.Marshal(AppSettings{
		MediaServiceEndpoint:        "https://media.example",
		MediaServiceAPIKey:          "media-secret",
		VideoIndexerServiceEndpoint: "https://video.example",
		VideoIndexerServiceAPIKey:   "video-secret",
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if bytes.Contains(data, []byte("media-secret")) || bytes.Contains(data, []byte("video-secret")) {
		t.Fatalf("settings JSON leaked a protected key: %s", data)
	}
}

func TestProtectedKeysRoundTripWithFakeProtector(t *testing.T) {
	tmp := t.TempDir()
	store := &FileStore{
		path:      filepath.Join(tmp, "settings.json"),
		defaults:  AppSettings{AzureContentUnderstanding: cu.Config{Endpoint: "https://cu.example", AnalyzerID: "analyzer", APIVersion: "2025-11-01", SourceMode: "https_url"}},
		protector: testSecretProtector{},
	}

	want := AppSettings{
		MediaServiceEndpoint:        "https://media.example",
		MediaServiceAPIKey:          "media-secret",
		VideoIndexerServiceEndpoint: "https://video.example",
		VideoIndexerServiceAPIKey:   "video-secret",
		AzureContentUnderstanding:   cu.Config{Endpoint: "https://cu.example", AnalyzerID: "analyzer", APIVersion: "2025-11-01", SourceMode: "https_url"},
	}

	saved, err := store.Save(ctx, want)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if saved.MediaServiceAPIKey != want.MediaServiceAPIKey || saved.VideoIndexerServiceAPIKey != want.VideoIndexerServiceAPIKey {
		t.Fatalf("Save returned %#v", saved)
	}

	for _, path := range []string{store.mediaServiceKeyPath(), store.videoIndexerServiceKeyPath()} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("ReadFile(%q): %v", path, err)
		}
		if bytes.Contains(raw, []byte("secret")) {
			t.Fatalf("protected file %q leaked plaintext: %q", path, raw)
		}
	}

	loaded, err := store.Load(ctx)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.MediaServiceAPIKey != want.MediaServiceAPIKey || loaded.VideoIndexerServiceAPIKey != want.VideoIndexerServiceAPIKey {
		t.Fatalf("Load returned %#v", loaded)
	}
	if loaded.AzureContentUnderstanding != want.AzureContentUnderstanding {
		t.Fatalf("CU settings changed: %#v", loaded.AzureContentUnderstanding)
	}
}

func TestLoadLegacySettingsMigratesNewFields(t *testing.T) {
	tmp := t.TempDir()
	store := &FileStore{
		path:      filepath.Join(tmp, "settings.json"),
		defaults:  AppSettings{AzureContentUnderstanding: cu.Config{Endpoint: "https://cu.example", AnalyzerID: "analyzer", APIVersion: "2025-11-01", SourceMode: "https_url"}},
		protector: testSecretProtector{},
	}
	if err := store.writeProtectedAPIKey("media-secret"); err != nil {
		t.Fatalf("writeProtectedAPIKey: %v", err)
	}
	if err := store.writeProtectedVideoIndexerAPIKey("video-secret"); err != nil {
		t.Fatalf("writeProtectedVideoIndexerAPIKey: %v", err)
	}

	legacy := `{
	  "tenantId":"organizations",
	  "clientId":"client-id",
	  "oneDriveFolder":"AI Video Studio",
	  "graphAuth":{"tenantId":"organizations","clientId":"client-id","authFlow":"device_code","scopes":["Files.ReadWrite.AppFolder"]},
	  "oneDriveDestination":{"mode":"app_folder","displayName":"AI Video Studio app folder","path":"/Apps/AI Video Studio"},
	  "azureContentUnderstanding":{"endpoint":"https://cu.example","analyzerId":"legacy-analyzer","apiVersion":"2025-01-01","sourceMode":"https_url"},
	  "chunkSizeBytes":10485760,
	  "maxConcurrentImports":1
	}`
	if err := os.WriteFile(store.path, []byte(legacy), 0o600); err != nil {
		t.Fatalf("WriteFile settings: %v", err)
	}

	loaded, err := store.Load(ctx)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.MediaServiceAPIKey != "media-secret" {
		t.Fatalf("unexpected media API key: %q", loaded.MediaServiceAPIKey)
	}
	if loaded.VideoIndexerServiceAPIKey != "video-secret" {
		t.Fatalf("unexpected video API key: %q", loaded.VideoIndexerServiceAPIKey)
	}
	if loaded.VideoIndexerServiceEndpoint != "" {
		t.Fatalf("legacy settings should not invent a video endpoint: %q", loaded.VideoIndexerServiceEndpoint)
	}
	if loaded.AzureContentUnderstanding.AnalyzerID != "legacy-analyzer" || loaded.AzureContentUnderstanding.APIVersion != "2025-01-01" {
		t.Fatalf("CU settings not preserved: %#v", loaded.AzureContentUnderstanding)
	}
}

func TestRoundTripAPIKey(t *testing.T) {
	tmp := t.TempDir()

	store := &FileStore{
		path:     filepath.Join(tmp, "settings.json"),
		defaults: AppSettings{},
	}

	mustSave := func(key string) AppSettings {
		s, err := store.Save(ctx, AppSettings{MediaServiceAPIKey: key})
		if err != nil {
			t.Fatalf("Save(%q): %v", key, err)
		}
		return s
	}

	mustLoad := func() AppSettings {
		s, err := store.Load(ctx)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		return s
	}

	// Save a key and verify round-trip.
	orig := "secret-api-key-12345"
	saved := mustSave(orig)
	if saved.MediaServiceAPIKey != orig {
		t.Fatalf("Save returned key=%q, want %q", saved.MediaServiceAPIKey, orig)
	}

	// Verify the sidecar file is NOT plaintext.
	keyFile := store.mediaServiceKeyPath()
	data, err := os.ReadFile(keyFile)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", keyFile, err)
	}
	if string(data) == orig {
		t.Fatalf("API key stored in plaintext at %q", keyFile)
	}
	t.Logf("key file size: %d bytes", len(data))

	// Load and verify the key decrypts back.
	loaded := mustLoad()
	if loaded.MediaServiceAPIKey != orig {
		t.Fatalf("Load returned key=%q, want %q", loaded.MediaServiceAPIKey, orig)
	}

	// Save empty key -> preserves existing key (by design: leaving blank
	// in settings UI should not accidentally clear a saved key)
	_ = mustSave("")
	// Verify key file still exists (key preserved).
	if _, err := os.Stat(keyFile); os.IsNotExist(err) {
		t.Fatalf("key file should still exist after save with empty key")
	}
	// Load and verify old key is still recoverable.
	loaded2 := mustLoad()
	if loaded2.MediaServiceAPIKey != orig {
		t.Fatalf("Load after empty save returned key=%q, want %q", loaded2.MediaServiceAPIKey, orig)
	}
	// Explicitly clear via writeProtectedAPIKey.
	if err := store.writeProtectedAPIKey(""); err != nil {
		t.Fatalf("writeProtectedAPIKey(\"\"): %v", err)
	}
	if _, err := os.Stat(keyFile); !os.IsNotExist(err) {
		t.Fatalf("key file should be removed after explicit clear")
	}
	// Now reload with a fresh store to confirm removal.
	s3 := &FileStore{path: filepath.Join(tmp, "settings.json"), defaults: AppSettings{}}
	loaded3, err := s3.Load(ctx)
	if err != nil {
		t.Fatalf("Load after clear: %v", err)
	}
	if loaded3.MediaServiceAPIKey != "" {
		t.Fatalf("Load after clear returned key=%q, want \"\"", loaded3.MediaServiceAPIKey)
	}
}

func TestKeyPersistenceAcrossStoreInstances(t *testing.T) {
	tmp := t.TempDir()
	settingsPath := filepath.Join(tmp, "settings.json")

	s1 := &FileStore{path: settingsPath, defaults: AppSettings{}}
	key := "cross-instance-key-98765"

	saved, err := s1.Save(ctx, AppSettings{MediaServiceAPIKey: key})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if saved.MediaServiceAPIKey != key {
		t.Fatalf("Save returned key=%q, want %q", saved.MediaServiceAPIKey, key)
	}

	// New FileStore reading from the same path must recover the key.
	s2 := &FileStore{path: settingsPath, defaults: AppSettings{}}
	loaded, err := s2.Load(ctx)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.MediaServiceAPIKey != key {
		t.Fatalf("Load from second store returned key=%q, want %q", loaded.MediaServiceAPIKey, key)
	}
}

func TestWriteProtectedAPIKeyClearsOnEmpty(t *testing.T) {
	tmp := t.TempDir()

	store := &FileStore{
		path:     filepath.Join(tmp, "settings.json"),
		defaults: AppSettings{},
	}

	// Write a key first.
	if _, err := store.Save(ctx, AppSettings{MediaServiceAPIKey: "temp-key"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// Verify key file exists.
	keyFile := store.mediaServiceKeyPath()
	if _, err := os.Stat(keyFile); os.IsNotExist(err) {
		t.Fatalf("key file not found after save")
	}

	// Write empty key -> file removed.
	if err := store.writeProtectedAPIKey(""); err != nil {
		t.Fatalf("writeProtectedAPIKey(\"\"): %v", err)
	}
	if _, err := os.Stat(keyFile); !os.IsNotExist(err) {
		t.Fatalf("key file should be removed after writing empty key")
	}
}

func TestWriteProtectedAPIKeyNoPath(t *testing.T) {
	store := &FileStore{}
	if err := store.writeProtectedAPIKey("unreachable"); err != nil {
		t.Fatalf("writeProtectedAPIKey with empty path should no-op, got err=%v", err)
	}
	if got := store.readProtectedAPIKey(); got != "" {
		t.Fatalf("readProtectedAPIKey with empty path returned %q, want \"\"", got)
	}
}

func TestCorruptedCiphertext(t *testing.T) {
	tmp := t.TempDir()
	store := &FileStore{
		path:     filepath.Join(tmp, "settings.json"),
		defaults: AppSettings{},
	}

	// Save a key first to create the encrypted file.
	if _, err := store.Save(ctx, AppSettings{MediaServiceAPIKey: "original-key"}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Corrupt the ciphertext by truncating it.
	keyFile := store.mediaServiceKeyPath()
	data, err := os.ReadFile(keyFile)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	// Truncate to half its length to break decryption.
	if err := os.WriteFile(keyFile, data[:len(data)/2], 0o600); err != nil {
		t.Fatalf("WriteFile truncated: %v", err)
	}

	// Load should return empty string, not crash.
	loaded, err := store.Load(ctx)
	if err != nil {
		t.Fatalf("Load after corruption: %v", err)
	}
	if loaded.MediaServiceAPIKey != "" {
		t.Fatalf("Load after corruption returned key=%q, want \"\"", loaded.MediaServiceAPIKey)
	}
}

func TestMasterKeyIsCreatedOnNonWindows(t *testing.T) {
	if isDPAPI {
		t.Skip("master key only used on non-Windows")
	}
	tmp := t.TempDir()
	store := &FileStore{
		path:     filepath.Join(tmp, "settings.json"),
		defaults: AppSettings{},
	}

	// Save a key, which should also create the master key.
	_, err := store.Save(ctx, AppSettings{MediaServiceAPIKey: "test-key"})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Verify master key exists and is 32 bytes.
	masterPath := filepath.Join(filepath.Dir(store.mediaServiceKeyPath()), masterKeyFileName)
	master, err := os.ReadFile(masterPath)
	if err != nil {
		t.Fatalf("master key file not created: %v", err)
	}
	if len(master) != masterKeySize {
		t.Fatalf("master key size = %d, want %d", len(master), masterKeySize)
	}
}
