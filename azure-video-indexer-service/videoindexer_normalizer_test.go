package main

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

type fakeSignalExtractor struct {
	calls  []string
	result MediaSignals
	err    error
}

func (f *fakeSignalExtractor) Extract(_ context.Context, sourceURL string) (MediaSignals, error) {
	f.calls = append(f.calls, sourceURL)
	if f.err != nil {
		return MediaSignals{}, f.err
	}
	if f.result.SourceURL == "" {
		f.result.SourceURL = redactURL(sourceURL)
	}
	return f.result, nil
}

func TestNormalizeVideoIndexResultFixture(t *testing.T) {
	raw, err := os.ReadFile("testdata/videoindexer_normalization_fixture.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	result, err := normalizeVideoIndexResult(raw, "fallback-video", "Processed")
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	again, err := normalizeVideoIndexResult(raw, "fallback-video", "Processed")
	if err != nil {
		t.Fatalf("normalize again: %v", err)
	}
	if !reflect.DeepEqual(result, again) {
		t.Fatal("normalization is not deterministic")
	}

	if result.VideoID != "video-root-001" {
		t.Fatalf("unexpected video id: %q", result.VideoID)
	}
	if result.State != "processed" {
		t.Fatalf("unexpected state: %q", result.State)
	}
	if result.DurationMs != 12500 {
		t.Fatalf("unexpected duration: %d", result.DurationMs)
	}
	if result.DetectedLanguage != "en-US" || result.SourceLanguage != "en-US" {
		t.Fatalf("unexpected language values: %#v", result)
	}
	if len(result.SourceIDs) < 2 || !containsString(result.SourceIDs, "source-a") || !containsString(result.SourceIDs, "source-b") {
		t.Fatalf("unexpected source ids: %#v", result.SourceIDs)
	}
	if len(result.Videos) != 2 {
		t.Fatalf("unexpected videos: %#v", result.Videos)
	}
	if len(result.Insights.Speakers) != 2 {
		t.Fatalf("unexpected speakers: %#v", result.Insights.Speakers)
	}
	if result.Insights.Speakers[0].TranscriptIDs == nil || len(result.Insights.Speakers[0].TranscriptIDs) == 0 {
		t.Fatalf("missing transcript mapping: %#v", result.Insights.Speakers[0])
	}
	if len(result.Insights.Scenes) != 2 || result.Insights.Scenes[0].StartMs != 0 || result.Insights.Scenes[1].StartMs != 5500 {
		t.Fatalf("unexpected scenes: %#v", result.Insights.Scenes)
	}
	if len(result.Insights.Shots) != 2 {
		t.Fatalf("unexpected shots: %#v", result.Insights.Shots)
	}
	if len(result.Insights.Keyframes) != 3 {
		t.Fatalf("unexpected keyframes: %#v", result.Insights.Keyframes)
	}
	for _, keyframe := range result.Insights.Keyframes {
		if strings.Contains(keyframe.Thumbnail.URL, "sig=") || strings.Contains(keyframe.Thumbnail.URL, "secret") {
			t.Fatalf("thumbnail url leaked credentials: %#v", keyframe.Thumbnail)
		}
	}
	if len(result.Insights.Transcript) != 3 {
		t.Fatalf("unexpected transcript entries: %#v", result.Insights.Transcript)
	}
	if got := findTranscriptText(result.Insights.Transcript, "First line"); got.SpeakerName != "Speaker #1" {
		t.Fatalf("unexpected speaker mapping: %#v", got)
	}
	if got := findTranscriptText(result.Insights.Transcript, "Hola"); got.SpeakerName != "Speaker #2" {
		t.Fatalf("unexpected speaker mapping: %#v", got)
	}
	if len(result.Insights.OCR) != 1 || len(result.Insights.Labels) != 1 || len(result.Insights.Objects) != 1 {
		t.Fatalf("unexpected optional insights: %#v", result.Insights)
	}
}

func TestNormalizeVideoIndexResultMalformedTimecode(t *testing.T) {
	raw, err := os.ReadFile("testdata/videoindexer_malformed_timecode.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	_, err = normalizeVideoIndexResult(raw, "fallback-video", "Processed")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "transcript 1 instance 0 start") || !strings.Contains(err.Error(), "not-a-timecode") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestVideoIndexNormalizerIntegratesSignals(t *testing.T) {
	raw, err := os.ReadFile("testdata/videoindexer_normalization_fixture.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	signalExtractor := &fakeSignalExtractor{
		result: MediaSignals{
			Duration:  17 * time.Second,
			SourceURL: "https://staged.example.com/input.mp4",
		},
	}
	normalizer := NewVideoIndexNormalizer(signalExtractor)
	result, err := normalizer.Normalize(context.Background(), JobDocument{VideoIndexerVideoID: "video-root-001"}, StagedAsset{}, "https://staged.example.com/input.mp4?sig=secret", RawVideoIndex{videoID: "video-root-001", state: "Processed", raw: raw})
	if err != nil {
		t.Fatalf("normalize with signals: %v", err)
	}
	if len(signalExtractor.calls) != 1 {
		t.Fatalf("unexpected signal extractor calls: %d", len(signalExtractor.calls))
	}
	if result.TechnicalSignals == nil || result.TechnicalSignals.SourceURL != "https://staged.example.com/input.mp4" {
		t.Fatalf("unexpected technical signals: %#v", result.TechnicalSignals)
	}
	if strings.Contains(result.TechnicalSignals.SourceURL, "sig=") {
		t.Fatalf("signal source url leaked credentials: %#v", result.TechnicalSignals)
	}
}

func TestParseTimecodeMs(t *testing.T) {
	tests := map[string]int64{
		"0:00:00":         0,
		"0:00:00.1666667": 167,
		"0:00:09.1333333": 9133,
		"0:18:50.2":       (18*60*1000 + 50*1000 + 200),
	}
	for raw, want := range tests {
		got, err := parseTimecodeMs(raw)
		if err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		if got != want {
			t.Fatalf("%s: got %d want %d", raw, got, want)
		}
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func findTranscriptText(entries []VideoIndexTranscript, text string) VideoIndexTranscript {
	for _, entry := range entries {
		if entry.Text == text {
			return entry
		}
	}
	return VideoIndexTranscript{}
}
