//go:build windows

package settings

import (
	"encoding/hex"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

var isDPAPI = true

const dpapiEntropyPrefix = "AI Video Studio secret:"

func protectWithDPAPI(path, plaintext string) ([]byte, error) {
	protected, err := cryptProtectSecret(path, []byte(plaintext))
	if err != nil {
		return nil, err
	}
	encoded := make([]byte, len(dpapiMarker)+hex.EncodedLen(len(protected)))
	copy(encoded, dpapiMarker)
	hex.Encode(encoded[len(dpapiMarker):], protected)
	return encoded, nil
}

func unprotectWithDPAPI(path string, ciphertext []byte) string {
	plain, err := cryptUnprotectSecret(path, ciphertext)
	if err != nil {
		return ""
	}
	return string(plain)
}

func cryptProtectSecret(path string, data []byte) ([]byte, error) {
	in := bytesToBlob(data)
	entropyBytes := []byte(dpapiEntropyPrefix + filepath.Base(path) + ":v1")
	entropy := bytesToBlob(entropyBytes)
	var out windows.DataBlob
	if err := windows.CryptProtectData(&in, nil, &entropy, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return blobToBytes(out), nil
}

func cryptUnprotectSecret(path string, data []byte) ([]byte, error) {
	in := bytesToBlob(data)
	entropyBytes := []byte(dpapiEntropyPrefix + filepath.Base(path) + ":v1")
	entropy := bytesToBlob(entropyBytes)
	var out windows.DataBlob
	if err := windows.CryptUnprotectData(&in, nil, &entropy, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return blobToBytes(out), nil
}

func bytesToBlob(data []byte) windows.DataBlob {
	if len(data) == 0 {
		return windows.DataBlob{}
	}
	return windows.DataBlob{Size: uint32(len(data)), Data: &data[0]}
}

func blobToBytes(blob windows.DataBlob) []byte {
	if blob.Size == 0 || blob.Data == nil {
		return nil
	}
	return append([]byte(nil), unsafe.Slice(blob.Data, blob.Size)...)
}
