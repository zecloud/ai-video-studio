//go:build prod

package appdevtools

// Enabled reports whether browser DevTools should be shown in production builds.
func Enabled() bool {
	return false
}
