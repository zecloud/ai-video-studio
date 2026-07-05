//go:build windows

package dji

func NewDefaultController() Controller {
	return NewAdapterController(WindowsBLEProtocolProfile(), NewWindowsBLEAdapter(), nil)
}
