package backend

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"time"

	"github.com/zecloud/ai-video-studio/internal/editing"
	"github.com/zecloud/ai-video-studio/internal/library"
	"github.com/zecloud/ai-video-studio/internal/onedrive"
	"github.com/zecloud/ai-video-studio/internal/settings"
	"github.com/zecloud/ai-video-studio/internal/videoindexerstudio"
)

const renderOutputFolder = "Renders"

type asyncRenderJobClient interface {
	CreateRenderJob(context.Context, videoindexerstudio.CreateRenderJobRequest) (*videoindexerstudio.RenderJobResponse, error)
	GetRenderJob(context.Context, string) (*videoindexerstudio.RenderJobResponse, error)
	CancelRenderJob(context.Context, string) (*videoindexerstudio.RenderJobResponse, error)
	OpenRenderOutput(context.Context, string) (*videoindexerstudio.RenderOutputStream, error)
}

type renderJobClientFactory func(context.Context) (asyncRenderJobClient, error)

func NewEditingServiceWithRenderJobs(store library.Store, renderBackend RenderBackend, odClient *onedrive.Client, settingsStore settings.Store) *EditingService {
	service := NewEditingService(store, renderBackend, odClient)
	service.configureRenderJobs(settingsStore)
	return service
}

func (s *EditingService) configureRenderJobs(store settings.Store) {
	if s == nil || store == nil {
		return
	}
	s.mu.Lock()
	s.renderSettings = store
	s.renderJobFactory = func(ctx context.Context) (asyncRenderJobClient, error) {
		loaded, err := store.Load(ctx)
		if err != nil {
			return nil, err
		}
		cfg, err := videoIndexerConfigFromSettings(loaded)
		if err != nil {
			return nil, err
		}
		return videoindexerstudio.NewClient(cfg, nil)
	}
	s.mu.Unlock()
}

// Render submits an asynchronous render job, polls it with the Wails call
// context, streams the completed output to a scoped temp file, and publishes it
// to OneDrive with the desktop delegated identity.
func (s *EditingService) Render(ctx context.Context, projectID string) (*editing.RenderJob, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.Lock()
	factory := s.renderJobFactory
	s.mu.Unlock()
	if factory == nil {
		return s.renderLegacy(ctx, projectID)
	}
	client, err := factory(ctx)
	if err != nil {
		if errors.Is(err, errNotConfigured) {
			return s.renderLegacy(ctx, projectID)
		}
		return nil, fmt.Errorf("editing: render service: %w", err)
	}
	return s.renderAsync(ctx, projectID, func(context.Context) (asyncRenderJobClient, error) { return client, nil })
}

func (s *EditingService) renderAsync(ctx context.Context, projectID string, factory renderJobClientFactory) (*editing.RenderJob, error) {
	if err := s.ensureProjectsLoaded(ctx); err != nil {
		return nil, err
	}
	s.mu.Lock()
	project, ok := s.projects[projectID]
	s.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("project %q not found", projectID)
	}

	requestClips, transitions, totalMS, err := s.buildAsyncRenderTimeline(ctx, project)
	if err != nil {
		return nil, err
	}
	if s.odClient == nil || s.odClient.TokenProvider == nil {
		return nil, errors.New("editing: OneDrive sign-in is required")
	}
	token, err := s.odClient.TokenProvider.AccessToken(ctx, s.odClient.Scopes)
	if err != nil {
		return nil, fmt.Errorf("editing: OneDrive token: %w", err)
	}
	if strings.TrimSpace(token) == "" {
		return nil, errors.New("editing: OneDrive token is empty")
	}
	client, err := factory(ctx)
	if err != nil {
		return nil, fmt.Errorf("editing: render service: %w", err)
	}

	started := time.Now().UTC()
	localID := fmt.Sprintf("render-%d", started.UnixNano())
	outputName := safeRenderOutputName(project.Name)
	preset := strings.TrimSpace(project.RenderPreset)
	if preset == "" {
		preset = "h264-1080p"
	}
	job := editing.RenderJob{ID: localID, ProjectID: projectID, Status: "submitted", OutputName: outputName, TotalMS: totalMS, Message: "Submitting render job."}
	renderCtx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	if s.renderCancels == nil {
		s.renderCancels = map[string]context.CancelFunc{}
	}
	s.renderCancels[localID] = cancel
	s.jobs[localID] = cloneEditingRenderJob(job)
	s.mu.Unlock()
	defer func() {
		cancel()
		s.mu.Lock()
		delete(s.renderCancels, localID)
		s.mu.Unlock()
	}()
	emitRenderEvent("editing:render:progress", job)

	created, err := client.CreateRenderJob(renderCtx, videoindexerstudio.CreateRenderJobRequest{
		ProjectID: projectID, OneDriveAccessToken: token, Clips: requestClips, Transitions: transitions,
		Preset: preset, OutputName: outputName, CorrelationID: localID,
	})
	if err != nil {
		return s.failAsyncRender(job, fmt.Errorf("editing: submit render: %w", err))
	}
	remote := created.Job
	job.RemoteJobID = remote.ID
	job = mergeRemoteRenderState(job, remote)
	s.storeEditingRenderJob(job, "editing:render:progress")

	for !remote.Status.Terminal() {
		interval := s.renderPollInterval
		if interval <= 0 {
			interval = 2 * time.Second
		}
		timer := time.NewTimer(interval)
		select {
		case <-renderCtx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			cancelErr := s.cancelRemoteRender(client, remote.ID)
			job.Status = "canceled"
			job.Message = "Render canceled."
			s.storeEditingRenderJob(job, "editing:render:completed")
			result := cloneEditingRenderJob(job)
			return result, errors.Join(renderCtx.Err(), cancelErr)
		case <-timer.C:
		}
		response, pollErr := client.GetRenderJob(renderCtx, remote.ID)
		if pollErr != nil {
			return s.failAsyncRender(job, fmt.Errorf("editing: poll render: %w", pollErr))
		}
		remote = response.Job
		job = mergeRemoteRenderState(job, remote)
		s.storeEditingRenderJob(job, "editing:render:progress")
	}

	switch remote.Status {
	case videoindexerstudio.RenderJobStatusFailed:
		message := "remote render failed"
		if remote.Error != nil && strings.TrimSpace(remote.Error.Message) != "" {
			message = remote.Error.Message
		}
		return s.failAsyncRender(job, errors.New(message))
	case videoindexerstudio.RenderJobStatusCanceled:
		job.Status = "canceled"
		job.Message = "Render canceled."
		s.storeEditingRenderJob(job, "editing:render:completed")
		return cloneEditingRenderJob(job), nil
	case videoindexerstudio.RenderJobStatusSucceeded:
	default:
		return s.failAsyncRender(job, fmt.Errorf("editing: unexpected terminal render status %q", remote.Status))
	}

	job.Status = "downloading"
	job.Percent = 90
	job.Message = "Downloading completed render securely."
	s.storeEditingRenderJob(job, "editing:render:progress")
	tempPath, size, err := downloadRenderToTemp(renderCtx, client, remote)
	if err != nil {
		return s.failAsyncRender(job, fmt.Errorf("editing: download render output: %w", err))
	}
	defer os.Remove(tempPath)

	job.Status = "publishing"
	job.Percent = 92
	job.Message = "Publishing render to OneDrive."
	s.storeEditingRenderJob(job, "editing:render:progress")
	driveItemID, err := s.publishRenderToOneDrive(renderCtx, tempPath, outputName, size, &job)
	if err != nil {
		return s.failAsyncRender(job, fmt.Errorf("editing: publish render: %w", err))
	}
	job.Status = "completed"
	job.Percent = 100
	job.CurrentMS = job.TotalMS
	job.OutputDriveItemID = driveItemID
	job.Message = "Render uploaded to OneDrive."
	s.storeEditingRenderJob(job, "editing:render:completed")
	return cloneEditingRenderJob(job), nil
}

func (s *EditingService) buildAsyncRenderTimeline(ctx context.Context, project editing.EditProject) ([]videoindexerstudio.RenderClipRequest, []videoindexerstudio.RenderTransitionRequest, int64, error) {
	if s.store == nil {
		return nil, nil, 0, errors.New("editing: project library is not configured")
	}
	assets, err := s.store.LoadAssets(ctx)
	if err != nil {
		return nil, nil, 0, fmt.Errorf("editing: load project assets: %w", err)
	}
	byID := make(map[string]libraryAssetRef, len(assets))
	for _, asset := range assets {
		byID[asset.ID] = libraryAssetRef{itemID: asset.CloudAssetID, name: asset.Name}
	}
	var clips []videoindexerstudio.RenderClipRequest
	var totalMS int64
	for _, track := range project.Timeline.Tracks {
		if track.Kind != "" && track.Kind != "video" {
			continue
		}
		for _, clip := range track.Clips {
			asset, exists := byID[clip.SourceAssetID]
			if !exists || strings.TrimSpace(asset.itemID) == "" {
				return nil, nil, 0, fmt.Errorf("editing: source asset %q has no OneDrive item", clip.SourceAssetID)
			}
			if clip.InMS < 0 || clip.OutMS <= clip.InMS {
				return nil, nil, 0, fmt.Errorf("editing: clip %q has an invalid trim range", clip.ID)
			}
			clips = append(clips, videoindexerstudio.RenderClipRequest{ID: clip.ID, OneDriveItemID: asset.itemID, SourceName: asset.name, InMS: clip.InMS, OutMS: clip.OutMS})
			totalMS += clip.OutMS - clip.InMS
		}
	}
	if len(clips) == 0 {
		return nil, nil, 0, fmt.Errorf("project %q has no clips", project.ID)
	}
	transitions := make([]videoindexerstudio.RenderTransitionRequest, 0, len(clips)-1)
	for i := 1; i < len(clips); i++ {
		transitions = append(transitions, videoindexerstudio.RenderTransitionRequest{Kind: "cut"})
	}
	return clips, transitions, totalMS, nil
}

type libraryAssetRef struct {
	itemID string
	name   string
}

func downloadRenderToTemp(ctx context.Context, client asyncRenderJobClient, remote videoindexerstudio.RenderJob) (path string, size int64, err error) {
	if remote.Output == nil || remote.Output.Size <= 0 {
		return "", 0, errors.New("remote render output metadata is incomplete")
	}
	stream, err := client.OpenRenderOutput(ctx, remote.ID)
	if err != nil {
		return "", 0, err
	}
	file, err := os.CreateTemp("", "ai-video-studio-render-*.mp4")
	if err != nil {
		_ = stream.Body.Close()
		return "", 0, err
	}
	path = file.Name()
	complete := false
	defer func() {
		if !complete || err != nil {
			_ = os.Remove(path)
		}
	}()
	if stream.ContentLength > 0 && stream.ContentLength != remote.Output.Size {
		err = fmt.Errorf("output size %d does not match expected %d", stream.ContentLength, remote.Output.Size)
	} else {
		size, err = io.Copy(file, stream.Body)
	}
	streamCloseErr := stream.Body.Close()
	fileCloseErr := file.Close()
	if err == nil && streamCloseErr != nil {
		err = streamCloseErr
	}
	if err == nil && fileCloseErr != nil {
		err = fileCloseErr
	}
	if err == nil && size != remote.Output.Size {
		err = fmt.Errorf("downloaded output size %d does not match expected %d", size, remote.Output.Size)
	}
	complete = err == nil
	return path, size, err
}

func (s *EditingService) publishRenderToOneDrive(ctx context.Context, tempPath, outputName string, size int64, job *editing.RenderJob) (string, error) {
	if s.odClient == nil {
		return "", errors.New("OneDrive client is not configured")
	}
	session, err := s.odClient.CreateUploadSession(ctx, path.Join(renderOutputFolder, outputName), size)
	if err != nil {
		return "", err
	}
	chunkSize := int64(onedrive.DefaultChunkSizeBytes)
	s.mu.Lock()
	settingsStore := s.renderSettings
	s.mu.Unlock()
	if settingsStore != nil {
		if loaded, loadErr := settingsStore.Load(ctx); loadErr == nil && loaded.ChunkSizeBytes > 0 {
			chunkSize = loaded.ChunkSizeBytes
		}
	}
	chunks, err := onedrive.PlanSequentialUpload(size, chunkSize, session.NextStart)
	if err != nil {
		return "", err
	}
	var uploaded int64 = session.NextStart
	for _, chunk := range chunks {
		var uploadErr error
		for attempt := 0; attempt < 3; attempt++ {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			file, openErr := os.Open(tempPath)
			if openErr != nil {
				return "", openErr
			}
			body := io.NewSectionReader(file, chunk.Start, chunk.Size)
			next, err := s.odClient.UploadChunk(ctx, session, chunk, body)
			closeErr := file.Close()
			if err == nil && closeErr != nil {
				err = closeErr
			}
			if err == nil {
				session = next
				uploadErr = nil
				break
			}
			uploadErr = err
		}
		if uploadErr != nil {
			return "", uploadErr
		}
		uploaded += chunk.Size
		job.Percent = 92 + 8*float64(uploaded)/float64(size)
		job.CurrentMS = int64(float64(job.TotalMS) * float64(uploaded) / float64(size))
		job.Message = "Uploading render to OneDrive."
		s.storeEditingRenderJob(*job, "editing:render:progress")
	}
	return session.DriveItemID, nil
}

func (s *EditingService) CancelRender(ctx context.Context, jobID string) (*editing.RenderJob, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.Lock()
	stored := s.jobs[jobID]
	cancel := s.renderCancels[jobID]
	factory := s.renderJobFactory
	if stored == nil {
		s.mu.Unlock()
		return nil, fmt.Errorf("render job %q not found", jobID)
	}
	job := *stored
	s.mu.Unlock()
	if job.Status == "completed" || job.Status == "failed" || job.Status == "canceled" {
		return cloneEditingRenderJob(job), nil
	}
	job.Status = "cancellation_requested"
	job.Message = "Requesting render cancellation."
	s.storeEditingRenderJob(job, "editing:render:progress")
	var remoteErr error
	if job.RemoteJobID != "" && factory != nil {
		client, err := factory(ctx)
		if err != nil {
			remoteErr = err
		} else if _, err := client.CancelRenderJob(ctx, job.RemoteJobID); err != nil {
			remoteErr = err
		}
	}
	if cancel != nil {
		cancel()
	}
	return cloneEditingRenderJob(job), remoteErr
}

func (s *EditingService) cancelRemoteRender(client asyncRenderJobClient, remoteID string) error {
	if client == nil || strings.TrimSpace(remoteID) == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, err := client.CancelRenderJob(ctx, remoteID)
	return err
}

func (s *EditingService) failAsyncRender(job editing.RenderJob, failure error) (*editing.RenderJob, error) {
	s.mu.Lock()
	current := s.jobs[job.ID]
	cancellationRequested := current != nil && (current.Status == "cancellation_requested" || current.Status == "canceled")
	s.mu.Unlock()
	if cancellationRequested || errors.Is(failure, context.Canceled) {
		job.Status = "canceled"
		job.ErrorDetail = ""
		job.Message = "Render canceled."
		s.storeEditingRenderJob(job, "editing:render:completed")
		return cloneEditingRenderJob(job), failure
	}
	job.Status = "failed"
	job.ErrorDetail = failure.Error()
	job.Message = "Render failed."
	s.storeEditingRenderJob(job, "editing:render:completed")
	return cloneEditingRenderJob(job), failure
}

func (s *EditingService) storeEditingRenderJob(job editing.RenderJob, event string) {
	s.mu.Lock()
	if s.jobs == nil {
		s.jobs = map[string]*editing.RenderJob{}
	}
	s.jobs[job.ID] = cloneEditingRenderJob(job)
	s.mu.Unlock()
	emitRenderEvent(event, job)
}

func cloneEditingRenderJob(job editing.RenderJob) *editing.RenderJob {
	cloned := job
	return &cloned
}

func mergeRemoteRenderState(job editing.RenderJob, remote videoindexerstudio.RenderJob) editing.RenderJob {
	job.RemoteJobID = remote.ID
	job.OutputName = remote.OutputName
	if remote.Error != nil {
		job.ErrorDetail = remote.Error.Message
	}
	switch remote.Status {
	case videoindexerstudio.RenderJobStatusStaging:
		job.Status, job.Percent, job.Message = "staging", 5, "Staging render inputs."
	case videoindexerstudio.RenderJobStatusQueued:
		job.Status, job.Percent, job.Message = "queued", 10, "Render queued."
	case videoindexerstudio.RenderJobStatusRendering:
		job.Status, job.Percent, job.Message = "rendering", 55, "Rendering with FFmpeg."
	case videoindexerstudio.RenderJobStatusUploading:
		job.Status, job.Percent, job.Message = "uploading", 85, "Securing render output."
	case videoindexerstudio.RenderJobStatusSucceeded:
		job.Status, job.Percent, job.Message = "rendered", 90, "Remote render completed."
	case videoindexerstudio.RenderJobStatusFailed:
		job.Status, job.Message = "failed", "Remote render failed."
	case videoindexerstudio.RenderJobStatusCanceled:
		job.Status, job.Message = "canceled", "Render canceled."
	}
	job.CurrentMS = int64(job.Percent / 100 * float64(job.TotalMS))
	return job
}

func safeRenderOutputName(name string) string {
	name = strings.TrimSpace(strings.Map(func(r rune) rune {
		if r < 32 || strings.ContainsRune(`<>:"/\\|?*`, r) {
			return '-'
		}
		return r
	}, name))
	name = strings.Trim(name, ". ")
	if name == "" {
		name = "render-output"
	}
	if !strings.HasSuffix(strings.ToLower(name), ".mp4") {
		name += ".mp4"
	}
	return name
}
