package transfer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/zecloud/ai-video-studio/internal/camera"
	"github.com/zecloud/ai-video-studio/internal/onedrive"
)

const DefaultMaxChunkRetries = 2

var (
	ErrInvalidTransferRequest = errors.New("invalid transfer request")
	ErrTransferCancelled      = errors.New("transfer cancelled")
)

type MediaSource interface {
	OpenMediaStream(context.Context, camera.MediaStreamRequest) (io.ReadCloser, error)
}

type UploadTarget interface {
	CreateUploadSession(context.Context, string, int64) (onedrive.OneDriveUploadSession, error)
	UploadChunk(context.Context, onedrive.OneDriveUploadSession, onedrive.ChunkRange, io.Reader) (onedrive.OneDriveUploadSession, error)
}

type CameraMediaSelection struct {
	ID          string               `json:"id"`
	Name        string               `json:"name"`
	Path        string               `json:"path"`
	Storage     camera.CameraStorage `json:"storage"`
	SizeBytes   int64                `json:"sizeBytes"`
	ContentType string               `json:"contentType,omitempty"`
}

type CameraToOneDriveRequest struct {
	CameraDeviceID  string                 `json:"cameraDeviceId"`
	Media           []CameraMediaSelection `json:"media"`
	DestinationPath string                 `json:"destinationPath"`
	ChunkSizeBytes  int64                  `json:"chunkSizeBytes,omitempty"`
}

type ProgressHandler func(TransferProgress)

type Orchestrator struct {
	Source          MediaSource
	Target          UploadTarget
	ChunkSizeBytes  int64
	MaxChunkRetries int
	Now             func() time.Time
	OnProgress      ProgressHandler

	mu      sync.Mutex
	jobs    map[string]TransferJob
	cancels map[string]context.CancelFunc
}

func NewOrchestrator(source MediaSource, target UploadTarget) *Orchestrator {
	return &Orchestrator{
		Source:          source,
		Target:          target,
		ChunkSizeBytes:  onedrive.DefaultChunkSizeBytes,
		MaxChunkRetries: DefaultMaxChunkRetries,
		Now:             func() time.Time { return time.Now().UTC() },
		jobs:            map[string]TransferJob{},
		cancels:         map[string]context.CancelFunc{},
	}
}

func (o *Orchestrator) ListJobs() []TransferJob {
	if o == nil {
		return []TransferJob{}
	}
	o.mu.Lock()
	defer o.mu.Unlock()

	jobs := make([]TransferJob, 0, len(o.jobs))
	for _, job := range o.jobs {
		jobs = append(jobs, job)
	}
	return jobs
}

func (o *Orchestrator) Cancel(jobID string) error {
	if o == nil {
		return nil
	}
	o.mu.Lock()
	cancel := o.cancels[jobID]
	o.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return nil
}

func (o *Orchestrator) TransferCameraToOneDrive(ctx context.Context, req CameraToOneDriveRequest) (TransferJob, error) {
	if o == nil {
		return TransferJob{}, fmt.Errorf("%w: nil orchestrator", ErrInvalidTransferRequest)
	}
	if o.Source == nil {
		return TransferJob{}, fmt.Errorf("%w: media source is not configured", ErrInvalidTransferRequest)
	}
	if o.Target == nil {
		return TransferJob{}, fmt.Errorf("%w: upload target is not configured", ErrInvalidTransferRequest)
	}
	if strings.TrimSpace(req.CameraDeviceID) == "" {
		return TransferJob{}, fmt.Errorf("%w: camera device id is required", ErrInvalidTransferRequest)
	}
	if len(req.Media) == 0 {
		return TransferJob{}, fmt.Errorf("%w: at least one media item is required", ErrInvalidTransferRequest)
	}

	chunkSize := req.ChunkSizeBytes
	if chunkSize == 0 {
		chunkSize = o.ChunkSizeBytes
	}
	if chunkSize == 0 {
		chunkSize = onedrive.DefaultChunkSizeBytes
	}

	now := o.now()
	job := TransferJob{
		ID:          fmt.Sprintf("transfer-%d", now.UnixNano()),
		SourcePath:  fmt.Sprintf("camera:%s", req.CameraDeviceID),
		Destination: cleanDestinationPrefix(req.DestinationPath),
		Status:      TransferRunning,
		CreatedAt:   now,
	}
	for _, item := range req.Media {
		if item.SizeBytes <= 0 {
			return TransferJob{}, fmt.Errorf("%w: media %q size must be positive", ErrInvalidTransferRequest, item.ID)
		}
		if strings.TrimSpace(item.Path) == "" {
			return TransferJob{}, fmt.Errorf("%w: media %q path is required", ErrInvalidTransferRequest, item.ID)
		}
		job.BytesTotal += item.SizeBytes
	}

	transferCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	o.storeJob(job, cancel)
	o.emit(job, "Transfer started.")

	for _, item := range req.Media {
		if err := o.transferOne(transferCtx, req.CameraDeviceID, job.ID, job.Destination, chunkSize, item); err != nil {
			status := TransferFailed
			if errors.Is(err, context.Canceled) || errors.Is(err, ErrTransferCancelled) {
				status = TransferCancelled
			}
			job = o.updateJob(job.ID, status, 0, err.Error())
			o.removeCancel(job.ID)
			return job, err
		}
		job = o.currentJob(job.ID)
	}

	job = o.updateJob(job.ID, TransferCompleted, job.BytesTotal, "Transfer completed.")
	o.removeCancel(job.ID)
	return job, nil
}

func (o *Orchestrator) TransferLocalToOneDrive(ctx context.Context, req LocalToOneDriveRequest) (TransferJob, error) {
	if o == nil {
		return TransferJob{}, fmt.Errorf("%w: nil orchestrator", ErrInvalidTransferRequest)
	}
	if o.Target == nil {
		return TransferJob{}, fmt.Errorf("%w: upload target is not configured", ErrInvalidTransferRequest)
	}
	if len(req.Files) == 0 {
		return TransferJob{}, fmt.Errorf("%w: at least one local file is required", ErrInvalidTransferRequest)
	}

	chunkSize := req.ChunkSizeBytes
	if chunkSize == 0 {
		chunkSize = o.ChunkSizeBytes
	}
	if chunkSize == 0 {
		chunkSize = onedrive.DefaultChunkSizeBytes
	}

	now := o.now()
	job := TransferJob{
		ID:          fmt.Sprintf("transfer-%d", now.UnixNano()),
		SourcePath:  "local-files",
		Destination: cleanDestinationPrefix(req.DestinationPath),
		Status:      TransferRunning,
		CreatedAt:   now,
	}
	for _, item := range req.Files {
		if item.SizeBytes <= 0 {
			return TransferJob{}, fmt.Errorf("%w: local file %q size must be positive", ErrInvalidTransferRequest, item.Path)
		}
		if strings.TrimSpace(item.Path) == "" {
			return TransferJob{}, fmt.Errorf("%w: local file path is required", ErrInvalidTransferRequest)
		}
		job.BytesTotal += item.SizeBytes
	}

	transferCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	o.storeJob(job, cancel)
	o.emit(job, "Local transfer started.")

	for _, item := range req.Files {
		if err := o.transferLocalOne(transferCtx, job.ID, job.Destination, chunkSize, item); err != nil {
			status := TransferFailed
			if errors.Is(err, context.Canceled) || errors.Is(err, ErrTransferCancelled) {
				status = TransferCancelled
			}
			job = o.updateJob(job.ID, status, 0, err.Error())
			o.removeCancel(job.ID)
			return job, err
		}
		job = o.currentJob(job.ID)
	}

	job = o.updateJob(job.ID, TransferCompleted, job.BytesTotal, "Local transfer completed.")
	o.removeCancel(job.ID)
	return job, nil
}

func (o *Orchestrator) transferOne(ctx context.Context, deviceID, jobID, destinationPrefix string, chunkSize int64, item CameraMediaSelection) error {
	session, err := o.Target.CreateUploadSession(ctx, destinationPath(destinationPrefix, item), item.SizeBytes)
	if err != nil {
		return err
	}
	chunks, err := onedrive.PlanSequentialUpload(item.SizeBytes, chunkSize, session.NextStart)
	if err != nil {
		return err
	}

	for _, chunk := range chunks {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("%w: %v", ErrTransferCancelled, err)
		}
		body, err := o.readCameraChunk(ctx, deviceID, item, chunk)
		if err != nil {
			return err
		}
		session, err = o.uploadChunkWithRetry(ctx, session, chunk, body)
		if err != nil {
			return err
		}
		o.incrementJob(jobID, chunk.Size, fmt.Sprintf("Uploaded %s.", item.Name))
	}
	return nil
}

func (o *Orchestrator) transferLocalOne(ctx context.Context, jobID, destinationPrefix string, chunkSize int64, item LocalMediaFile) error {
	session, err := o.Target.CreateUploadSession(ctx, destinationPathLocal(destinationPrefix, item), item.SizeBytes)
	if err != nil {
		return err
	}
	chunks, err := onedrive.PlanSequentialUpload(item.SizeBytes, chunkSize, session.NextStart)
	if err != nil {
		return err
	}

	for _, chunk := range chunks {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("%w: %v", ErrTransferCancelled, err)
		}
		session, err = o.uploadLocalChunkWithRetry(ctx, session, chunk, item.Path)
		if err != nil {
			return err
		}
		o.incrementJob(jobID, chunk.Size, fmt.Sprintf("Uploaded %s.", item.Name))
	}
	o.setJobDriveItem(jobID, session.DriveItemID, item.Name)
	return nil
}

func (o *Orchestrator) readCameraChunk(ctx context.Context, deviceID string, item CameraMediaSelection, chunk onedrive.ChunkRange) ([]byte, error) {
	stream, err := o.Source.OpenMediaStream(ctx, camera.MediaStreamRequest{
		DeviceID: deviceID,
		Path:     item.Path,
		Storage:  item.Storage,
		Offset:   chunk.Start,
		Length:   chunk.Size,
	})
	if err != nil {
		return nil, err
	}
	defer stream.Close()

	limited := io.LimitReader(stream, chunk.Size)
	buf := bytes.NewBuffer(make([]byte, 0, chunk.Size))
	if _, err := io.Copy(buf, limited); err != nil {
		return nil, err
	}
	if int64(buf.Len()) != chunk.Size {
		return nil, fmt.Errorf("camera stream ended early for %s: read %d of %d bytes", item.Path, buf.Len(), chunk.Size)
	}
	return buf.Bytes(), nil
}

func (o *Orchestrator) uploadChunkWithRetry(ctx context.Context, session onedrive.OneDriveUploadSession, chunk onedrive.ChunkRange, body []byte) (onedrive.OneDriveUploadSession, error) {
	maxRetries := o.MaxChunkRetries
	if maxRetries < 0 {
		maxRetries = 0
	}
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if err := ctx.Err(); err != nil {
			return onedrive.OneDriveUploadSession{}, fmt.Errorf("%w: %v", ErrTransferCancelled, err)
		}
		next, err := o.Target.UploadChunk(ctx, session, chunk, bytes.NewReader(body))
		if err == nil {
			return next, nil
		}
		lastErr = err
	}
	return onedrive.OneDriveUploadSession{}, lastErr
}

func (o *Orchestrator) uploadLocalChunkWithRetry(ctx context.Context, session onedrive.OneDriveUploadSession, chunk onedrive.ChunkRange, filePath string) (onedrive.OneDriveUploadSession, error) {
	maxRetries := o.MaxChunkRetries
	if maxRetries < 0 {
		maxRetries = 0
	}
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if err := ctx.Err(); err != nil {
			return onedrive.OneDriveUploadSession{}, fmt.Errorf("%w: %v", ErrTransferCancelled, err)
		}
		body, err := openLocalChunk(filePath, chunk)
		if err != nil {
			return onedrive.OneDriveUploadSession{}, err
		}
		next, uploadErr := o.Target.UploadChunk(ctx, session, chunk, body)
		closeErr := body.Close()
		if uploadErr == nil && closeErr != nil {
			uploadErr = closeErr
		}
		if uploadErr == nil {
			return next, nil
		}
		lastErr = uploadErr
	}
	return onedrive.OneDriveUploadSession{}, lastErr
}

type localChunkReader struct {
	io.Reader
	file      *os.File
	closeOnce sync.Once
	closeErr  error
}

func (r *localChunkReader) Close() error {
	r.closeOnce.Do(func() {
		r.closeErr = r.file.Close()
	})
	return r.closeErr
}

func openLocalChunk(filePath string, chunk onedrive.ChunkRange) (io.ReadCloser, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	return &localChunkReader{Reader: io.NewSectionReader(file, chunk.Start, chunk.Size), file: file}, nil
}

func (o *Orchestrator) storeJob(job TransferJob, cancel context.CancelFunc) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.jobs == nil {
		o.jobs = map[string]TransferJob{}
	}
	if o.cancels == nil {
		o.cancels = map[string]context.CancelFunc{}
	}
	o.jobs[job.ID] = job
	o.cancels[job.ID] = cancel
}

func (o *Orchestrator) removeCancel(jobID string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	delete(o.cancels, jobID)
}

func (o *Orchestrator) currentJob(jobID string) TransferJob {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.jobs[jobID]
}

func (o *Orchestrator) incrementJob(jobID string, bytesCompleted int64, message string) {
	o.mu.Lock()
	job := o.jobs[jobID]
	job.BytesCompleted += bytesCompleted
	if job.BytesCompleted > job.BytesTotal {
		job.BytesCompleted = job.BytesTotal
	}
	job.Message = message
	o.jobs[jobID] = job
	o.mu.Unlock()
	o.emit(job, message)
}

func (o *Orchestrator) updateJob(jobID string, status TransferStatus, bytesCompleted int64, message string) TransferJob {
	o.mu.Lock()
	job := o.jobs[jobID]
	job.Status = status
	if bytesCompleted > 0 {
		job.BytesCompleted = bytesCompleted
	}
	job.Message = message
	o.jobs[jobID] = job
	o.mu.Unlock()
	o.emit(job, message)
	return job
}

func (o *Orchestrator) setJobDriveItem(jobID, driveItemID, fileName string) {
	o.mu.Lock()
	job := o.jobs[jobID]
	job.DriveItemID = driveItemID
	job.FileName = fileName
	o.jobs[jobID] = job
	o.mu.Unlock()
}

func (o *Orchestrator) emit(job TransferJob, message string) {
	if o.OnProgress == nil {
		return
	}
	o.OnProgress(TransferProgress{
		JobID:          job.ID,
		Status:         job.Status,
		BytesCompleted: job.BytesCompleted,
		BytesTotal:     job.BytesTotal,
		Message:        message,
	})
}

func (o *Orchestrator) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now().UTC()
}

func cleanDestinationPrefix(raw string) string {
	clean := strings.Trim(strings.ReplaceAll(strings.TrimSpace(raw), "\\", "/"), "/")
	if clean == "" {
		return "Imports"
	}
	return clean
}

func destinationPath(prefix string, item CameraMediaSelection) string {
	name := strings.TrimSpace(item.Name)
	if name == "" {
		name = path.Base(strings.ReplaceAll(item.Path, "\\", "/"))
	}
	return path.Join(prefix, name)
}

func destinationPathLocal(prefix string, item LocalMediaFile) string {
	name := strings.TrimSpace(item.Name)
	if name == "" {
		name = path.Base(strings.ReplaceAll(item.Path, "\\", "/"))
	}
	return path.Join(prefix, name)
}
