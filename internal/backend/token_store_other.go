//go:build !windows

package backend

func newPlatformTokenProtector() tokenProtector {
	return nil
}
