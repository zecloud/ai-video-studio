//go:build windows

package dji

import "testing"

func TestBenignWindowsRuntimeInitErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{name: "already initialized", err: hresultTestError{code: 0x00000001}},
		{name: "changed apartment mode", err: hresultTestError{code: 0x80010106}},
		{name: "incorrect function localized", err: hresultTestError{code: 0x80070001}},
		{name: "incorrect function message", err: messageTestError("Fonction incorrecte.")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !isBenignWindowsRuntimeInitError(tc.err) {
				t.Fatalf("expected %v to be treated as benign", tc.err)
			}
		})
	}

	if isBenignWindowsRuntimeInitError(hresultTestError{code: 0x80004005}) {
		t.Fatal("unexpectedly treated generic COM failure as benign")
	}
}

func TestDescribeInitialPairingNotification(t *testing.T) {
	if got := describeInitialPairingNotification(bleNotification{}, messageTestError("timeout")); got != "(no initial fff4 notification observed before write)" {
		t.Fatalf("unexpected timeout description: %s", got)
	}
	got := describeInitialPairingNotification(bleNotification{source: BLEPairingCharUUID, packet: []byte{0x01, 0xab}}, nil)
	if want := "(after initial fff4 notification 01AB)"; got != want {
		t.Fatalf("description = %q, want %q", got, want)
	}
}

type hresultTestError struct {
	code uintptr
}

func (e hresultTestError) Error() string {
	return "hresult test error"
}

func (e hresultTestError) Code() uintptr {
	return e.code
}

type messageTestError string

func (e messageTestError) Error() string {
	return string(e)
}
