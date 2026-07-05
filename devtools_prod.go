//go:build prod

package main

// devtoolsEnabled returns false unconditionally in production builds.
func devtoolsEnabled() bool {
	return false
}