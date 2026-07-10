package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"testing"
	"time"
)

type scriptedRunner struct {
	mu    sync.Mutex
	calls []commandCall
	run   func(ctx context.Context, name string, args ...string) ([]byte, []byte, error)
}

type commandCall struct {
	name string
	args []string
}

func (r *scriptedRunner) Run(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
	r.mu.Lock()
	r.calls = append(r.calls, commandCall{name: name, args: append([]string(nil), args...)})
	run := r.run
	r.mu.Unlock()
	if run == nil {
		return nil, nil, nil
	}
	return run(ctx, name, args...)
}

func (r *scriptedRunner) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

func TestMediaSignalExtractorExtractsTechnicalSignalsAndSilences(t *testing.T) {
	runner := &scriptedRunner{}
	runner.run = func(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
		switch {
		case name == "ffprobe":
			return []byte(`{
				"format":{"duration":"12.5"},
				"streams":[
					{"codec_type":"video","codec_name":"h264","width":3840,"height":2160,"avg_frame_rate":"30000/1001","r_frame_rate":"24000/1001"},
					{"codec_type":"audio","codec_name":"aac","channels":"2","sample_rate":"48000"}
				]
			}`), nil, nil
		case name == "ffmpeg" && containsArg(args, "-loglevel", "error"):
			return nil, nil, nil
		case name == "ffmpeg" && containsArg(args, "-loglevel", "info"):
			return nil, []byte(strings.Join([]string{
				"[silencedetect @ 0x1] silence_start: 0",
				"[silencedetect @ 0x1] silence_end: 1.200 | silence_duration: 1.200",
				"[silencedetect @ 0x1] silence_start: 3.000",
				"[silencedetect @ 0x1] silence_end: 4.000 | silence_duration: 1.000",
				"[silencedetect @ 0x1] silence_start: 3.950",
				"[silencedetect @ 0x1] silence_end: 5.000 | silence_duration: 1.050",
				"[silencedetect @ 0x1] silence_start: 9.500",
			}, "\n")), nil
		default:
			t.Fatalf("unexpected command: %s %v", name, args)
		}
		return nil, nil, nil
	}

	extractor := NewMediaSignalExtractor(runner, MediaSignalConfig{})
	result, err := extractor.Extract(context.Background(), "https://example.blob.core.windows.net/input/video.mp4?sig=secret&st=1")
	if err != nil {
		t.Fatalf("extract: %v", err)
	}

	if result.SourceURL != "https://example.blob.core.windows.net/input/video.mp4" {
		t.Fatalf("unexpected source url: %q", result.SourceURL)
	}
	if !result.Video.Present || result.Video.Codec != "h264" || result.Video.Width != 3840 || result.Video.Height != 2160 {
		t.Fatalf("unexpected video metadata: %#v", result.Video)
	}
	if diff := math.Abs(result.Video.FPS - 29.97002997002997); diff > 0.0001 {
		t.Fatalf("unexpected fps: %.8f", result.Video.FPS)
	}
	if !result.Audio.Present || result.Audio.Codec != "aac" || result.Audio.Channels != 2 || result.Audio.SampleRate != 48000 {
		t.Fatalf("unexpected audio metadata: %#v", result.Audio)
	}
	if result.Duration != 12*time.Second+500*time.Millisecond {
		t.Fatalf("unexpected duration: %s", result.Duration)
	}
	want := []SilenceInterval{
		{Start: 0, End: 1200 * time.Millisecond},
		{Start: 3 * time.Second, End: 5 * time.Second},
		{Start: 9500 * time.Millisecond, End: 12500 * time.Millisecond},
	}
	if len(result.Silences) != len(want) {
		t.Fatalf("unexpected silences: %#v", result.Silences)
	}
	for i := range want {
		if result.Silences[i] != want[i] {
			t.Fatalf("silence %d mismatch: got %#v want %#v", i, result.Silences[i], want[i])
		}
	}
	if runner.callCount() != 3 {
		t.Fatalf("expected three commands, got %d", runner.callCount())
	}
}

func TestMediaSignalExtractorHandlesNoAudioAndMalformedFPSJSON(t *testing.T) {
	t.Run("no audio and malformed fps fallback", func(t *testing.T) {
		runner := &scriptedRunner{}
		runner.run = func(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
			switch {
			case name == "ffprobe":
				return []byte(`{
					"format":{"duration":"4.0"},
					"streams":[
						{"codec_type":"video","codec_name":"hevc","width":1920,"height":1080,"avg_frame_rate":"not-a-fps","r_frame_rate":"24000/1001"}
					]
				}`), nil, nil
			case name == "ffmpeg" && containsArg(args, "-loglevel", "error"):
				return nil, nil, nil
			case name == "ffmpeg" && containsArg(args, "-loglevel", "info"):
				t.Fatalf("silencedetect should not run without audio: %s %v", name, args)
			}
			return nil, nil, nil
		}

		extractor := NewMediaSignalExtractor(runner, MediaSignalConfig{})
		result, err := extractor.Extract(context.Background(), "https://example.blob.core.windows.net/input/video.mp4?sig=secret")
		if err != nil {
			t.Fatalf("extract: %v", err)
		}
		if result.Audio.Present {
			t.Fatalf("expected no audio: %#v", result.Audio)
		}
		if diff := math.Abs(result.Video.FPS - 23.976023976023978); diff > 0.0001 {
			t.Fatalf("unexpected fallback fps: %.8f", result.Video.FPS)
		}
		if runner.callCount() != 2 {
			t.Fatalf("expected probe + validation, got %d calls", runner.callCount())
		}
	})

	t.Run("malformed json", func(t *testing.T) {
		runner := &scriptedRunner{}
		runner.run = func(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
			if name != "ffprobe" {
				t.Fatalf("unexpected command: %s %v", name, args)
			}
			return []byte(`{"format":`), nil, nil
		}

		extractor := NewMediaSignalExtractor(runner, MediaSignalConfig{})
		_, err := extractor.Extract(context.Background(), "https://example.blob.core.windows.net/input/video.mp4?sig=secret")
		if err == nil {
			t.Fatal("expected error")
		}
		if !strings.Contains(err.Error(), "invalid ffprobe JSON") {
			t.Fatalf("unexpected error: %v", err)
		}
		if strings.Contains(err.Error(), "sig=secret") {
			t.Fatalf("query string leaked in error: %v", err)
		}
	})
}

func TestMediaSignalExtractorCancellationAndRedaction(t *testing.T) {
	t.Run("cancellation", func(t *testing.T) {
		runner := &scriptedRunner{}
		runner.run = func(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
			switch name {
			case "ffprobe":
				return []byte(`{
					"format":{"duration":"8.0"},
					"streams":[
						{"codec_type":"video","codec_name":"h264","width":1280,"height":720,"avg_frame_rate":"30/1"},
						{"codec_type":"audio","codec_name":"aac","channels":"2","sample_rate":"48000"}
					]
				}`), nil, nil
			case "ffmpeg":
				<-ctx.Done()
				return nil, nil, ctx.Err()
			default:
				t.Fatalf("unexpected command: %s %v", name, args)
			}
			return nil, nil, nil
		}

		extractor := NewMediaSignalExtractor(runner, MediaSignalConfig{
			ValidationTimeout: 20 * time.Millisecond,
			SilenceTimeout:    20 * time.Millisecond,
			ProbeTimeout:      time.Second,
		})
		_, err := extractor.Extract(context.Background(), "https://example.blob.core.windows.net/input/video.mp4?sig=secret&st=1")
		if err == nil {
			t.Fatal("expected error")
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("expected deadline exceeded, got %v", err)
		}
		if strings.Contains(err.Error(), "sig=secret") {
			t.Fatalf("query string leaked in error: %v", err)
		}
	})

	t.Run("redacted command failure", func(t *testing.T) {
		sourceURL := "https://example.blob.core.windows.net/input/video.mp4?sig=secret&st=1"
		runner := &scriptedRunner{}
		runner.run = func(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
			if name != "ffprobe" {
				t.Fatalf("unexpected command: %s %v", name, args)
			}
			return nil, []byte(fmt.Sprintf("error opening %s", sourceURL)), errors.New("ffprobe failed")
		}

		extractor := NewMediaSignalExtractor(runner, MediaSignalConfig{})
		_, err := extractor.Extract(context.Background(), sourceURL)
		if err == nil {
			t.Fatal("expected error")
		}
		if strings.Contains(err.Error(), "sig=secret") {
			t.Fatalf("query string leaked in error: %v", err)
		}
		if !strings.Contains(err.Error(), "https://example.blob.core.windows.net/input/video.mp4") {
			t.Fatalf("expected sanitized url in error, got %v", err)
		}
	})
}

func containsArg(args []string, key, value string) bool {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == key && args[i+1] == value {
			return true
		}
	}
	return false
}
