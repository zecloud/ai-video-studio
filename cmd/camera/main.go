package main

import (
	"embed"

	"github.com/zecloud/ai-video-studio/internal/appdevtools"
	"github.com/zecloud/ai-video-studio/internal/cameraapp"
)

//go:embed frontend/dist
var frontendAssets embed.FS

func main() { cameraapp.Run(frontendAssets, appdevtools.Enabled()) }
