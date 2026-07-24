//go:build !prod

package appdevtools

import (
	"os"
	"strings"
)

// Enabled reports whether browser DevTools should be shown in non-production builds.
func Enabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("AI_VIDEO_STUDIO_DEVTOOLS")))
	return v != "0" && v != "false"
}
