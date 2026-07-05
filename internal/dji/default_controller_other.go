//go:build !windows

package dji

func NewDefaultController() Controller {
	return NewNoopController()
}
