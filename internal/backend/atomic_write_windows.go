//go:build windows

package backend

import (
	"fmt"
	"syscall"
	"unsafe"
)

var (
	kernel32        = syscall.NewLazyDLL("kernel32.dll")
	procMoveFileExW = kernel32.NewProc("MoveFileExW")
)

const moveFileReplaceExisting = 0x1
const moveFileWriteThrough = 0x8

func replaceFileAtomically(src, dst string) error {
	srcPtr, err := syscall.UTF16PtrFromString(src)
	if err != nil {
		return err
	}
	dstPtr, err := syscall.UTF16PtrFromString(dst)
	if err != nil {
		return err
	}
	r1, _, callErr := procMoveFileExW.Call(
		uintptr(unsafe.Pointer(srcPtr)),
		uintptr(unsafe.Pointer(dstPtr)),
		uintptr(moveFileReplaceExisting|moveFileWriteThrough),
	)
	if r1 == 0 {
		if callErr != syscall.Errno(0) {
			return fmt.Errorf("replace file atomically: %w", callErr)
		}
		return fmt.Errorf("replace file atomically: move failed")
	}
	return nil
}
