package transfer

import (
	"context"
	"time"
)

type TransferStatus string

const (
	TransferQueued    TransferStatus = "queued"
	TransferRunning   TransferStatus = "running"
	TransferCompleted TransferStatus = "completed"
	TransferBlocked   TransferStatus = "blocked"
	TransferFailed    TransferStatus = "failed"
	TransferCancelled TransferStatus = "cancelled"
)

type TransferJob struct {
	ID             string         `json:"id"`
	SourcePath     string         `json:"sourcePath"`
	Destination    string         `json:"destination"`
	Status         TransferStatus `json:"status"`
	BytesTotal     int64          `json:"bytesTotal"`
	BytesCompleted int64          `json:"bytesCompleted"`
	DriveItemID    string         `json:"driveItemId,omitempty"`
	FileName       string         `json:"fileName,omitempty"`
	CreatedAt      time.Time      `json:"createdAt"`
	Message        string         `json:"message,omitempty"`
}

type TransferProgress struct {
	JobID          string         `json:"jobId"`
	Status         TransferStatus `json:"status"`
	BytesCompleted int64          `json:"bytesCompleted"`
	BytesTotal     int64          `json:"bytesTotal"`
	BytesPerSecond int64          `json:"bytesPerSecond,omitempty"`
	Message        string         `json:"message,omitempty"`
}

type StartTransferRequest struct {
	CameraDeviceID  string   `json:"cameraDeviceId"`
	MediaIDs        []string `json:"mediaIds"`
	DestinationPath string   `json:"destinationPath"`
}

type LocalMediaFile struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Path        string    `json:"path"`
	SizeBytes   int64     `json:"sizeBytes"`
	ModifiedAt  time.Time `json:"modifiedAt"`
	ContentType string    `json:"contentType,omitempty"`
}

type DescribeLocalFilesRequest struct {
	Paths []string `json:"paths"`
}

type LocalToOneDriveRequest struct {
	Files           []LocalMediaFile `json:"files"`
	DestinationPath string           `json:"destinationPath"`
	ChunkSizeBytes  int64            `json:"chunkSizeBytes,omitempty"`
}

type Service interface {
	StartCameraToOneDrive(context.Context, StartTransferRequest) (TransferJob, error)
	DescribeLocalFiles(context.Context, DescribeLocalFilesRequest) ([]LocalMediaFile, error)
	StartLocalToOneDrive(context.Context, LocalToOneDriveRequest) (TransferJob, error)
	ListJobs(context.Context) ([]TransferJob, error)
	Cancel(context.Context, string) error
}
