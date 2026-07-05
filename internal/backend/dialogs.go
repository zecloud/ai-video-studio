package backend

import (
	"context"
	"errors"

	"github.com/wailsapp/wails/v3/pkg/application"
)

type FileDialogService struct {
	app *application.App
}

func NewFileDialogService(app *application.App) *FileDialogService {
	return &FileDialogService{app: app}
}

func (s *FileDialogService) ChooseLocalVideos(_ context.Context) ([]string, error) {
	if s == nil || s.app == nil {
		return nil, errors.New("file dialog service is not attached to the Wails application")
	}
	return s.app.Dialog.OpenFile().
		SetTitle("Choose Osmo video files").
		SetMessage("Select MP4/MOV/M4V/LRV files from the USB-mounted camera or another local disk.").
		SetButtonText("Select videos").
		CanChooseFiles(true).
		AddFilter("Video files", "*.mp4;*.mov;*.m4v;*.lrv").
		PromptForMultipleSelection()
}
