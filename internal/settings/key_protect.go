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
func protectAPIKey(path string, key, plaintext []byte) ([]byte, error) {
	if len(plaintext) == 0 {
		return nil, nil
	}
	return seal(key, plaintext, []byte("ai-video-studio:"+filepath.Base(path)+":v1"))
}

// unprotectAPIKey decrypts a previously protected ciphertext with AES-GCM.
// Returns an empty string when ciphertext is empty or decryption fails.
func unprotectAPIKey(path string, key, ciphertext []byte) string {
	if len(ciphertext) == 0 {
		return ""
	}
	plain, err := open(key, ciphertext, []byte("ai-video-studio:"+filepath.Base(path)+":v1"))
	if err != nil {
		return ""
	}
	return string(plain)
}

// ----- internal AES-GCM helpers -----

func seal(key, plain, ad []byte) ([]byte, error) {
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
	return aesGCM.Seal(nonce, nonce, plain, ad), nil
}

func open(key, sealed, ad []byte) ([]byte, error) {
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
	return aesGCM.Open(nil, nonce, ciphertext, ad)
}

// dpapiMarker prefixes the hex-encoded DPAPI blob so the code can
// distinguish Windows DPAPI ciphertext from raw AES-GCM ciphertext.
// On Windows this marker is mandatory (no downgrade to AES-GCM).
// On non-Windows platforms raw AES-GCM is used without a prefix.
const dpapiMarker = "dpapi:v1:"

type secretProtector interface {
	Protect(path string, plaintext []byte) ([]byte, error)
	Unprotect(path string, ciphertext []byte) ([]byte, error)
}

type defaultSecretProtector struct{}

func (defaultSecretProtector) Protect(path string, plaintext []byte) ([]byte, error) {
	if len(plaintext) == 0 {
		return nil, nil
	}
	if isDPAPI {
		protected, err := protectWithDPAPI(path, string(plaintext))
		if err != nil {
			return nil, err
		}
		return protected, nil
	}
	key, err := loadOrCreateMasterKey(path)
	if err != nil {
		return nil, err
	}
	return protectAPIKey(path, key, plaintext)
}

func (defaultSecretProtector) Unprotect(path string, ciphertext []byte) ([]byte, error) {
	if len(ciphertext) == 0 {
		return nil, nil
	}
	if isDPAPI {
		// On Windows, reject ciphertext that is not DPAPI-protected.
		if len(ciphertext) < len(dpapiMarker) || string(ciphertext[:len(dpapiMarker)]) != dpapiMarker {
			return nil, nil
		}
		hexData := ciphertext[len(dpapiMarker):]
		dpapiBlob := make([]byte, hex.DecodedLen(len(hexData)))
		n, err := hex.Decode(dpapiBlob, hexData)
		if err != nil {
			return nil, nil
		}
		plain := unprotectWithDPAPI(path, dpapiBlob[:n])
		if plain == "" {
			return nil, nil
		}
		return []byte(plain), nil
	}
	key, err := loadOrCreateMasterKey(path)
	if err != nil {
		return nil, err
	}
	plain := unprotectAPIKey(path, key, ciphertext)
	return []byte(plain), nil
}

// readProtectedAPIKey reads and decrypts the media service API key from the
// sidecar file.
func (s *FileStore) readProtectedAPIKey() string { return s.readProtectedKey(mediaServiceKeyFileName) }

func (s *FileStore) readProtectedVideoIndexerAPIKey() string {
	return s.readProtectedKey(videoIndexerServiceKeyFileName)
}

func (s *FileStore) writeProtectedAPIKey(plaintext string) error {
	return s.writeProtectedKey(mediaServiceKeyFileName, plaintext)
}

func (s *FileStore) writeProtectedVideoIndexerAPIKey(plaintext string) error {
	return s.writeProtectedKey(videoIndexerServiceKeyFileName, plaintext)
}
