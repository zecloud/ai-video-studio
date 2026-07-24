package cameraapp

import (
	"github.com/wailsapp/wails/v3/pkg/application"
	"io/fs"
	"log"
)

func Run(assets fs.FS, devtools bool) {
	dist, err := fs.Sub(assets, "frontend/dist")
	if err != nil {
		log.Fatalf("camera frontend assets are unavailable: %v", err)
	}
	app := application.New(application.Options{Name: "AI Video Studio Camera", Description: "Connect to and browse DJI Osmo Action 4 cameras.", Services: []application.Service{application.NewService(NewCameraService()), application.NewService(NewDJIControlService())}, Assets: application.AssetOptions{Handler: application.BundledAssetFileServer(dist)}, Mac: application.MacOptions{ApplicationShouldTerminateAfterLastWindowClosed: true}})
	app.Window.NewWithOptions(application.WebviewWindowOptions{Title: "AI Video Studio Camera", Width: 1100, Height: 750, MinWidth: 800, MinHeight: 600, URL: "/", DevToolsEnabled: devtools})
	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}
