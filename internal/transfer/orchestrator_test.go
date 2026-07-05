package transfer

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zecloud/ai-video-studio/internal/camera"
	"github.com/zecloud/ai-video-studio/internal/onedrive"
)

func TestOrchestratorStreamsCameraChunksToOneDriveWithoutDisk(t *testing.T) {
	source := &fakeMediaSource{data: []byte("abcdefghij")}
	target := &fakeUploadTarget{}
	orch := NewOrchestrator(source, target)
	orch.ChunkSizeBytes = onedrive.GraphChunkAlignmentBytes
	orch.Now = func() time.Time { return time.Unix(100, 0).UTC() }

	job, err := orch.TransferCameraToOneDrive(context.Background(), CameraToOneDriveRequest{
		CameraDeviceID:  "osmo-1",
		DestinationPath: "Imports/Test",
		ChunkSizeBytes:  onedrive.GraphChunkAlignmentBytes,
		Media: []CameraMediaSelection{{
			ID:        "clip-1",
			Name:      "clip.mp4",
			Path:      "/DCIM/100MEDIA/clip.mp4",
			Storage:   camera.CameraStorageSD,
			SizeBytes: int64(len(source.data)),
		}},
	})
	if err != nil {
		t.Fatalf("TransferCameraToOneDrive returned error: %v", err)
	}
	if job.Status != TransferCompleted || job.BytesCompleted != int64(len(source.data)) {
		t.Fatalf("unexpected job: %+v", job)
	}
	if got := string(target.uploaded); got != "abcdefghij" {
		t.Fatalf("uploaded bytes = %q", got)
	}
	if len(source.requests) != 1 || source.requests[0].Offset != 0 || source.requests[0].Length != int64(len(source.data)) {
		t.Fatalf("unexpected camera requests: %+v", source.requests)
	}
	if target.destination != "Imports/Test/clip.mp4" {
		t.Fatalf("destination = %q", target.destination)
	}
}

func TestOrchestratorRetriesFailedChunk(t *testing.T) {
	source := &fakeMediaSource{data: []byte("abc")}
	target := &fakeUploadTarget{failUploads: 1}
	orch := NewOrchestrator(source, target)
	orch.ChunkSizeBytes = onedrive.GraphChunkAlignmentBytes

	_, err := orch.TransferCameraToOneDrive(context.Background(), CameraToOneDriveRequest{
		CameraDeviceID: "osmo-1",
		Media: []CameraMediaSelection{{
			ID:        "clip-1",
			Name:      "clip.mp4",
			Path:      "/clip.mp4",
			Storage:   camera.CameraStorageSD,
			SizeBytes: int64(len(source.data)),
		}},
	})
	if err != nil {
		t.Fatalf("TransferCameraToOneDrive returned error: %v", err)
	}
	if target.uploadAttempts != 2 {
		t.Fatalf("upload attempts = %d, want 2", target.uploadAttempts)
	}
}

func TestOrchestratorStreamsLocalFileToOneDriveWithoutDuplicateCopy(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "clip.mp4")
	if err := os.WriteFile(filePath, []byte("local-video-bytes"), 0o600); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	target := &fakeUploadTarget{}
	orch := NewOrchestrator(nil, target)
	orch.ChunkSizeBytes = onedrive.GraphChunkAlignmentBytes
	orch.Now = func() time.Time { return time.Unix(200, 0).UTC() }

	job, err := orch.TransferLocalToOneDrive(context.Background(), LocalToOneDriveRequest{
		DestinationPath: "Imports/USB",
		ChunkSizeBytes:  onedrive.GraphChunkAlignmentBytes,
		Files: []LocalMediaFile{{
			ID:        filePath,
			Name:      "clip.mp4",
			Path:      filePath,
			SizeBytes: int64(len("local-video-bytes")),
		}},
	})
	if err != nil {
		t.Fatalf("TransferLocalToOneDrive returned error: %v", err)
	}
	if job.Status != TransferCompleted || job.BytesCompleted != int64(len("local-video-bytes")) {
		t.Fatalf("unexpected job: %+v", job)
	}
	if got := string(target.uploaded); got != "local-video-bytes" {
		t.Fatalf("uploaded bytes = %q", got)
	}
	if target.destination != "Imports/USB/clip.mp4" {
		t.Fatalf("destination = %q", target.destination)
	}
}

func TestOrchestratorAllowsHTTPClientToCloseLocalChunkBody(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "clip.mp4")
	if err := os.WriteFile(filePath, []byte("local-video-bytes"), 0o600); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	target := &fakeUploadTarget{closeRequestBody: true}
	orch := NewOrchestrator(nil, target)
	orch.ChunkSizeBytes = onedrive.GraphChunkAlignmentBytes

	job, err := orch.TransferLocalToOneDrive(context.Background(), LocalToOneDriveRequest{
		DestinationPath: "Imports/USB",
		ChunkSizeBytes:  onedrive.GraphChunkAlignmentBytes,
		Files: []LocalMediaFile{{
			ID:        filePath,
			Name:      "clip.mp4",
			Path:      filePath,
			SizeBytes: int64(len("local-video-bytes")),
		}},
	})
	if err != nil {
		t.Fatalf("TransferLocalToOneDrive returned error: %v", err)
	}
	if job.Status != TransferCompleted {
		t.Fatalf("job status = %s, want %s", job.Status, TransferCompleted)
	}
	if got := string(target.uploaded); got != "local-video-bytes" {
		t.Fatalf("uploaded bytes = %q", got)
	}
}

func TestOrchestratorCancelsBeforeUpload(t *testing.T) {
	source := &fakeMediaSource{data: []byte("abc")}
	target := &fakeUploadTarget{}
	orch := NewOrchestrator(source, target)
	orch.ChunkSizeBytes = onedrive.GraphChunkAlignmentBytes
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	job, err := orch.TransferCameraToOneDrive(ctx, CameraToOneDriveRequest{
		CameraDeviceID: "osmo-1",
		Media: []CameraMediaSelection{{
			ID:        "clip-1",
			Name:      "clip.mp4",
			Path:      "/clip.mp4",
			Storage:   camera.CameraStorageSD,
			SizeBytes: int64(len(source.data)),
		}},
	})
	if !errors.Is(err, ErrTransferCancelled) {
		t.Fatalf("expected ErrTransferCancelled, got %v", err)
	}
	if job.Status != TransferCancelled {
		t.Fatalf("expected cancelled job, got %+v", job)
	}
}

type fakeMediaSource struct {
	data     []byte
	requests []camera.MediaStreamRequest
}

func (s *fakeMediaSource) OpenMediaStream(_ context.Context, req camera.MediaStreamRequest) (io.ReadCloser, error) {
	s.requests = append(s.requests, req)
	end := req.Offset + req.Length
	if end > int64(len(s.data)) {
		end = int64(len(s.data))
	}
	return io.NopCloser(bytes.NewReader(s.data[req.Offset:end])), nil
}

type fakeUploadTarget struct {
	destination      string
	uploaded         []byte
	failUploads      int
	closeRequestBody bool
	uploadAttempts   int
}

func (t *fakeUploadTarget) CreateUploadSession(_ context.Context, destinationPath string, _ int64) (onedrive.OneDriveUploadSession, error) {
	t.destination = destinationPath
	return onedrive.OneDriveUploadSession{UploadURL: "https://upload.example/session"}, nil
}

func (t *fakeUploadTarget) UploadChunk(_ context.Context, session onedrive.OneDriveUploadSession, chunk onedrive.ChunkRange, body io.Reader) (onedrive.OneDriveUploadSession, error) {
	t.uploadAttempts++
	if t.failUploads > 0 {
		t.failUploads--
		return onedrive.OneDriveUploadSession{}, errors.New("temporary upload failure")
	}
	data, err := io.ReadAll(body)
	if err != nil {
		return onedrive.OneDriveUploadSession{}, err
	}
	if t.closeRequestBody {
		closer, ok := body.(io.Closer)
		if !ok {
			return onedrive.OneDriveUploadSession{}, errors.New("body is not closeable")
		}
		if err := closer.Close(); err != nil {
			return onedrive.OneDriveUploadSession{}, err
		}
	}
	t.uploaded = append(t.uploaded, data...)
	session.NextStart = chunk.End + 1
	if session.NextStart == chunk.Total {
		session.DriveItemID = "drive-item-1"
	}
	return session, nil
}
