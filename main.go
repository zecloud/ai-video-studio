package main

import (
	"embed"

	"github.com/zecloud/ai-video-studio/internal/studioapp"
)

//go:embed frontend/dist
var frontendAssets embed.FS

func main() { studioapp.Run(frontendAssets, devtoolsEnabled()) }
