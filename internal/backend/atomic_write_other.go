//go:build !windows

package backend

import "os"

func replaceFileAtomically(src, dst string) error {
	return os.Rename(src, dst)
}
