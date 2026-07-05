package main

import (
	"embed"
	"io/fs"
	"log"

	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/zecloud/ai-video-studio/internal/backend"
)

//go:embed frontend/dist/*
var frontendAssets embed.FS

func main() {
	dist, err := fs.Sub(frontendAssets, "frontend/dist")
	if err != nil {
		log.Fatal(err)
	}
	settingsStore := backend.NewSettingsStore()
	oneDriveService := backend.NewOneDriveService(settingsStore)
	settingsService := backend.NewSettingsService(settingsStore)
	libraryStore := backend.NewLibraryStore()
	cuService := backend.NewContentUnderstandingServiceFromSettings(settingsStore)
	mediaClient := backend.NewMediaServiceClient(settingsStore)

	app := application.New(application.Options{
		Name:        "AI Video Studio",
		Description: "Import DJI Osmo Action 4 footage to OneDrive and prepare AI-assisted edits.",
		Services: []application.Service{
			application.NewService(backend.NewAppService()),
			application.NewService(backend.NewCameraService()),
			application.NewService(backend.NewDJIControlService()),
			application.NewService(backend.NewTransferService(oneDriveService, libraryStore)),
			application.NewService(oneDriveService),
			application.NewService(cuService),
			application.NewService(backend.NewEditingService()),
			application.NewService(backend.NewVideoProcessingService()),
			application.NewService(backend.NewProjectLibraryService(libraryStore, oneDriveService, cuService, mediaClient)),
			application.NewService(settingsService),
		},
		Assets: application.AssetOptions{
			Handler: application.BundledAssetFileServer(dist),
		},
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
	})
	app.RegisterService(application.NewService(backend.NewFileDialogService(app)))

	app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:           "AI Video Studio",
		Width:           1280,
		Height:          800,
		MinWidth:        960,
		MinHeight:       640,
		URL:             "/",
		DevToolsEnabled: true,
	})

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}
