//go:build windows

package settings

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

var isDPAPI = true

const msKeyEntropy = "AI Video Studio Media Service key v1"

func protectWithDPAPI(plaintext string) ([]byte, error) {
	return cryptProtectMSKey([]byte(plaintext))
}

func unprotectWithDPAPI(ciphertext []byte) string {
	plain, err := cryptUnprotectMSKey(ciphertext)
	if err != nil {
		return ""
	}
	return string(plain)
}

func cryptProtectMSKey(data []byte) ([]byte, error) {
	in := bytesToBlob(data)
	entropyBytes := []byte(msKeyEntropy)
	entropy := bytesToBlob(entropyBytes)
	var out windows.DataBlob
	if err := windows.CryptProtectData(&in, nil, &entropy, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return blobToBytes(out), nil
}

func cryptUnprotectMSKey(data []byte) ([]byte, error) {
	in := bytesToBlob(data)
	entropyBytes := []byte(msKeyEntropy)
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