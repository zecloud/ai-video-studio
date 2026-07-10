package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

type VideoIndexResult struct {
	VideoID          string             `json:"videoId"`
	State            string             `json:"state,omitempty"`
	DurationMs       int64              `json:"durationMs,omitempty"`
	DetectedLanguage string             `json:"detectedLanguage,omitempty"`
	SourceLanguage   string             `json:"sourceLanguage,omitempty"`
	SourceLanguages  []string           `json:"sourceLanguages,omitempty"`
	SourceIDs        []string           `json:"sourceIds,omitempty"`
	Videos           []VideoIndexVideo  `json:"videos,omitempty"`
	Insights         VideoIndexInsights `json:"insights"`
	TechnicalSignals *MediaSignals      `json:"technicalSignals,omitempty"`
}

type VideoIndexVideo struct {
	ID               string  `json:"id"`
	SourceID         string  `json:"sourceId,omitempty"`
	DurationMs       int64   `json:"durationMs,omitempty"`
	DetectedLanguage string  `json:"detectedLanguage,omitempty"`
	Confidence       float64 `json:"confidence,omitempty"`
}

type VideoIndexInsights struct {
	Speakers   []VideoIndexSpeaker    `json:"speakers,omitempty"`
	Scenes     []VideoIndexScene      `json:"scenes,omitempty"`
	Shots      []VideoIndexShot       `json:"shots,omitempty"`
	Keyframes  []VideoIndexKeyframe   `json:"keyframes,omitempty"`
	Transcript []VideoIndexTranscript `json:"transcript,omitempty"`
	OCR        []VideoIndexOCR        `json:"ocr,omitempty"`
	Labels     []VideoIndexLabel      `json:"labels,omitempty"`
	Objects    []VideoIndexObject     `json:"objects,omitempty"`
}

type VideoIndexSpeaker struct {
	ID            string   `json:"id"`
	Name          string   `json:"name,omitempty"`
	Language      string   `json:"language,omitempty"`
	TranscriptIDs []string `json:"transcriptIds,omitempty"`
}

type VideoIndexScene struct {
	ID         string        `json:"id"`
	IndexID    int64         `json:"indexId,omitempty"`
	SourceID   string        `json:"sourceId,omitempty"`
	StartMs    int64         `json:"startMs"`
	EndMs      int64         `json:"endMs"`
	Confidence float64       `json:"confidence,omitempty"`
	Thumbnail  *ThumbnailRef `json:"thumbnail,omitempty"`
}

type VideoIndexShot struct {
	ID          string   `json:"id"`
	IndexID     int64    `json:"indexId,omitempty"`
	SourceID    string   `json:"sourceId,omitempty"`
	StartMs     int64    `json:"startMs"`
	EndMs       int64    `json:"endMs"`
	Tags        []string `json:"tags,omitempty"`
	KeyframeIDs []string `json:"keyframeIds,omitempty"`
	Confidence  float64  `json:"confidence,omitempty"`
}

type VideoIndexKeyframe struct {
	ID         string       `json:"id"`
	IndexID    int64        `json:"indexId,omitempty"`
	SourceID   string       `json:"sourceId,omitempty"`
	ShotID     string       `json:"shotId,omitempty"`
	StartMs    int64        `json:"startMs"`
	EndMs      int64        `json:"endMs"`
	Confidence float64      `json:"confidence,omitempty"`
	Thumbnail  ThumbnailRef `json:"thumbnail"`
}

type ThumbnailRef struct {
	VideoID     string `json:"videoId,omitempty"`
	ThumbnailID string `json:"thumbnailId,omitempty"`
	URL         string `json:"url,omitempty"`
}

type VideoIndexTranscript struct {
	ID          string  `json:"id"`
	IndexID     int64   `json:"indexId,omitempty"`
	SourceID    string  `json:"sourceId,omitempty"`
	SpeakerID   string  `json:"speakerId,omitempty"`
	SpeakerName string  `json:"speakerName,omitempty"`
	Language    string  `json:"language,omitempty"`
	StartMs     int64   `json:"startMs"`
	EndMs       int64   `json:"endMs"`
	Text        string  `json:"text"`
	Confidence  float64 `json:"confidence,omitempty"`
}

type VideoIndexOCR struct {
	ID         string         `json:"id"`
	IndexID    int64          `json:"indexId,omitempty"`
	SourceID   string         `json:"sourceId,omitempty"`
	Language   string         `json:"language,omitempty"`
	Text       string         `json:"text"`
	StartMs    int64          `json:"startMs"`
	EndMs      int64          `json:"endMs"`
	Confidence float64        `json:"confidence,omitempty"`
	Bounds     VideoIndexRect `json:"bounds,omitempty"`
}

type VideoIndexRect struct {
	Left   int `json:"left,omitempty"`
	Top    int `json:"top,omitempty"`
	Width  int `json:"width,omitempty"`
	Height int `json:"height,omitempty"`
	Angle  int `json:"angle,omitempty"`
}

type VideoIndexLabel struct {
	ID          string  `json:"id"`
	IndexID     int64   `json:"indexId,omitempty"`
	SourceID    string  `json:"sourceId,omitempty"`
	Language    string  `json:"language,omitempty"`
	Name        string  `json:"name"`
	ReferenceID string  `json:"referenceId,omitempty"`
	StartMs     int64   `json:"startMs"`
	EndMs       int64   `json:"endMs"`
	Confidence  float64 `json:"confidence,omitempty"`
}

type VideoIndexObject struct {
	ID          string       `json:"id"`
	IndexID     int64        `json:"indexId,omitempty"`
	SourceID    string       `json:"sourceId,omitempty"`
	Type        string       `json:"type"`
	DisplayName string       `json:"displayName,omitempty"`
	WikiDataID  string       `json:"wikiDataId,omitempty"`
	StartMs     int64        `json:"startMs"`
	EndMs       int64        `json:"endMs"`
	Confidence  float64      `json:"confidence,omitempty"`
	Thumbnail   ThumbnailRef `json:"thumbnail,omitempty"`
}

type VideoIndexNormalizer interface {
	Normalize(ctx context.Context, job JobDocument, asset StagedAsset, readURL string, index VideoIndexData) (VideoIndexResult, error)
}

type DefaultVideoIndexNormalizer struct {
	signals MediaSignalExtractorInterface
	obs     *Observability
}

type MediaSignalExtractorInterface interface {
	Extract(ctx context.Context, sourceURL string) (MediaSignals, error)
}

func NewVideoIndexNormalizer(signals MediaSignalExtractorInterface) *DefaultVideoIndexNormalizer {
	return &DefaultVideoIndexNormalizer{signals: signals}
}

func (n *DefaultVideoIndexNormalizer) Normalize(ctx context.Context, job JobDocument, asset StagedAsset, readURL string, index VideoIndexData) (result VideoIndexResult, err error) {
	if n == nil {
		return VideoIndexResult{}, fmt.Errorf("video index normalizer is nil")
	}
	if index == nil {
		return VideoIndexResult{}, fmt.Errorf("video index data is required")
	}
	start := time.Now()
	var span trace.Span
	if n.obs != nil {
		ctx, span = n.obs.StartSpan(ctx, "vi.normalize", attribute.String("stage", "vi.normalize"))
		defer func() {
			n.obs.FinishSpan(ctx, span, "vi.normalize", start, []attribute.KeyValue{attribute.String("stage", "vi.normalize")}, err)
		}()
	}
	raw := index.RawJSON()
	if len(raw) == 0 {
		return VideoIndexResult{}, fmt.Errorf("video index data is empty")
	}

	normalized, err := normalizeVideoIndexResult(raw, index.VideoID(), index.State())
	if err != nil {
		return VideoIndexResult{}, err
	}

	if n.signals != nil {
		signals, err := n.signals.Extract(ctx, readURL)
		if err != nil {
			return VideoIndexResult{}, err
		}
		normalized.TechnicalSignals = &signals
		if normalized.DurationMs <= 0 && signals.Duration > 0 {
			normalized.DurationMs = signals.Duration.Milliseconds()
		}
	}

	if normalized.VideoID == "" {
		normalized.VideoID = job.VideoIndexerVideoID
	}
	return normalized, nil
}

func normalizeVideoIndexResult(raw []byte, fallbackVideoID, fallbackState string) (VideoIndexResult, error) {
	var payload rawVideoIndexResponse
	if err := json.Unmarshal(raw, &payload); err != nil {
		return VideoIndexResult{}, fmt.Errorf("invalid video index JSON: %w", err)
	}

	result := VideoIndexResult{
		VideoID: fallbackVideoID,
		State:   normalizeVideoState(firstNonEmpty(payload.State, fallbackState)),
	}
	result.VideoID = firstNonEmpty(payload.VideoID, payload.ID, result.VideoID)
	if result.VideoID == "" {
		return VideoIndexResult{}, fmt.Errorf("video index response is missing a video id")
	}

	dur, hasDuration, err := resolveVideoDuration(payload)
	if err != nil {
		return VideoIndexResult{}, err
	}
	if hasDuration {
		result.DurationMs = dur
	}
	result.DetectedLanguage = firstNonEmpty(payload.Language, payload.Insights.Language, payload.Insights.SourceLanguage)
	result.SourceLanguage = firstNonEmpty(payload.SourceLanguage, payload.Insights.SourceLanguage)
	result.SourceLanguages = dedupeStrings(append(append([]string(nil), payload.SourceLanguages...), payload.Insights.SourceLanguages...))

	videos, sourceIDs, err := normalizeVideos(result.VideoID, payload.Videos)
	if err != nil {
		return VideoIndexResult{}, err
	}
	result.Videos = videos
	primarySourceID := primaryVideoSourceID(payload)
	result.SourceIDs = dedupeStrings(append(result.SourceIDs, append([]string{primarySourceID}, sourceIDs...)...))

	insightsBlocks := []rawVideoIndexInsights{payload.Insights}
	insightsSources := []string{primarySourceID}
	for _, video := range payload.Videos {
		if hasRawInsights(video.Insights) {
			insightsBlocks = append(insightsBlocks, video.Insights)
			insightsSources = append(insightsSources, firstNonEmpty(video.SourceID, video.ID, primarySourceID))
		}
	}
	for i, block := range insightsBlocks {
		insights, err := normalizeInsights(result.VideoID, result.DurationMs, insightsSources[i], result.SourceIDs, block, nil)
		if err != nil {
			return VideoIndexResult{}, err
		}
		mergeVideoIndexInsights(&result.Insights, insights)
	}
	result.SourceIDs = dedupeStrings(append(result.SourceIDs, collectInsightSourceIDs(result.VideoID, payload.Insights, payload.Videos)...))
	sortVideoIndexResult(&result)
	speakerNames := map[string]string{}
	for _, speaker := range result.Insights.Speakers {
		if speaker.ID != "" && speaker.Name != "" {
			speakerNames[speaker.ID] = speaker.Name
		}
	}
	for i := range result.Insights.Transcript {
		if name := speakerNames[result.Insights.Transcript[i].SpeakerID]; name != "" {
			result.Insights.Transcript[i].SpeakerName = name
		}
	}
	return result, nil
}

type rawVideoIndexResponse struct {
	ID              string                `json:"id"`
	VideoID         string                `json:"videoId"`
	State           string                `json:"state"`
	Duration        string                `json:"duration"`
	Language        string                `json:"language"`
	SourceLanguage  string                `json:"sourceLanguage"`
	SourceLanguages []string              `json:"sourceLanguages"`
	Videos          []rawVideoIndexVideo  `json:"videos"`
	Insights        rawVideoIndexInsights `json:"insights"`
}

type rawVideoIndexVideo struct {
	ID         string                `json:"id"`
	SourceID   string                `json:"sourceId"`
	Duration   string                `json:"duration"`
	Language   string                `json:"language"`
	Confidence float64               `json:"confidence"`
	Insights   rawVideoIndexInsights `json:"insights"`
}

type rawVideoIndexInsights struct {
	Duration        string             `json:"duration"`
	Language        string             `json:"language"`
	SourceLanguage  string             `json:"sourceLanguage"`
	SourceLanguages []string           `json:"sourceLanguages"`
	Speakers        []rawVideoSpeaker  `json:"speakers"`
	Scenes          []rawTimelineGroup `json:"scenes"`
	Shots           []rawShotGroup     `json:"shots"`
	Transcript      []rawTranscript    `json:"transcript"`
	OCR             []rawOCRGroup      `json:"ocr"`
	Labels          []rawLabelGroup    `json:"labels"`
	Objects         []rawObjectGroup   `json:"detectedObjects"`
}

type rawVideoSpeaker struct {
	ID          json.RawMessage   `json:"id"`
	Name        string            `json:"name"`
	DisplayName string            `json:"displayName"`
	Instances   []rawTimeInstance `json:"instances"`
}

type rawTimelineGroup struct {
	ID        int64             `json:"id"`
	Instances []rawTimeInstance `json:"instances"`
}

type rawShotGroup struct {
	ID        int64              `json:"id"`
	Tags      []string           `json:"tags"`
	KeyFrames []rawKeyframeGroup `json:"keyFrames"`
	Instances []rawTimeInstance  `json:"instances"`
}

type rawKeyframeGroup struct {
	ID        int64             `json:"id"`
	Instances []rawTimeInstance `json:"instances"`
}

type rawTranscript struct {
	ID         int64             `json:"id"`
	Text       string            `json:"text"`
	Confidence float64           `json:"confidence"`
	SpeakerID  json.RawMessage   `json:"speakerId"`
	Language   string            `json:"language"`
	Instances  []rawTimeInstance `json:"instances"`
}

type rawOCRGroup struct {
	ID         int64             `json:"id"`
	Text       string            `json:"text"`
	Confidence float64           `json:"confidence"`
	Left       int               `json:"left"`
	Top        int               `json:"top"`
	Width      int               `json:"width"`
	Height     int               `json:"height"`
	Angle      int               `json:"angle"`
	Language   string            `json:"language"`
	Instances  []rawTimeInstance `json:"instances"`
}

type rawLabelGroup struct {
	ID          int64             `json:"id"`
	Name        string            `json:"name"`
	ReferenceID string            `json:"referenceId"`
	Language    string            `json:"language"`
	Instances   []rawTimeInstance `json:"instances"`
}

type rawObjectGroup struct {
	ID           int64             `json:"id"`
	Type         string            `json:"type"`
	ThumbnailID  string            `json:"thumbnailId"`
	ThumbnailURL string            `json:"thumbnailUrl"`
	DisplayName  string            `json:"displayName"`
	WikiDataID   string            `json:"wikiDataId"`
	Instances    []rawTimeInstance `json:"instances"`
}

type rawTimeInstance struct {
	Start         string   `json:"start"`
	End           string   `json:"end"`
	AdjustedStart string   `json:"adjustedStart"`
	AdjustedEnd   string   `json:"adjustedEnd"`
	Confidence    *float64 `json:"confidence,omitempty"`
	ThumbnailID   string   `json:"thumbnailId,omitempty"`
	ThumbnailURL  string   `json:"thumbnailUrl,omitempty"`
}

func normalizeVideos(resultVideoID string, videos []rawVideoIndexVideo) ([]VideoIndexVideo, []string, error) {
	out := make([]VideoIndexVideo, 0, len(videos))
	sourceIDs := make([]string, 0, len(videos))
	seen := map[string]struct{}{}
	for i, raw := range videos {
		durationMs, hasDuration, err := parseDurationField(raw.Duration)
		if err != nil {
			return nil, nil, fmt.Errorf("videos[%d].duration: %w", i, err)
		}
		if !hasDuration {
			durationMs = 0
		}
		videoID := firstNonEmpty(raw.ID, raw.SourceID)
		if videoID == "" {
			videoID = stableID("video", resultVideoID, strconv.Itoa(i), raw.SourceID, raw.Language, raw.Duration)
		}
		sourceID := firstNonEmpty(raw.SourceID, raw.ID)
		entry := VideoIndexVideo{
			ID:               stableID("video", resultVideoID, videoID, sourceID, raw.Language, raw.Duration, fmt.Sprintf("%.6f", raw.Confidence)),
			SourceID:         sourceID,
			DurationMs:       durationMs,
			DetectedLanguage: firstNonEmpty(raw.Language),
			Confidence:       raw.Confidence,
		}
		if _, ok := seen[entry.ID]; ok {
			continue
		}
		seen[entry.ID] = struct{}{}
		out = append(out, entry)
		sourceIDs = append(sourceIDs, sourceID)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].SourceID == out[j].SourceID {
			return out[i].ID < out[j].ID
		}
		return out[i].SourceID < out[j].SourceID
	})
	return out, dedupeStrings(sourceIDs), nil
}

func normalizeInsights(resultVideoID string, durationMs int64, primarySourceID string, sourceIDs []string, insights rawVideoIndexInsights, videos []rawVideoIndexVideo) (VideoIndexInsights, error) {
	result := VideoIndexInsights{}
	sourceIDs = dedupeStrings(sourceIDs)

	speakerNames := map[string]string{}
	speakerTranscriptIDs := map[string][]string{}
	for _, raw := range insights.Speakers {
		id, err := rawSpeakerID(raw.ID)
		if err != nil {
			return VideoIndexInsights{}, err
		}
		if id == "" {
			continue
		}
		name := firstNonEmpty(raw.Name, raw.DisplayName, "Speaker "+id)
		speakerNames[id] = name
	}

	for _, raw := range append([]rawTranscript(nil), insights.Transcript...) {
		entries, err := normalizeTranscript(resultVideoID, durationMs, primarySourceID, sourceIDs, speakerNames, raw)
		if err != nil {
			return VideoIndexInsights{}, err
		}
		for _, entry := range entries {
			speakerTranscriptIDs[entry.SpeakerID] = append(speakerTranscriptIDs[entry.SpeakerID], entry.ID)
			result.Transcript = append(result.Transcript, entry)
			if entry.SpeakerID != "" && speakerNames[entry.SpeakerID] == "" {
				speakerNames[entry.SpeakerID] = defaultSpeakerLabel(entry.SpeakerID)
			}
		}
	}

	for _, raw := range append([]rawTimelineGroup(nil), insights.Scenes...) {
		items, err := normalizeTimelineGroup("scene", resultVideoID, durationMs, primarySourceID, sourceIDs, raw.ID, raw.Instances, func(startMs, endMs int64, inst rawTimeInstance) VideoIndexScene {
			return VideoIndexScene{
				ID:         stableID("scene", resultVideoID, strconv.FormatInt(raw.ID, 10), strconv.FormatInt(startMs, 10), strconv.FormatInt(endMs, 10)),
				IndexID:    raw.ID,
				SourceID:   pickSourceID(primarySourceID, sourceIDs),
				StartMs:    startMs,
				EndMs:      endMs,
				Confidence: confidenceFromInstance(inst),
			}
		})
		if err != nil {
			return VideoIndexInsights{}, err
		}
		result.Scenes = append(result.Scenes, items...)
	}

	var shotKeyframeMap = map[string][]string{}
	for _, raw := range append([]rawShotGroup(nil), insights.Shots...) {
		shotIDBase := strconv.FormatInt(raw.ID, 10)
		shotSource := pickSourceID(primarySourceID, sourceIDs)
		shotItems, err := normalizeTimelineGroup("shot", resultVideoID, durationMs, primarySourceID, sourceIDs, raw.ID, raw.Instances, func(startMs, endMs int64, inst rawTimeInstance) VideoIndexShot {
			id := stableID("shot", resultVideoID, shotIDBase, strconv.FormatInt(startMs, 10), strconv.FormatInt(endMs, 10), strings.Join(raw.Tags, "|"))
			return VideoIndexShot{
				ID:          id,
				IndexID:     raw.ID,
				SourceID:    shotSource,
				StartMs:     startMs,
				EndMs:       endMs,
				Tags:        dedupeStrings(append([]string(nil), raw.Tags...)),
				KeyframeIDs: nil,
				Confidence:  confidenceFromInstance(inst),
			}
		})
		if err != nil {
			return VideoIndexInsights{}, err
		}
		for i := range shotItems {
			shotItems[i].KeyframeIDs = shotKeyframeMap[shotItems[i].ID]
		}
		result.Shots = append(result.Shots, shotItems...)

		for _, keyframe := range raw.KeyFrames {
			items, err := normalizeKeyframes(resultVideoID, durationMs, primarySourceID, sourceIDs, firstShotID(shotItems), keyframe)
			if err != nil {
				return VideoIndexInsights{}, err
			}
			for _, item := range items {
				result.Keyframes = append(result.Keyframes, item)
				shotKeyframeMap[item.ShotID] = append(shotKeyframeMap[item.ShotID], item.ID)
			}
		}
	}

	for i := range result.Shots {
		result.Shots[i].KeyframeIDs = dedupeStrings(shotKeyframeMap[result.Shots[i].ID])
	}

	for _, raw := range append([]rawOCRGroup(nil), insights.OCR...) {
		entries, err := normalizeOCR(resultVideoID, durationMs, primarySourceID, sourceIDs, raw)
		if err != nil {
			return VideoIndexInsights{}, err
		}
		result.OCR = append(result.OCR, entries...)
	}

	for _, raw := range append([]rawLabelGroup(nil), insights.Labels...) {
		entries, err := normalizeLabels(resultVideoID, durationMs, primarySourceID, sourceIDs, raw)
		if err != nil {
			return VideoIndexInsights{}, err
		}
		result.Labels = append(result.Labels, entries...)
	}

	for _, raw := range append([]rawObjectGroup(nil), insights.Objects...) {
		entries, err := normalizeObjects(resultVideoID, durationMs, primarySourceID, sourceIDs, raw)
		if err != nil {
			return VideoIndexInsights{}, err
		}
		result.Objects = append(result.Objects, entries...)
	}

	result.Speakers = normalizeSpeakers(speakerNames, speakerTranscriptIDs)
	for i := range result.Transcript {
		if name := speakerNames[result.Transcript[i].SpeakerID]; name != "" {
			result.Transcript[i].SpeakerName = name
		} else if result.Transcript[i].SpeakerID != "" {
			result.Transcript[i].SpeakerName = defaultSpeakerLabel(result.Transcript[i].SpeakerID)
		}
	}
	sortInsights(&result)
	return result, nil
}

func normalizeSpeakers(names map[string]string, transcripts map[string][]string) []VideoIndexSpeaker {
	ids := make([]string, 0, len(names))
	for id := range names {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]VideoIndexSpeaker, 0, len(ids))
	for _, id := range ids {
		out = append(out, VideoIndexSpeaker{
			ID:            id,
			Name:          names[id],
			TranscriptIDs: dedupeStrings(transcripts[id]),
		})
	}
	return out
}

func normalizeTranscript(resultVideoID string, durationMs int64, primarySourceID string, sourceIDs []string, speakerNames map[string]string, raw rawTranscript) ([]VideoIndexTranscript, error) {
	insts, err := normalizeInstances("transcript", resultVideoID, durationMs, primarySourceID, sourceIDs, raw.ID, raw.Instances)
	if err != nil {
		return nil, err
	}
	out := make([]VideoIndexTranscript, 0, len(insts))
	speakerID, err := rawSpeakerID(raw.SpeakerID)
	if err != nil {
		return nil, err
	}
	for _, inst := range insts {
		id := stableID("transcript", resultVideoID, strconv.FormatInt(raw.ID, 10), speakerID, strings.TrimSpace(raw.Text), strconv.FormatInt(inst.startMs, 10), strconv.FormatInt(inst.endMs, 10))
		out = append(out, VideoIndexTranscript{
			ID:          id,
			IndexID:     raw.ID,
			SourceID:    pickSourceID(primarySourceID, sourceIDs),
			SpeakerID:   speakerID,
			SpeakerName: speakerNames[speakerID],
			Language:    firstNonEmpty(raw.Language),
			StartMs:     inst.startMs,
			EndMs:       inst.endMs,
			Text:        strings.TrimSpace(raw.Text),
			Confidence:  raw.Confidence,
		})
	}
	return out, nil
}

func normalizeKeyframes(resultVideoID string, durationMs int64, primarySourceID string, sourceIDs []string, shotID string, raw rawKeyframeGroup) ([]VideoIndexKeyframe, error) {
	insts, err := normalizeInstances("keyframe", resultVideoID, durationMs, primarySourceID, sourceIDs, raw.ID, raw.Instances)
	if err != nil {
		return nil, err
	}
	out := make([]VideoIndexKeyframe, 0, len(insts))
	for _, inst := range insts {
		id := stableID("keyframe", resultVideoID, shotID, strconv.FormatInt(raw.ID, 10), strconv.FormatInt(inst.startMs, 10), strconv.FormatInt(inst.endMs, 10), inst.thumbnailID, inst.thumbnailURL)
		thumbnail := ThumbnailRef{
			VideoID:     resultVideoID,
			ThumbnailID: firstNonEmpty(inst.thumbnailID, rawKeyframeThumbnail(raw.Instances)),
			URL:         sanitizeThumbnailURL(firstNonEmpty(inst.thumbnailURL, rawThumbnailURL(raw.Instances))),
		}
		out = append(out, VideoIndexKeyframe{
			ID:         id,
			IndexID:    raw.ID,
			SourceID:   pickSourceID(primarySourceID, sourceIDs),
			ShotID:     shotID,
			StartMs:    inst.startMs,
			EndMs:      inst.endMs,
			Confidence: confidenceFromInstance(inst.raw),
			Thumbnail:  thumbnail,
		})
	}
	return out, nil
}

func normalizeOCR(resultVideoID string, durationMs int64, primarySourceID string, sourceIDs []string, raw rawOCRGroup) ([]VideoIndexOCR, error) {
	insts, err := normalizeInstances("ocr", resultVideoID, durationMs, primarySourceID, sourceIDs, raw.ID, raw.Instances)
	if err != nil {
		return nil, err
	}
	out := make([]VideoIndexOCR, 0, len(insts))
	for _, inst := range insts {
		id := stableID("ocr", resultVideoID, strconv.FormatInt(raw.ID, 10), strings.TrimSpace(raw.Text), strconv.FormatInt(inst.startMs, 10), strconv.FormatInt(inst.endMs, 10))
		out = append(out, VideoIndexOCR{
			ID:         id,
			IndexID:    raw.ID,
			SourceID:   pickSourceID(primarySourceID, sourceIDs),
			Language:   firstNonEmpty(raw.Language),
			Text:       strings.TrimSpace(raw.Text),
			StartMs:    inst.startMs,
			EndMs:      inst.endMs,
			Confidence: raw.Confidence,
			Bounds:     VideoIndexRect{Left: raw.Left, Top: raw.Top, Width: raw.Width, Height: raw.Height, Angle: raw.Angle},
		})
	}
	return out, nil
}

func normalizeLabels(resultVideoID string, durationMs int64, primarySourceID string, sourceIDs []string, raw rawLabelGroup) ([]VideoIndexLabel, error) {
	insts, err := normalizeInstances("label", resultVideoID, durationMs, primarySourceID, sourceIDs, raw.ID, raw.Instances)
	if err != nil {
		return nil, err
	}
	out := make([]VideoIndexLabel, 0, len(insts))
	for _, inst := range insts {
		id := stableID("label", resultVideoID, strconv.FormatInt(raw.ID, 10), raw.Name, raw.ReferenceID, strconv.FormatInt(inst.startMs, 10), strconv.FormatInt(inst.endMs, 10))
		out = append(out, VideoIndexLabel{
			ID:          id,
			IndexID:     raw.ID,
			SourceID:    pickSourceID(primarySourceID, sourceIDs),
			Language:    firstNonEmpty(raw.Language),
			Name:        strings.TrimSpace(raw.Name),
			ReferenceID: strings.TrimSpace(raw.ReferenceID),
			StartMs:     inst.startMs,
			EndMs:       inst.endMs,
			Confidence:  confidenceFromInstance(inst.raw),
		})
	}
	return out, nil
}

func normalizeObjects(resultVideoID string, durationMs int64, primarySourceID string, sourceIDs []string, raw rawObjectGroup) ([]VideoIndexObject, error) {
	insts, err := normalizeInstances("object", resultVideoID, durationMs, primarySourceID, sourceIDs, raw.ID, raw.Instances)
	if err != nil {
		return nil, err
	}
	out := make([]VideoIndexObject, 0, len(insts))
	for _, inst := range insts {
		id := stableID("object", resultVideoID, strconv.FormatInt(raw.ID, 10), raw.Type, raw.DisplayName, raw.WikiDataID, strconv.FormatInt(inst.startMs, 10), strconv.FormatInt(inst.endMs, 10))
		out = append(out, VideoIndexObject{
			ID:          id,
			IndexID:     raw.ID,
			SourceID:    pickSourceID(primarySourceID, sourceIDs),
			Type:        strings.TrimSpace(raw.Type),
			DisplayName: strings.TrimSpace(raw.DisplayName),
			WikiDataID:  strings.TrimSpace(raw.WikiDataID),
			StartMs:     inst.startMs,
			EndMs:       inst.endMs,
			Confidence:  confidenceFromInstance(inst.raw),
			Thumbnail: ThumbnailRef{
				VideoID:     resultVideoID,
				ThumbnailID: firstNonEmpty(raw.ThumbnailID, inst.thumbnailID),
				URL:         sanitizeThumbnailURL(firstNonEmpty(raw.ThumbnailURL, inst.thumbnailURL)),
			},
		})
	}
	return out, nil
}

type normalizedInstance struct {
	startMs      int64
	endMs        int64
	confidence   float64
	raw          rawTimeInstance
	thumbnailID  string
	thumbnailURL string
}

func normalizeTimelineGroup[T any](kind, resultVideoID string, durationMs int64, primarySourceID string, sourceIDs []string, rawID int64, instances []rawTimeInstance, build func(startMs, endMs int64, inst rawTimeInstance) T) ([]T, error) {
	insts, err := normalizeInstances(kind, resultVideoID, durationMs, primarySourceID, sourceIDs, rawID, instances)
	if err != nil {
		return nil, err
	}
	out := make([]T, 0, len(insts))
	for _, inst := range insts {
		out = append(out, build(inst.startMs, inst.endMs, inst.raw))
	}
	return out, nil
}

func normalizeInstances(kind, resultVideoID string, durationMs int64, primarySourceID string, sourceIDs []string, rawID int64, instances []rawTimeInstance) ([]normalizedInstance, error) {
	if len(instances) == 0 {
		return nil, fmt.Errorf("%s %d has no instances", kind, rawID)
	}
	out := make([]normalizedInstance, 0, len(instances))
	seen := map[string]struct{}{}
	for i, inst := range instances {
		startRaw := firstNonEmpty(inst.AdjustedStart, inst.Start)
		endRaw := firstNonEmpty(inst.AdjustedEnd, inst.End)
		startMs, err := parseTimecodeMs(startRaw)
		if err != nil {
			return nil, fmt.Errorf("%s %d instance %d start: %w", kind, rawID, i, err)
		}
		endMs, err := parseTimecodeMs(endRaw)
		if err != nil {
			return nil, fmt.Errorf("%s %d instance %d end: %w", kind, rawID, i, err)
		}
		startMs, endMs, err = clampInterval(kind, rawID, i, startMs, endMs, durationMs)
		if err != nil {
			return nil, err
		}
		key := fmt.Sprintf("%d:%d:%s:%s", startMs, endMs, inst.ThumbnailID, inst.ThumbnailURL)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, normalizedInstance{
			startMs:      startMs,
			endMs:        endMs,
			confidence:   confidenceFromInstance(inst),
			raw:          inst,
			thumbnailID:  inst.ThumbnailID,
			thumbnailURL: inst.ThumbnailURL,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].startMs == out[j].startMs {
			if out[i].endMs == out[j].endMs {
				return out[i].thumbnailID < out[j].thumbnailID
			}
			return out[i].endMs < out[j].endMs
		}
		return out[i].startMs < out[j].startMs
	})
	return out, nil
}

func clampInterval(kind string, rawID int64, instance int, startMs, endMs, durationMs int64) (int64, int64, error) {
	if startMs < 0 || endMs < 0 {
		return 0, 0, fmt.Errorf("%s %d instance %d has negative timecode", kind, rawID, instance)
	}
	if durationMs > 0 {
		if startMs > durationMs {
			startMs = durationMs
		}
		if endMs > durationMs {
			endMs = durationMs
		}
	}
	if endMs <= startMs {
		return 0, 0, fmt.Errorf("%s %d instance %d has invalid range [%d,%d)", kind, rawID, instance, startMs, endMs)
	}
	return startMs, endMs, nil
}

func resolveVideoDuration(payload rawVideoIndexResponse) (int64, bool, error) {
	candidates := []string{
		payload.Duration,
		payload.Insights.Duration,
	}
	for _, video := range payload.Videos {
		candidates = append(candidates, video.Duration)
		if video.Insights.Duration != "" {
			candidates = append(candidates, video.Insights.Duration)
		}
	}
	var best int64
	var ok bool
	for _, raw := range candidates {
		ms, has, err := parseDurationField(raw)
		if err != nil {
			return 0, false, err
		}
		if !has {
			continue
		}
		if !ok || ms > best {
			best = ms
			ok = true
		}
	}
	return best, ok, nil
}

func primaryVideoSourceID(payload rawVideoIndexResponse) string {
	for _, video := range payload.Videos {
		if id := firstNonEmpty(video.SourceID, video.ID); id != "" {
			return id
		}
	}
	return firstNonEmpty(payload.ID, payload.VideoID)
}

func parseDurationField(raw string) (int64, bool, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false, nil
	}
	ms, err := parseTimecodeMs(raw)
	if err != nil {
		return 0, false, err
	}
	return ms, true, nil
}

func parseTimecodeMs(raw string) (int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, fmt.Errorf("timecode is empty")
	}
	parts := strings.Split(raw, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, fmt.Errorf("invalid timecode %q", raw)
	}
	hours := int64(0)
	minutesPart := parts[len(parts)-2]
	secondsPart := parts[len(parts)-1]
	if len(parts) == 3 {
		var err error
		hours, err = parseNonNegativeInt(parts[0], "hours")
		if err != nil {
			return 0, err
		}
	}
	minutes, err := parseNonNegativeInt(minutesPart, "minutes")
	if err != nil {
		return 0, err
	}
	seconds, fracMs, err := parseSecondsPart(secondsPart)
	if err != nil {
		return 0, err
	}
	total := hours*3600000 + minutes*60000 + seconds*1000 + fracMs
	if total < 0 {
		return 0, fmt.Errorf("timecode %q overflowed", raw)
	}
	return total, nil
}

func parseNonNegativeInt(raw, field string) (int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, fmt.Errorf("%s is empty", field)
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid %s value %q", field, raw)
	}
	if value < 0 {
		return 0, fmt.Errorf("%s value %q must be non-negative", field, raw)
	}
	return value, nil
}

func parseSecondsPart(raw string) (int64, int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, 0, fmt.Errorf("seconds are empty")
	}
	if strings.Contains(raw, "-") {
		return 0, 0, fmt.Errorf("seconds value %q must be non-negative", raw)
	}
	whole := raw
	frac := ""
	if idx := strings.IndexByte(raw, '.'); idx >= 0 {
		whole = raw[:idx]
		frac = raw[idx+1:]
	}
	seconds, err := parseNonNegativeInt(whole, "seconds")
	if err != nil {
		return 0, 0, err
	}
	if frac == "" {
		return seconds, 0, nil
	}
	if len(frac) > 12 {
		return 0, 0, fmt.Errorf("seconds fraction %q is too precise", raw)
	}
	fracValue, err := strconv.ParseInt(frac, 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid seconds fraction %q", raw)
	}
	scale := pow10Int64(len(frac))
	ms := (fracValue*1000 + scale/2) / scale
	return seconds, ms, nil
}

func rawSpeakerID(raw json.RawMessage) (string, error) {
	raw = json.RawMessage(strings.TrimSpace(string(raw)))
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	if raw[0] == '"' {
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return "", fmt.Errorf("invalid speaker id: %w", err)
		}
		return strings.TrimSpace(text), nil
	}
	var number json.Number
	if err := json.Unmarshal(raw, &number); err != nil {
		return "", fmt.Errorf("invalid speaker id: %w", err)
	}
	return number.String(), nil
}

func confidenceFromInstance(inst rawTimeInstance) float64 {
	if inst.Confidence == nil {
		return 0
	}
	return *inst.Confidence
}

func rawKeyframeThumbnail(instances []rawTimeInstance) string {
	for _, inst := range instances {
		if inst.ThumbnailID != "" {
			return inst.ThumbnailID
		}
	}
	return ""
}

func rawThumbnailURL(instances []rawTimeInstance) string {
	for _, inst := range instances {
		if inst.ThumbnailURL != "" {
			return inst.ThumbnailURL
		}
	}
	return ""
}

func sanitizeThumbnailURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	return redactURL(raw)
}

func sortVideoIndexResult(result *VideoIndexResult) {
	sort.SliceStable(result.Videos, func(i, j int) bool {
		if result.Videos[i].SourceID == result.Videos[j].SourceID {
			return result.Videos[i].ID < result.Videos[j].ID
		}
		return result.Videos[i].SourceID < result.Videos[j].SourceID
	})
	result.Videos = dedupeByID(result.Videos, func(item VideoIndexVideo) string { return item.ID })
	sortInsights(&result.Insights)
}

func sortInsights(insights *VideoIndexInsights) {
	sort.SliceStable(insights.Speakers, func(i, j int) bool { return insights.Speakers[i].ID < insights.Speakers[j].ID })
	insights.Speakers = dedupeByID(insights.Speakers, func(item VideoIndexSpeaker) string { return item.ID })
	sort.SliceStable(insights.Scenes, func(i, j int) bool {
		if insights.Scenes[i].StartMs == insights.Scenes[j].StartMs {
			return insights.Scenes[i].ID < insights.Scenes[j].ID
		}
		return insights.Scenes[i].StartMs < insights.Scenes[j].StartMs
	})
	sort.SliceStable(insights.Shots, func(i, j int) bool {
		if insights.Shots[i].StartMs == insights.Shots[j].StartMs {
			return insights.Shots[i].ID < insights.Shots[j].ID
		}
		return insights.Shots[i].StartMs < insights.Shots[j].StartMs
	})
	sort.SliceStable(insights.Keyframes, func(i, j int) bool {
		if insights.Keyframes[i].StartMs == insights.Keyframes[j].StartMs {
			return insights.Keyframes[i].ID < insights.Keyframes[j].ID
		}
		return insights.Keyframes[i].StartMs < insights.Keyframes[j].StartMs
	})
	sort.SliceStable(insights.Transcript, func(i, j int) bool {
		if insights.Transcript[i].StartMs == insights.Transcript[j].StartMs {
			return insights.Transcript[i].ID < insights.Transcript[j].ID
		}
		return insights.Transcript[i].StartMs < insights.Transcript[j].StartMs
	})
	sort.SliceStable(insights.OCR, func(i, j int) bool {
		if insights.OCR[i].StartMs == insights.OCR[j].StartMs {
			return insights.OCR[i].ID < insights.OCR[j].ID
		}
		return insights.OCR[i].StartMs < insights.OCR[j].StartMs
	})
	sort.SliceStable(insights.Labels, func(i, j int) bool {
		if insights.Labels[i].StartMs == insights.Labels[j].StartMs {
			return insights.Labels[i].ID < insights.Labels[j].ID
		}
		return insights.Labels[i].StartMs < insights.Labels[j].StartMs
	})
	sort.SliceStable(insights.Objects, func(i, j int) bool {
		if insights.Objects[i].StartMs == insights.Objects[j].StartMs {
			return insights.Objects[i].ID < insights.Objects[j].ID
		}
		return insights.Objects[i].StartMs < insights.Objects[j].StartMs
	})
	insights.Scenes = dedupeByID(insights.Scenes, func(item VideoIndexScene) string { return item.ID })
	insights.Shots = dedupeByID(insights.Shots, func(item VideoIndexShot) string { return item.ID })
	insights.Keyframes = dedupeByID(insights.Keyframes, func(item VideoIndexKeyframe) string { return item.ID })
	insights.Transcript = dedupeByID(insights.Transcript, func(item VideoIndexTranscript) string { return item.ID })
	insights.OCR = dedupeByID(insights.OCR, func(item VideoIndexOCR) string { return item.ID })
	insights.Labels = dedupeByID(insights.Labels, func(item VideoIndexLabel) string { return item.ID })
	insights.Objects = dedupeByID(insights.Objects, func(item VideoIndexObject) string { return item.ID })
}

func stableID(prefix string, parts ...string) string {
	h := sha256.New()
	h.Write([]byte(prefix))
	for _, part := range parts {
		h.Write([]byte{0})
		h.Write([]byte(part))
	}
	sum := h.Sum(nil)
	return prefix + "-" + hex.EncodeToString(sum[:12])
}

func dedupeStrings(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func dedupeByID[T any](values []T, idFn func(T) string) []T {
	if len(values) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	out := make([]T, 0, len(values))
	for _, value := range values {
		id := strings.TrimSpace(idFn(value))
		if id == "" {
			out = append(out, value)
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, value)
	}
	return out
}

func collectInsightSourceIDs(resultVideoID string, insights rawVideoIndexInsights, videos []rawVideoIndexVideo) []string {
	sourceIDs := []string{}
	for _, video := range videos {
		if id := firstNonEmpty(video.SourceID, video.ID); id != "" {
			sourceIDs = append(sourceIDs, id)
		}
	}
	return dedupeStrings(sourceIDs)
}

func hasRawInsights(insights rawVideoIndexInsights) bool {
	return len(insights.Speakers) > 0 ||
		len(insights.Scenes) > 0 ||
		len(insights.Shots) > 0 ||
		len(insights.Transcript) > 0 ||
		len(insights.OCR) > 0 ||
		len(insights.Labels) > 0 ||
		len(insights.Objects) > 0 ||
		insights.Duration != "" ||
		insights.Language != "" ||
		insights.SourceLanguage != "" ||
		len(insights.SourceLanguages) > 0
}

func mergeVideoIndexInsights(dst *VideoIndexInsights, src VideoIndexInsights) {
	dst.Speakers = append(dst.Speakers, src.Speakers...)
	dst.Scenes = append(dst.Scenes, src.Scenes...)
	dst.Shots = append(dst.Shots, src.Shots...)
	dst.Keyframes = append(dst.Keyframes, src.Keyframes...)
	dst.Transcript = append(dst.Transcript, src.Transcript...)
	dst.OCR = append(dst.OCR, src.OCR...)
	dst.Labels = append(dst.Labels, src.Labels...)
	dst.Objects = append(dst.Objects, src.Objects...)
}

func defaultSpeakerLabel(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return ""
	}
	return "Speaker " + id
}

func firstShotID(items []VideoIndexShot) string {
	if len(items) == 0 {
		return ""
	}
	return items[0].ID
}

func pickSourceID(primary string, sources []string) string {
	if id := strings.TrimSpace(primary); id != "" {
		return id
	}
	for _, source := range sources {
		if id := strings.TrimSpace(source); id != "" {
			return id
		}
	}
	return ""
}

func pow10Int64(n int) int64 {
	if n <= 0 {
		return 1
	}
	value := int64(1)
	for i := 0; i < n; i++ {
		value *= 10
	}
	return value
}
