package videoprocessing

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestCompareBindingsRecommendsFFmpegGoFirst(t *testing.T) {
	evaluations := CompareBindings()
	if len(evaluations) != 2 {
		t.Fatalf("evaluations = %d, want 2", len(evaluations))
	}
	if evaluations[0].Package != BackendFFmpegGo {
		t.Fatalf("first package = %q, want %q", evaluations[0].Package, BackendFFmpegGo)
	}
	if !strings.Contains(strings.ToLower(evaluations[0].Decision), "recommended") {
		t.Fatalf("first decision should recommend ffmpeg-go: %+v", evaluations[0])
	}
}

func TestRuntimeStatusUsesConfiguredFFmpegAndFFprobePaths(t *testing.T) {
	runner := fakeRunner{responses: map[string][]byte{
		"C:\\tools\\ffmpeg.exe -version":  []byte("ffmpeg version 7.1\n"),
		"C:\\tools\\ffprobe.exe -version": []byte("ffprobe version 7.1\n"),
	}}
	processor := &CLIProcessor{
		Config: FFmpegRuntimeConfig{
			FFmpegPath:  "C:\\tools\\ffmpeg.exe",
			FFprobePath: "C:\\tools\\ffprobe.exe",
		},
		Runner: runner,
	}

	status, err := processor.RuntimeStatus(context.Background())
	if err != nil {
		t.Fatalf("RuntimeStatus returned error: %v", err)
	}
	if !status.Available || status.FFmpegVersion != "ffmpeg version 7.1" || status.FFprobeVersion != "ffprobe version 7.1" {
		t.Fatalf("unexpected status: %+v", status)
	}
}

func TestRuntimeStatusReportsMissingRuntime(t *testing.T) {
	processor := &CLIProcessor{Runner: fakeRunner{err: errors.New("not found")}}

	status, err := processor.RuntimeStatus(context.Background())
	if !errors.Is(err, ErrRuntimeUnavailable) {
		t.Fatalf("expected ErrRuntimeUnavailable, got %v", err)
	}
	if status.Available {
		t.Fatalf("status should not be available: %+v", status)
	}
}

func TestProbeParsesFFprobeJSON(t *testing.T) {
	processor := &CLIProcessor{Runner: fakeRunner{responses: map[string][]byte{
		"ffprobe -v error -print_format json -show_format -show_streams clip.mp4": []byte(`{
			"format": {"duration": "12.345"},
			"streams": [
				{"codec_type":"video","codec_name":"h264","width":3840,"height":2160,"avg_frame_rate":"60000/1001"},
				{"codec_type":"audio","codec_name":"aac"}
			]
		}`),
	}}}

	result, err := processor.Probe(context.Background(), "clip.mp4")
	if err != nil {
		t.Fatalf("Probe returned error: %v", err)
	}
	if result.DurationMS != 12345 || result.Width != 3840 || result.Height != 2160 || result.VideoCodec != "h264" || result.AudioCodec != "aac" {
		t.Fatalf("unexpected probe result: %+v", result)
	}
	if result.FrameRate < 59.93 || result.FrameRate > 59.95 {
		t.Fatalf("frame rate = %f, want about 59.94", result.FrameRate)
	}
}

func TestThumbnailBuildsBoundedCommand(t *testing.T) {
	runner := &recordingRunner{}
	processor := &CLIProcessor{Runner: runner}

	err := processor.Thumbnail(context.Background(), ThumbnailRequest{
		Input:  "clip.mp4",
		Output: "thumb.jpg",
		TimeMS: 1500,
		Width:  640,
	})
	if err != nil {
		t.Fatalf("Thumbnail returned error: %v", err)
	}
	got := strings.Join(append([]string{runner.name}, runner.args...), " ")
	want := "ffmpeg -hide_banner -loglevel error -ss 1.500 -i clip.mp4 -frames:v 1 -vf scale=640:-1 -y thumb.jpg"
	if got != want {
		t.Fatalf("command = %q, want %q", got, want)
	}
}

func TestBuildRenderArgsSupportsTrimConcatScaleAndTextOverlay(t *testing.T) {
	args, err := BuildRenderArgs(RenderRequest{
		ProjectID: "project-1",
		Output:    "render.mp4",
		Clips: []RenderClip{
			{ID: "clip-1", Input: "a.mp4", InMS: 1000, OutMS: 4000},
			{ID: "clip-2", Input: "b.mp4", InMS: 2000, OutMS: 5000},
		},
		TextOverlays: []RenderTextOverlay{
			{ID: "title-1", Text: "Ride: day 1", StartMS: 500, EndMS: 2500},
		},
		Width: 1920,
	})
	if err != nil {
		t.Fatalf("BuildRenderArgs returned error: %v", err)
	}
	command := strings.Join(args, " ")
	for _, want := range []string{
		"-ss 1.000 -t 3.000 -i a.mp4",
		"-ss 2.000 -t 3.000 -i b.mp4",
		"concat=n=2:v=1:a=0",
		"scale=1920:-2",
		"drawtext=text='Ride\\: day 1'",
		"-c:v libx264",
		"-an render.mp4",
	} {
		if !strings.Contains(command, want) {
			t.Fatalf("command %q does not contain %q", command, want)
		}
	}
}

func TestRenderRunsBuiltCommand(t *testing.T) {
	runner := &recordingRunner{}
	processor := &CLIProcessor{Runner: runner}

	job, err := processor.Render(context.Background(), RenderRequest{
		ProjectID: "project-1",
		Output:    "render.mp4",
		Clips:     []RenderClip{{ID: "clip-1", Input: "a.mp4", InMS: 0, OutMS: 1000}},
	})
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}
	if job.Status != "completed" || job.Output != "render.mp4" {
		t.Fatalf("unexpected render job: %+v", job)
	}
	if runner.name != "ffmpeg" {
		t.Fatalf("runner command = %q, want ffmpeg", runner.name)
	}
}

func TestParseProgressLine(t *testing.T) {
	progress := ParseProgressLine("out_time=00:00:05.000\nprogress=continue", 10_000)
	if progress.CurrentMS != 5000 || progress.Percent != 0.5 || progress.Message != "continue" {
		t.Fatalf("unexpected progress: %+v", progress)
	}
}

type fakeRunner struct {
	responses map[string][]byte
	err       error
}

func (r fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	if r.err != nil {
		return nil, r.err
	}
	key := strings.Join(append([]string{name}, args...), " ")
	if response, ok := r.responses[key]; ok {
		return response, nil
	}
	return nil, errors.New("unexpected command: " + key)
}

type recordingRunner struct {
	name string
	args []string
}

func (r *recordingRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.name = name
	r.args = append([]string{}, args...)
	return []byte("ok"), nil
}
