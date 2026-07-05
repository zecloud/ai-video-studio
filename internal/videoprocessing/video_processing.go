package videoprocessing

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

const (
	BackendFFmpegCLI = "ffmpeg-cli"
	BackendFFmpegGo  = "github.com/u2takey/ffmpeg-go"
	BackendFFGo      = "github.com/obinnaokechukwu/ffgo"
)

var (
	ErrRuntimeUnavailable = errors.New("FFmpeg runtime is unavailable")
	ErrInvalidProbeResult = errors.New("invalid ffprobe result")
)

type ProbeResult struct {
	Path       string  `json:"path"`
	DurationMS int64   `json:"durationMs"`
	Width      int     `json:"width"`
	Height     int     `json:"height"`
	FrameRate  float64 `json:"frameRate"`
	VideoCodec string  `json:"videoCodec"`
	AudioCodec string  `json:"audioCodec"`
}

type ThumbnailRequest struct {
	AssetID string `json:"assetId"`
	Input   string `json:"input"`
	Output  string `json:"output"`
	TimeMS  int64  `json:"timeMs"`
	Width   int    `json:"width"`
}

type RenderRequest struct {
	ProjectID    string              `json:"projectId"`
	PresetID     string              `json:"presetId"`
	Output       string              `json:"output"`
	Clips        []RenderClip        `json:"clips"`
	TextOverlays []RenderTextOverlay `json:"textOverlays,omitempty"`
	Width        int                 `json:"width,omitempty"`
	Height       int                 `json:"height,omitempty"`
	VideoCodec   string              `json:"videoCodec,omitempty"`
	AudioCodec   string              `json:"audioCodec,omitempty"`
}

type RenderJob struct {
	ID        string `json:"id"`
	ProjectID string `json:"projectId"`
	Status    string `json:"status"`
	Output    string `json:"output,omitempty"`
	Message   string `json:"message,omitempty"`
	Log       string `json:"log,omitempty"`
}

type RenderProgress struct {
	JobID     string  `json:"jobId"`
	Percent   float64 `json:"percent"`
	CurrentMS int64   `json:"currentMs"`
	Message   string  `json:"message,omitempty"`
}

type RenderClip struct {
	ID    string `json:"id"`
	Input string `json:"input"`
	InMS  int64  `json:"inMs"`
	OutMS int64  `json:"outMs"`
	Muted bool   `json:"muted,omitempty"`
}

type RenderTextOverlay struct {
	ID      string `json:"id"`
	Text    string `json:"text"`
	StartMS int64  `json:"startMs"`
	EndMS   int64  `json:"endMs"`
}

type RuntimeStatus struct {
	Available      bool   `json:"available"`
	Backend        string `json:"backend"`
	FFmpegPath     string `json:"ffmpegPath,omitempty"`
	FFmpegVersion  string `json:"ffmpegVersion,omitempty"`
	FFprobePath    string `json:"ffprobePath,omitempty"`
	FFprobeVersion string `json:"ffprobeVersion,omitempty"`
	Message        string `json:"message"`
}

type BindingEvaluation struct {
	Name            string   `json:"name"`
	Package         string   `json:"package"`
	License         string   `json:"license"`
	IntegrationMode string   `json:"integrationMode"`
	Decision        string   `json:"decision"`
	Strengths       []string `json:"strengths"`
	Risks           []string `json:"risks"`
}

type FFmpegRuntimeConfig struct {
	FFmpegPath  string `json:"ffmpegPath,omitempty"`
	FFprobePath string `json:"ffprobePath,omitempty"`
}

type VideoProcessor interface {
	Probe(context.Context, string) (ProbeResult, error)
	Thumbnail(context.Context, ThumbnailRequest) error
	Render(context.Context, RenderRequest) (RenderJob, error)
	RuntimeStatus(context.Context) (RuntimeStatus, error)
}

type CLIProcessor struct {
	Config FFmpegRuntimeConfig
	Runner CommandRunner
}

type CommandRunner interface {
	Run(context.Context, string, ...string) ([]byte, error)
}

func NewFFmpegCLIProcessor(config FFmpegRuntimeConfig) *CLIProcessor {
	return &CLIProcessor{Config: config, Runner: execCommandRunner{}}
}

func CompareBindings() []BindingEvaluation {
	return []BindingEvaluation{
		{
			Name:            "ffmpeg-go",
			Package:         BackendFFmpegGo,
			License:         "Apache-2.0",
			IntegrationMode: "Pure-Go command builder around the ffmpeg and ffprobe executables.",
			Decision:        "Recommended first implementation path for AI Video Studio editing workflows.",
			Strengths: []string{
				"Good fit for Wails desktop packaging because it keeps Go builds simple and requires only FFmpeg binaries at runtime.",
				"Fluent graph API covers trim, concat, filters, overlays, thumbnails, probe, logs, and progress arguments.",
				"Does not link libav* into the app binary, reducing LGPL/GPL obligations to the distributed FFmpeg executable and codecs.",
			},
			Risks: []string{
				"FFmpeg and ffprobe must be installed, bundled, or configured per platform.",
				"Progress and cancellation are process-level concerns that must be handled by the app wrapper.",
				"Less suitable for custom in-memory frame processing than direct libav bindings.",
			},
		},
		{
			Name:            "ffgo",
			Package:         BackendFFGo,
			License:         "Apache-2.0",
			IntegrationMode: "purego dynamic calls into FFmpeg libav* shared libraries, with optional shim binaries.",
			Decision:        "Defer until frame-level processing or custom I/O is required and packaging has been validated.",
			Strengths: []string{
				"Potentially better for frame-level decode/encode, custom io.Reader/io.Writer paths, hardware acceleration, and direct libav workflows.",
				"Still supports CGO-disabled Go builds by using purego.",
				"API claims broad coverage for decode, encode, filters, stream copy, HLS, and metadata workflows.",
			},
			Risks: []string{
				"Desktop packaging is more complex because compatible FFmpeg shared libraries and optional shims must be shipped or discovered.",
				"Dynamic-library loading behavior must be validated on Windows, macOS, and Linux before product use.",
				"Direct libav distribution can make LGPL/GPL and codec-compliance review more involved than a separate executable.",
			},
		},
	}
}

func (p *CLIProcessor) RuntimeStatus(ctx context.Context) (RuntimeStatus, error) {
	ffmpegPath := configuredPath(p.Config.FFmpegPath, "ffmpeg")
	ffprobePath := configuredPath(p.Config.FFprobePath, "ffprobe")

	ffmpegVersion, ffmpegErr := p.runVersion(ctx, ffmpegPath)
	ffprobeVersion, ffprobeErr := p.runVersion(ctx, ffprobePath)
	status := RuntimeStatus{
		Available:      ffmpegErr == nil && ffprobeErr == nil,
		Backend:        BackendFFmpegCLI,
		FFmpegPath:     ffmpegPath,
		FFmpegVersion:  ffmpegVersion,
		FFprobePath:    ffprobePath,
		FFprobeVersion: ffprobeVersion,
	}
	switch {
	case ffmpegErr != nil && ffprobeErr != nil:
		status.Message = "FFmpeg and ffprobe are not available; configure their paths before editing workflows."
		return status, fmt.Errorf("%w: ffmpeg: %v; ffprobe: %v", ErrRuntimeUnavailable, ffmpegErr, ffprobeErr)
	case ffmpegErr != nil:
		status.Message = "FFmpeg is not available; configure the ffmpeg binary path before rendering."
		return status, fmt.Errorf("%w: ffmpeg: %v", ErrRuntimeUnavailable, ffmpegErr)
	case ffprobeErr != nil:
		status.Message = "ffprobe is not available; configure the ffprobe binary path before probing media."
		return status, fmt.Errorf("%w: ffprobe: %v", ErrRuntimeUnavailable, ffprobeErr)
	default:
		status.Message = "FFmpeg runtime is available for probe, thumbnail, and render workflows."
		return status, nil
	}
}

func (p *CLIProcessor) Probe(ctx context.Context, input string) (ProbeResult, error) {
	if strings.TrimSpace(input) == "" {
		return ProbeResult{}, fmt.Errorf("%w: input path is required", ErrInvalidProbeResult)
	}
	ffprobePath := configuredPath(p.Config.FFprobePath, "ffprobe")
	output, err := p.runner().Run(ctx, ffprobePath,
		"-v", "error",
		"-print_format", "json",
		"-show_format",
		"-show_streams",
		input,
	)
	if err != nil {
		return ProbeResult{}, err
	}
	return parseProbeResult(input, output)
}

func (p *CLIProcessor) Thumbnail(ctx context.Context, req ThumbnailRequest) error {
	if strings.TrimSpace(req.Input) == "" {
		return fmt.Errorf("%w: thumbnail input path is required", ErrRuntimeUnavailable)
	}
	if strings.TrimSpace(req.Output) == "" {
		return fmt.Errorf("%w: thumbnail output path is required", ErrRuntimeUnavailable)
	}
	ffmpegPath := configuredPath(p.Config.FFmpegPath, "ffmpeg")
	args := []string{
		"-hide_banner",
		"-loglevel", "error",
		"-ss", formatSeconds(req.TimeMS),
		"-i", req.Input,
		"-frames:v", "1",
	}
	if req.Width > 0 {
		args = append(args, "-vf", fmt.Sprintf("scale=%d:-1", req.Width))
	}
	args = append(args, "-y", req.Output)
	_, err := p.runner().Run(ctx, ffmpegPath, args...)
	return err
}

func (p *CLIProcessor) Render(ctx context.Context, req RenderRequest) (RenderJob, error) {
	args, err := BuildRenderArgs(req)
	if err != nil {
		return RenderJob{}, err
	}
	ffmpegPath := configuredPath(p.Config.FFmpegPath, "ffmpeg")
	output, err := p.runner().Run(ctx, ffmpegPath, args...)
	job := RenderJob{
		ID:        "render-" + req.ProjectID,
		ProjectID: req.ProjectID,
		Output:    req.Output,
		Log:       string(output),
	}
	if err != nil {
		job.Status = "failed"
		job.Message = err.Error()
		return job, err
	}
	job.Status = "completed"
	job.Message = "Render completed."
	return job, nil
}

func (p *CLIProcessor) runVersion(ctx context.Context, command string) (string, error) {
	output, err := p.runner().Run(ctx, command, "-version")
	if err != nil {
		return "", err
	}
	firstLine := strings.TrimSpace(strings.SplitN(string(output), "\n", 2)[0])
	if firstLine == "" {
		return "", fmt.Errorf("%w: empty version output", ErrRuntimeUnavailable)
	}
	return firstLine, nil
}

func (p *CLIProcessor) runner() CommandRunner {
	if p.Runner != nil {
		return p.Runner
	}
	return execCommandRunner{}
}

type execCommandRunner struct{}

func (execCommandRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return output, fmt.Errorf("%s %s failed: %w: %s", name, strings.Join(args, " "), err, message)
	}
	return output, nil
}

type ffprobeOutput struct {
	Format  ffprobeFormat   `json:"format"`
	Streams []ffprobeStream `json:"streams"`
}

type ffprobeFormat struct {
	Duration string `json:"duration"`
}

type ffprobeStream struct {
	CodecType    string `json:"codec_type"`
	CodecName    string `json:"codec_name"`
	Width        int    `json:"width"`
	Height       int    `json:"height"`
	AvgFrameRate string `json:"avg_frame_rate"`
	RFrameRate   string `json:"r_frame_rate"`
}

func parseProbeResult(input string, raw []byte) (ProbeResult, error) {
	var probed ffprobeOutput
	if err := json.Unmarshal(raw, &probed); err != nil {
		return ProbeResult{}, fmt.Errorf("%w: %v", ErrInvalidProbeResult, err)
	}
	result := ProbeResult{Path: input}
	if probed.Format.Duration != "" {
		durationSeconds, err := strconv.ParseFloat(probed.Format.Duration, 64)
		if err != nil {
			return ProbeResult{}, fmt.Errorf("%w: duration: %v", ErrInvalidProbeResult, err)
		}
		result.DurationMS = int64(durationSeconds * 1000)
	}
	for _, stream := range probed.Streams {
		switch stream.CodecType {
		case "video":
			if result.VideoCodec == "" {
				result.Width = stream.Width
				result.Height = stream.Height
				result.VideoCodec = stream.CodecName
				result.FrameRate = parseFrameRate(firstNonEmpty(stream.AvgFrameRate, stream.RFrameRate))
			}
		case "audio":
			if result.AudioCodec == "" {
				result.AudioCodec = stream.CodecName
			}
		}
	}
	if result.VideoCodec == "" && result.AudioCodec == "" {
		return ProbeResult{}, fmt.Errorf("%w: no audio or video stream found", ErrInvalidProbeResult)
	}
	return result, nil
}

func parseFrameRate(value string) float64 {
	if value == "" || value == "0/0" {
		return 0
	}
	parts := strings.Split(value, "/")
	if len(parts) == 1 {
		parsed, _ := strconv.ParseFloat(parts[0], 64)
		return parsed
	}
	numerator, nErr := strconv.ParseFloat(parts[0], 64)
	denominator, dErr := strconv.ParseFloat(parts[1], 64)
	if nErr != nil || dErr != nil || denominator == 0 {
		return 0
	}
	return numerator / denominator
}

func configuredPath(configured, fallback string) string {
	if strings.TrimSpace(configured) != "" {
		return configured
	}
	return fallback
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func formatSeconds(ms int64) string {
	return strconv.FormatFloat(float64(ms)/1000, 'f', 3, 64)
}

func BuildRenderArgs(req RenderRequest) ([]string, error) {
	if strings.TrimSpace(req.ProjectID) == "" {
		return nil, fmt.Errorf("%w: project id is required", ErrRuntimeUnavailable)
	}
	if strings.TrimSpace(req.Output) == "" {
		return nil, fmt.Errorf("%w: render output path is required", ErrRuntimeUnavailable)
	}
	if len(req.Clips) == 0 {
		return nil, fmt.Errorf("%w: at least one render clip is required", ErrRuntimeUnavailable)
	}

	args := []string{"-hide_banner", "-y", "-loglevel", "info"}
	for _, clip := range req.Clips {
		if strings.TrimSpace(clip.Input) == "" {
			return nil, fmt.Errorf("%w: render clip input path is required", ErrRuntimeUnavailable)
		}
		if clip.OutMS <= clip.InMS {
			return nil, fmt.Errorf("%w: render clip outMs must be greater than inMs", ErrRuntimeUnavailable)
		}
		args = append(args, "-ss", formatSeconds(clip.InMS), "-t", formatSeconds(clip.OutMS-clip.InMS), "-i", clip.Input)
	}

	filter, err := buildVideoFilter(req)
	if err != nil {
		return nil, err
	}
	args = append(args, "-filter_complex", filter, "-map", "[outv]")
	videoCodec := strings.TrimSpace(req.VideoCodec)
	if videoCodec == "" {
		videoCodec = "libx264"
	}
	args = append(args, "-c:v", videoCodec, "-pix_fmt", "yuv420p")
	if strings.TrimSpace(req.AudioCodec) != "" {
		args = append(args, "-c:a", req.AudioCodec)
	} else {
		args = append(args, "-an")
	}
	args = append(args, req.Output)
	return args, nil
}

func ParseProgressLine(line string, durationMS int64) RenderProgress {
	progress := RenderProgress{Message: strings.TrimSpace(line)}
	for _, part := range strings.Split(line, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		switch key {
		case "out_time_ms":
			current, err := strconv.ParseInt(value, 10, 64)
			if err == nil {
				progress.CurrentMS = current / 1000
			}
		case "out_time":
			progress.CurrentMS = parseTimestampMS(value)
		case "progress":
			progress.Message = value
		}
	}
	if durationMS > 0 && progress.CurrentMS > 0 {
		progress.Percent = min(1, float64(progress.CurrentMS)/float64(durationMS))
	}
	return progress
}

func buildVideoFilter(req RenderRequest) (string, error) {
	parts := make([]string, 0, len(req.Clips)+len(req.TextOverlays)+1)
	labels := make([]string, 0, len(req.Clips))
	for index := range req.Clips {
		label := fmt.Sprintf("v%d", index)
		parts = append(parts, fmt.Sprintf("[%d:v]setpts=PTS-STARTPTS[%s]", index, label))
		labels = append(labels, "["+label+"]")
	}
	current := "joined"
	if len(labels) == 1 {
		parts = append(parts, fmt.Sprintf("%snull[%s]", labels[0], current))
	} else {
		parts = append(parts, fmt.Sprintf("%sconcat=n=%d:v=1:a=0[%s]", strings.Join(labels, ""), len(labels), current))
	}
	if req.Width > 0 || req.Height > 0 {
		next := "scaled"
		width := req.Width
		height := req.Height
		if width <= 0 {
			width = -2
		}
		if height <= 0 {
			height = -2
		}
		parts = append(parts, fmt.Sprintf("[%s]scale=%d:%d[%s]", current, width, height, next))
		current = next
	}
	for index, overlay := range req.TextOverlays {
		if strings.TrimSpace(overlay.Text) == "" {
			continue
		}
		if overlay.EndMS <= overlay.StartMS {
			return "", fmt.Errorf("%w: text overlay endMs must be greater than startMs", ErrRuntimeUnavailable)
		}
		next := fmt.Sprintf("text%d", index)
		parts = append(parts, fmt.Sprintf("[%s]drawtext=text='%s':x=(w-text_w)/2:y=h-(text_h*3):enable='between(t,%.3f,%.3f)'[%s]",
			current,
			escapeDrawtext(overlay.Text),
			float64(overlay.StartMS)/1000,
			float64(overlay.EndMS)/1000,
			next,
		))
		current = next
	}
	parts = append(parts, fmt.Sprintf("[%s]format=yuv420p[outv]", current))
	return strings.Join(parts, ";"), nil
}

func escapeDrawtext(text string) string {
	replacer := strings.NewReplacer(
		"\\", "\\\\",
		":", "\\:",
		"'", "\\'",
		"%", "\\%",
		"\n", " ",
		"\r", " ",
	)
	return replacer.Replace(text)
}

func parseTimestampMS(value string) int64 {
	segments := strings.Split(value, ":")
	if len(segments) != 3 {
		return 0
	}
	hours, hErr := strconv.ParseInt(segments[0], 10, 64)
	minutes, mErr := strconv.ParseInt(segments[1], 10, 64)
	seconds, sErr := strconv.ParseFloat(segments[2], 64)
	if hErr != nil || mErr != nil || sErr != nil {
		return 0
	}
	return (hours*3600+minutes*60)*1000 + int64(seconds*1000)
}
