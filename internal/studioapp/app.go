package studioapp

import (
	"io/fs"
	"log"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/zecloud/ai-video-studio/internal/backend"
)

func Run(assets fs.FS, devtools bool) {
	dist, err := fs.Sub(assets, "frontend/dist")
	if err != nil {
		log.Fatalf("studio frontend assets are unavailable: %v", err)
	}
	settingsStore := backend.NewSettingsStore()
	oneDrive := backend.NewOneDriveService(settingsStore)
	settings := backend.NewSettingsService(settingsStore)
	library := backend.NewLibraryStore()
	media := backend.NewMediaServiceClient(settingsStore)
	editing := backend.NewEditingServiceWithRenderJobs(library, media, oneDrive.DriveClient(), settingsStore)
	app := application.New(application.Options{Name: "AI Video Studio", Description: "Import DJI Osmo Action 4 footage to OneDrive and prepare AI-assisted edits.", Services: []application.Service{
		application.NewService(backend.NewAppService()), application.NewService(backend.NewTransferService(oneDrive, library)), application.NewService(oneDrive),
		application.NewService(backend.NewContentUnderstandingServiceFromSettings(settingsStore)), application.NewService(editing),
		application.NewService(backend.NewVideoIndexerStudioServiceFromSettings(library, oneDrive, editing, settingsStore)), application.NewService(backend.NewVideoProcessingService()),
		application.NewService(backend.NewProjectLibraryService(library, oneDrive, media)), application.NewService(settings),
	}, Assets: application.AssetOptions{Handler: application.BundledAssetFileServer(dist)}, Mac: application.MacOptions{ApplicationShouldTerminateAfterLastWindowClosed: true}})
	app.RegisterService(application.NewService(backend.NewFileDialogService(app)))
	app.Window.NewWithOptions(application.WebviewWindowOptions{Title: "AI Video Studio", Width: 1280, Height: 800, MinWidth: 960, MinHeight: 640, URL: "/", DevToolsEnabled: devtools})
	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}
