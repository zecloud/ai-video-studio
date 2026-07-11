package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

type CommandRunner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, []byte, error)
}

type ExecCommandRunner struct{}

func (ExecCommandRunner) Run(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.Bytes(), stderr.Bytes(), err
}

type MediaSignalConfig struct {
	ProbeTimeout       time.Duration
	ValidationTimeout  time.Duration
	SilenceTimeout     time.Duration
	ValidationSample   time.Duration
	SilenceThreshold   string
	SilenceMinDuration time.Duration
	SilenceMergeGap    time.Duration
	FFProbeBinary      string
	FFmpegBinary       string
}

type MediaSignalExtractor struct {
	runner CommandRunner
	cfg    MediaSignalConfig
	obs    *Observability
}

type MediaSignals struct {
	SourceURL string
	Duration  time.Duration
	Video     MediaVideoSignals
	Audio     MediaAudioSignals
	Silences  []SilenceInterval
}

type MediaVideoSignals struct {
	Present bool
	Codec   string
	Width   int
	Height  int
	FPS     float64
}

type MediaAudioSignals struct {
	Present    bool
	Codec      string
	Channels   int
	SampleRate int
}

type SilenceInterval struct {
	Start time.Duration
	End   time.Duration
}

func NewMediaSignalExtractor(runner CommandRunner, cfg MediaSignalConfig) *MediaSignalExtractor {
	if runner == nil {
		runner = ExecCommandRunner{}
	}
	cfg = cfg.withDefaults()
	return &MediaSignalExtractor{runner: runner, cfg: cfg}
}

func (cfg MediaSignalConfig) withDefaults() MediaSignalConfig {
	if cfg.ProbeTimeout <= 0 {
		cfg.ProbeTimeout = 30 * time.Second
	}
	if cfg.ValidationTimeout <= 0 {
		cfg.ValidationTimeout = 20 * time.Second
	}
	if cfg.SilenceTimeout <= 0 {
		cfg.SilenceTimeout = 2 * time.Minute
	}
	if cfg.ValidationSample <= 0 {
		cfg.ValidationSample = 5 * time.Second
	}
	if cfg.SilenceThreshold == "" {
		cfg.SilenceThreshold = "-35dB"
	}
	if cfg.SilenceMinDuration <= 0 {
		cfg.SilenceMinDuration = 500 * time.Millisecond
	}
	if cfg.SilenceMergeGap <= 0 {
		cfg.SilenceMergeGap = 10 * time.Millisecond
	}
	if cfg.FFProbeBinary == "" {
		cfg.FFProbeBinary = "ffprobe"
	}
	if cfg.FFmpegBinary == "" {
		cfg.FFmpegBinary = "ffmpeg"
	}
	return cfg
}

func (e *MediaSignalExtractor) Extract(ctx context.Context, sourceURL string) (signals MediaSignals, err error) {
	if e == nil {
		return MediaSignals{}, errors.New("media signal extractor is nil")
	}
	if strings.TrimSpace(sourceURL) == "" {
		return MediaSignals{}, errors.New("source url is required")
	}
	sourceURL = strings.TrimSpace(sourceURL)
	cfg := e.cfg.withDefaults()
	redactedURL := redactURL(sourceURL)
	start := time.Now()
	var span trace.Span
	if e.obs != nil {
		ctx, span = e.obs.StartSpan(ctx, "ffmpeg.signals", attribute.String("stage", "ffmpeg.signals"))
		defer func() {
			e.obs.FinishSpan(ctx, span, "ffmpeg.signals", start, []attribute.KeyValue{attribute.String("stage", "ffmpeg.signals")}, err)
		}()
	}

	probeOut, err := e.runProbe(ctx, sourceURL, cfg)
	if err != nil {
		return MediaSignals{}, err
	}
	signals, err = parseProbeOutput(probeOut)
	if err != nil {
		return MediaSignals{}, fmt.Errorf("ffprobe %s: %w", redactedURL, err)
	}
	signals.SourceURL = redactedURL

	if validateErr := e.validateDecode(ctx, sourceURL, cfg); validateErr != nil {
		err = validateErr
		return MediaSignals{}, err
	}

	if signals.Audio.Present {
		silences, silenceErr := e.detectSilences(ctx, sourceURL, signals.Duration, cfg)
		if silenceErr != nil {
			err = silenceErr
			return MediaSignals{}, err
		}
		signals.Silences = silences
	}

	return signals, nil
}

func (e *MediaSignalExtractor) runProbe(ctx context.Context, sourceURL string, cfg MediaSignalConfig) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, cfg.ProbeTimeout)
	defer cancel()

	args := []string{
		"-hide_banner",
		"-v", "error",
		"-nostdin",
		"-show_format",
		"-show_streams",
		"-print_format", "json",
		"-show_entries", "format=duration:stream=index,codec_type,codec_name,width,height,avg_frame_rate,r_frame_rate,channels,sample_rate,duration",
		sourceURL,
	}
	stdout, stderr, err := e.runner.Run(ctx, cfg.FFProbeBinary, args...)
	if err != nil {
		return nil, wrapCommandError("ffprobe", sourceURL, stderr, err)
	}
	return stdout, nil
}

func (e *MediaSignalExtractor) validateDecode(ctx context.Context, sourceURL string, cfg MediaSignalConfig) error {
	ctx, cancel := context.WithTimeout(ctx, cfg.ValidationTimeout)
	defer cancel()

	args := []string{
		"-hide_banner",
		"-nostats",
		"-loglevel", "error",
		"-nostdin",
		"-t", formatSeconds(cfg.ValidationSample),
		"-i", sourceURL,
		"-f", "null",
		"-",
	}
	_, stderr, err := e.runner.Run(ctx, cfg.FFmpegBinary, args...)
	if err != nil {
		return wrapCommandError("ffmpeg validation", sourceURL, stderr, err)
	}
	return nil
}

func (e *MediaSignalExtractor) detectSilences(ctx context.Context, sourceURL string, duration time.Duration, cfg MediaSignalConfig) ([]SilenceInterval, error) {
	ctx, cancel := context.WithTimeout(ctx, cfg.SilenceTimeout)
	defer cancel()

	filter := fmt.Sprintf("silencedetect=noise=%s:d=%s", cfg.SilenceThreshold, formatSeconds(cfg.SilenceMinDuration))
	args := []string{
		"-hide_banner",
		"-nostats",
		"-loglevel", "info",
		"-nostdin",
		"-i", sourceURL,
		"-vn",
		"-sn",
		"-dn",
		"-af", filter,
		"-f", "null",
		"-",
	}
	_, stderr, err := e.runner.Run(ctx, cfg.FFmpegBinary, args...)
	if err != nil {
		return nil, wrapCommandError("ffmpeg silencedetect", sourceURL, stderr, err)
	}
	return parseSilenceEvents(string(stderr), duration, cfg.SilenceMergeGap), nil
}

func parseProbeOutput(data []byte) (MediaSignals, error) {
	var payload probeOutput
	if err := json.Unmarshal(data, &payload); err != nil {
		return MediaSignals{}, fmt.Errorf("invalid ffprobe JSON: %w", err)
	}

	var signals MediaSignals
	if d, ok := parseJSONSeconds(payload.Format.Duration); ok {
		signals.Duration = d
	}

	for _, stream := range payload.Streams {
		codecType := jsonText(stream["codec_type"])
		switch codecType {
		case "video":
			if signals.Video.Present {
				continue
			}
			signals.Video.Present = true
			signals.Video.Codec = jsonText(stream["codec_name"])
			signals.Video.Width = jsonInt(stream["width"])
			signals.Video.Height = jsonInt(stream["height"])
			if fps, ok := parseFPS(jsonText(stream["avg_frame_rate"])); ok {
				signals.Video.FPS = fps
			} else if fps, ok := parseFPS(jsonText(stream["r_frame_rate"])); ok {
				signals.Video.FPS = fps
			}
			if signals.Duration == 0 {
				if d, ok := parseJSONSeconds(jsonText(stream["duration"])); ok {
					signals.Duration = d
				}
			}
		case "audio":
			if signals.Audio.Present {
				continue
			}
			signals.Audio.Present = true
			signals.Audio.Codec = jsonText(stream["codec_name"])
			signals.Audio.Channels = jsonInt(stream["channels"])
			signals.Audio.SampleRate = jsonInt(stream["sample_rate"])
			if signals.Duration == 0 {
				if d, ok := parseJSONSeconds(jsonText(stream["duration"])); ok {
					signals.Duration = d
				}
			}
		}
	}

	return signals, nil
}

type probeOutput struct {
	Format  probeFormat                  `json:"format"`
	Streams []map[string]json.RawMessage `json:"streams"`
}

type probeFormat struct {
	Duration string `json:"duration"`
}

func parseSilenceEvents(stderr string, duration time.Duration, mergeGap time.Duration) []SilenceInterval {
	var intervals []SilenceInterval
	var openStart *time.Duration

	flush := func(start, end time.Duration) {
		start = clampDuration(start, 0, duration)
		if duration > 0 {
			end = clampDuration(end, 0, duration)
		}
		if end < start {
			start, end = end, start
		}
		if duration > 0 {
			if start >= duration {
				return
			}
			if end > duration {
				end = duration
			}
		}
		intervals = append(intervals, SilenceInterval{Start: start, End: end})
	}

	for _, line := range strings.Split(stderr, "\n") {
		if idx := strings.Index(line, "silence_start:"); idx >= 0 {
			value := parseLogSeconds(line[idx+len("silence_start:"):])
			if openStart != nil {
				flush(*openStart, value)
			}
			start := value
			openStart = &start
		}
		if idx := strings.Index(line, "silence_end:"); idx >= 0 {
			end := parseLogSeconds(line[idx+len("silence_end:"):])
			start := end
			if sidx := strings.Index(line, "silence_duration:"); sidx >= 0 {
				if dur := parseLogSeconds(line[sidx+len("silence_duration:"):]); dur > 0 {
					start = end - dur
				}
			}
			if openStart != nil {
				flush(*openStart, end)
				openStart = nil
			} else {
				flush(start, end)
			}
		}
	}

	if openStart != nil && duration > 0 {
		flush(*openStart, duration)
	}

	return mergeSilences(intervals, mergeGap)
}

func mergeSilences(intervals []SilenceInterval, mergeGap time.Duration) []SilenceInterval {
	if len(intervals) == 0 {
		return nil
	}
	sorted := append([]SilenceInterval(nil), intervals...)
	for i := range sorted {
		if sorted[i].End < sorted[i].Start {
			sorted[i].Start, sorted[i].End = sorted[i].End, sorted[i].Start
		}
	}
	sortSilenceIntervals(sorted)
	out := make([]SilenceInterval, 0, len(sorted))
	for _, current := range sorted {
		if current.End < current.Start {
			continue
		}
		if len(out) == 0 {
			out = append(out, current)
			continue
		}
		last := &out[len(out)-1]
		if current.Start <= last.End+mergeGap {
			if current.End > last.End {
				last.End = current.End
			}
			continue
		}
		out = append(out, current)
	}
	return out
}

func sortSilenceIntervals(intervals []SilenceInterval) {
	for i := 1; i < len(intervals); i++ {
		current := intervals[i]
		j := i - 1
		for j >= 0 && (intervals[j].Start > current.Start || (intervals[j].Start == current.Start && intervals[j].End > current.End)) {
			intervals[j+1] = intervals[j]
			j--
		}
		intervals[j+1] = current
	}
}

func clampDuration(v, min, max time.Duration) time.Duration {
	if v < min {
		return min
	}
	if max > 0 && v > max {
		return max
	}
	return v
}

func parseFPS(raw string) (float64, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "0/0" || raw == "N/A" {
		return 0, false
	}
	if strings.Contains(raw, "/") {
		parts := strings.SplitN(raw, "/", 2)
		if len(parts) != 2 {
			return 0, false
		}
		num, err := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
		if err != nil {
			return 0, false
		}
		den, err := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
		if err != nil || den == 0 {
			return 0, false
		}
		if num < 0 || den < 0 {
			return 0, false
		}
		return num / den, true
	}
	fps, err := strconv.ParseFloat(raw, 64)
	if err != nil || fps < 0 {
		return 0, false
	}
	return fps, true
}

func parseJSONSeconds(raw string) (time.Duration, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "N/A" {
		return 0, false
	}
	seconds, err := strconv.ParseFloat(raw, 64)
	if err != nil || seconds < 0 {
		return 0, false
	}
	return time.Duration(seconds * float64(time.Second)), true
}

func parseLogSeconds(raw string) time.Duration {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, ":")
	raw = strings.TrimSpace(strings.SplitN(raw, "|", 2)[0])
	raw = strings.TrimSpace(strings.TrimPrefix(raw, ":"))
	if raw == "" {
		return 0
	}
	if v, err := strconv.ParseFloat(raw, 64); err == nil && v >= 0 {
		return time.Duration(v * float64(time.Second))
	}
	return 0
}

func jsonText(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return ""
	}
	if raw[0] == '"' {
		var out string
		if err := json.Unmarshal(raw, &out); err == nil {
			return strings.TrimSpace(out)
		}
	}
	return strings.TrimSpace(strings.Trim(string(raw), "\""))
}

func jsonInt(raw json.RawMessage) int {
	text := jsonText(raw)
	if text == "" {
		return 0
	}
	v, err := strconv.Atoi(text)
	if err != nil {
		return 0
	}
	return v
}

func formatSeconds(v time.Duration) string {
	if v <= 0 {
		return "0"
	}
	seconds := float64(v) / float64(time.Second)
	return strconv.FormatFloat(seconds, 'f', -1, 64)
}

func wrapCommandError(label, sourceURL string, stderr []byte, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%s: %w", label, err)
	}
	message := strings.TrimSpace(redactText(string(stderr), sourceURL))
	if message == "" {
		message = redactText(err.Error(), sourceURL)
	}
	if message == "" {
		message = "command failed"
	}
	return fmt.Errorf("%s: %s", label, message)
}
