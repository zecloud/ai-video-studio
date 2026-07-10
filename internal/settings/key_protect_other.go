//go:build !windows

package settings

import "errors"

var isDPAPI = false

func protectWithDPAPI(_ string, _ string) ([]byte, error) {
	return nil, errors.New("DPAPI is not available on this platform")
}

func unprotectWithDPAPI(_ string, _ []byte) string {
	return ""
}
