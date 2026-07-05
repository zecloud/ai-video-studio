//go:build !prod

package main

import (
	"os"
	"strings"
)

// devtoolsEnabled returns whether the browser DevTools should be shown.
// In non-production builds (the default), DevTools are enabled when
// the AI_VIDEO_STUDIO_DEVTOOLS environment variable is not explicitly
// set to "0" or "false".
func devtoolsEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("AI_VIDEO_STUDIO_DEVTOOLS")))
	return v != "0" && v != "false"
}