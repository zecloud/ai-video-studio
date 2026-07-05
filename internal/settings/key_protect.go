package settings

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// masterKeyFileName is the sidecar file that holds the per-installation master
// key for AES-GCM encryption on non-Windows platforms. This file is generated
// once on first run and must be kept next to the encrypted key file.
const masterKeyFileName = "mediaservice.key.master"

// masterKeySize is 32 bytes (AES-256).
const masterKeySize = 32

// loadOrCreateMasterKey returns the per-installation AES-256 master key used
// to encrypt the API key via AES-GCM (non-Windows fallback). The key is read
// from the master-key sidecar file if it exists, or generated and stored
// there on first run.
func loadOrCreateMasterKey(keyFilePath string) ([]byte, error) {
	masterPath := filepath.Join(filepath.Dir(keyFilePath), masterKeyFileName)
	data, err := os.ReadFile(masterPath)
	if err == nil && len(data) == masterKeySize {
		return data, nil
	}
	// Generate a fresh random master key.
	key := make([]byte, masterKeySize)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate master key: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(masterPath), 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(masterPath, key, 0o600); err != nil {
		return nil, err
	}
	return key, nil
}

// protectAPIKey encrypts the plaintext API key with AES-GCM using the given
// raw key bytes. Returns an empty slice when plaintext is empty.
func protectAPIKey(key, plaintext []byte) ([]byte, error) {
	if len(plaintext) == 0 {
		return nil, nil
	}
	return seal(key, plaintext)
}

// unprotectAPIKey decrypts a previously protected ciphertext with AES-GCM.
// Returns an empty string when ciphertext is empty or decryption fails.
func unprotectAPIKey(key, ciphertext []byte) string {
	if len(ciphertext) == 0 {
		return ""
	}
	plain, err := open(key, ciphertext)
	if err != nil {
		return ""
	}
	return string(plain)
}

// ----- internal AES-GCM helpers -----

func seal(key, plain []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aesGCM, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aesGCM.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	// Include the application-level associated data bound to the file path.
	ad := []byte("ai-video-studio:mediaservice.key:v1")
	return aesGCM.Seal(nonce, nonce, plain, ad), nil
}

func open(key, sealed []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aesGCM, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(sealed) < aesGCM.NonceSize() {
		return nil, errors.New("ciphertext too short")
	}
	nonce, ciphertext := sealed[:aesGCM.NonceSize()], sealed[aesGCM.NonceSize():]
	ad := []byte("ai-video-studio:mediaservice.key:v1")
	return aesGCM.Open(nil, nonce, ciphertext, ad)
}

// dpapiMarker prefixes the hex-encoded DPAPI blob so the code can
// distinguish Windows DPAPI ciphertext from raw AES-GCM ciphertext.
// On Windows this marker is mandatory (no downgrade to AES-GCM).
// On non-Windows platforms raw AES-GCM is used without a prefix.
const dpapiMarker = "dpapi:v1:"

// readProtectedAPIKey reads and decrypts the API key from the sidecar file.
// On Windows: expects the "dpapi:v1:" marker + hex-encoded DPAPI blob.
// On non-Windows: expects raw AES-GCM ciphertext.
func (s *FileStore) readProtectedAPIKey() string {
	path := s.keyPath()
	if path == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return s.unprotectAPIKeyString(data)
}

// writeProtectedAPIKey encrypts the plaintext key and writes it to
// the sidecar file. When plaintext is empty, the file is removed.
func (s *FileStore) writeProtectedAPIKey(plaintext string) error {
	path := s.keyPath()
	if path == "" {
		return nil
	}
	if plaintext == "" {
		_ = os.Remove(path)
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}

	protected, err := s.protectAPIKeyString(plaintext)
	if err != nil {
		return err
	}

	return os.WriteFile(path, protected, 0o600)
}

// protectAPIKeyString encrypts plaintext using DPAPI on Windows and
// AES-GCM (with per-installation master key) on other platforms.
func (s *FileStore) protectAPIKeyString(plaintext string) ([]byte, error) {
	if isDPAPI {
		protected, err := protectWithDPAPI(plaintext)
		if err != nil {
			return nil, err
		}
		encoded := make([]byte, len(dpapiMarker)+hex.EncodedLen(len(protected)))
		copy(encoded, dpapiMarker)
		hex.Encode(encoded[len(dpapiMarker):], protected)
		return encoded, nil
	}
	key, err := loadOrCreateMasterKey(s.keyPath())
	if err != nil {
		return nil, err
	}
	return protectAPIKey(key, []byte(plaintext))
}

// unprotectAPIKeyString decrypts data produced by protectAPIKeyString.
func (s *FileStore) unprotectAPIKeyString(data []byte) string {
	if isDPAPI {
		// On Windows, reject ciphertext that is not DPAPI-protected.
		if len(data) < len(dpapiMarker) || string(data[:len(dpapiMarker)]) != dpapiMarker {
			return ""
		}
		hexData := data[len(dpapiMarker):]
		dpapiBlob := make([]byte, hex.DecodedLen(len(hexData)))
		n, err := hex.Decode(dpapiBlob, hexData)
		if err != nil {
			return ""
		}
		return unprotectWithDPAPI(dpapiBlob[:n])
	}
	key, err := loadOrCreateMasterKey(s.keyPath())
	if err != nil {
		return ""
	}
	return unprotectAPIKey(key, data)
}