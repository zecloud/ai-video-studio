package settings

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

var ctx = context.Background()

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
	keyFile := store.keyPath()
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
	keyFile := store.keyPath()
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
	keyFile := store.keyPath()
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
	masterPath := filepath.Join(filepath.Dir(store.keyPath()), masterKeyFileName)
	master, err := os.ReadFile(masterPath)
	if err != nil {
		t.Fatalf("master key file not created: %v", err)
	}
	if len(master) != masterKeySize {
		t.Fatalf("master key size = %d, want %d", len(master), masterKeySize)
	}
}