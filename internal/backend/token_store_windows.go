//go:build windows

package backend

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

const oneDriveTokenEntropy = "AI Video Studio OneDrive token cache v1"

type windowsDPAPITokenProtector struct{}

func newPlatformTokenProtector() tokenProtector {
	return windowsDPAPITokenProtector{}
}

func (windowsDPAPITokenProtector) Protect(data []byte) ([]byte, error) {
	return cryptProtect(data)
}

func (windowsDPAPITokenProtector) Unprotect(data []byte) ([]byte, error) {
	return cryptUnprotect(data)
}

func (windowsDPAPITokenProtector) Description() string {
	return "Windows DPAPI user-protected token cache"
}

func cryptProtect(data []byte) ([]byte, error) {
	in := bytesToBlob(data)
	entropyBytes := []byte(oneDriveTokenEntropy)
	entropy := bytesToBlob(entropyBytes)
	name, err := windows.UTF16PtrFromString("AI Video Studio OneDrive token")
	if err != nil {
		return nil, err
	}
	var out windows.DataBlob
	if err := windows.CryptProtectData(&in, name, &entropy, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return blobToBytes(out), nil
}

func cryptUnprotect(data []byte) ([]byte, error) {
	in := bytesToBlob(data)
	entropyBytes := []byte(oneDriveTokenEntropy)
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
