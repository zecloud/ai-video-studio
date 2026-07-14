package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
)

const (
	editPlanSchemaVersion           = 1
	defaultEditPlannerEvidenceLimit = 64 << 10
	maxEditPlanHighlights           = 8
	maxEditPlanSuggestions          = 8
	maxEditPlanClips                = 12
	maxEditPlanTotalClipDuration    = 20 * time.Minute
	maxEditPlanClipDuration         = 10 * time.Minute
)

type EditPlan struct {
	SchemaVersion int              `json:"schemaVersion" jsonschema:"Structured edit plan schema version"`
	VideoID       string           `json:"videoId" jsonschema:"Normalized Video Indexer video id"`
	AssetID       string           `json:"assetId" jsonschema:"Stable asset id for the source video"`
	Title         string           `json:"title" jsonschema:"Plan title"`
	Summary       string           `json:"summary" jsonschema:"Plan summary"`
	Highlights    []Highlight      `json:"highlights" jsonschema:"Priority highlight candidates; use an empty array when none exist"`
	Suggestions   []EditSuggestion `json:"suggestions" jsonschema:"Edit suggestions with ordered clips; use an empty array when none exist"`
	SourceRefs    []SourceRef      `json:"sourceRefs" jsonschema:"Plan-level source refs; use an empty array when none exist"`
}

type Highlight struct {
	ID         string      `json:"id" jsonschema:"Stable highlight id"`
	Title      string      `json:"title" jsonschema:"Highlight title"`
	Reason     string      `json:"reason" jsonschema:"Why this moment matters"`
	StartMs    int64       `json:"startMs" jsonschema:"Inclusive start time in milliseconds"`
	EndMs      int64       `json:"endMs" jsonschema:"Exclusive end time in milliseconds"`
	Score      float64     `json:"score" jsonschema:"Confidence or priority score from 0 to 1"`
	SourceRefs []SourceRef `json:"sourceRefs" jsonschema:"Grounding citations for the highlight"`
}

type EditSuggestion struct {
	ID         string          `json:"id" jsonschema:"Stable suggestion id"`
	Title      string          `json:"title" jsonschema:"Suggestion title"`
	Reason     string          `json:"reason" jsonschema:"Why this edit is useful"`
	StartMs    int64           `json:"startMs" jsonschema:"Inclusive start time in milliseconds"`
	EndMs      int64           `json:"endMs" jsonschema:"Exclusive end time in milliseconds"`
	Score      float64         `json:"score" jsonschema:"Confidence or priority score from 0 to 1"`
	SourceRefs []SourceRef     `json:"sourceRefs" jsonschema:"Grounding citations for the suggestion"`
	Clips      []SuggestedClip `json:"clips" jsonschema:"Ordered clip candidates for this suggestion; use an empty array when none exist"`
}

type SuggestedClip struct {
	ID            string      `json:"id" jsonschema:"Stable clip id"`
	Title         string      `json:"title" jsonschema:"Clip title"`
	Reason        string      `json:"reason" jsonschema:"Why the clip should be kept"`
	SourceAssetID string      `json:"sourceAssetId" jsonschema:"Stable source asset id for this clip"`
	StartMs       int64       `json:"startMs" jsonschema:"Inclusive start time in milliseconds in the source asset"`
	EndMs         int64       `json:"endMs" jsonschema:"Exclusive end time in milliseconds in the source asset"`
	Score         float64     `json:"score" jsonschema:"Confidence or priority score from 0 to 1"`
	SourceRefs    []SourceRef `json:"sourceRefs" jsonschema:"Grounding citations for the clip"`
}

type SourceRef struct {
	RefID         string `json:"refId" jsonschema:"Stable source fact id"`
	SourceKind    string `json:"sourceKind" jsonschema:"Source family such as video_indexer or ffmpeg"`
	SourceAssetID string `json:"sourceAssetId" jsonschema:"Stable asset id for the source fact"`
	FactKind      string `json:"factKind" jsonschema:"Normalized fact kind; use an empty string when unavailable"`
	StartMs       int64  `json:"startMs" jsonschema:"Inclusive fact start time in milliseconds; use zero when not time-bound"`
	EndMs         int64  `json:"endMs" jsonschema:"Exclusive fact end time in milliseconds; use zero when not time-bound"`
	Text          string `json:"text" jsonschema:"Supporting text; use an empty string when unavailable"`
}

type editPlannerEvidencePacket struct {
	SchemaVersion  int                          `json:"schemaVersion" jsonschema:"Evidence packet schema version"`
	JobID          string                       `json:"jobId" jsonschema:"Job identifier"`
	VideoID        string                       `json:"videoId" jsonschema:"Normalized Video Indexer video id"`
	DurationMs     int64                        `json:"durationMs" jsonschema:"Known video duration in milliseconds"`
	SourceAssetIDs []string                     `json:"sourceAssetIds" jsonschema:"Stable source asset identifiers"`
	Signals        editPlannerSignalsEvidence   `json:"signals" jsonschema:"FFmpeg-derived signal evidence"`
	Speakers       []editPlannerSpeakerEvidence `json:"speakers,omitempty" jsonschema:"Speaker facts"`
	Scenes         []editPlannerSceneEvidence   `json:"scenes" jsonschema:"Scene-grouped evidence"`
}

type editPlannerSignalsEvidence struct {
	Duration SourceRef                    `json:"duration" jsonschema:"FFmpeg duration fact"`
	Video    *editPlannerVideoEvidence    `json:"video,omitempty" jsonschema:"Video stream facts"`
	Audio    *editPlannerAudioEvidence    `json:"audio,omitempty" jsonschema:"Audio stream facts"`
	Silence  []editPlannerSilenceEvidence `json:"silences,omitempty" jsonschema:"Detected silence windows"`
}

type editPlannerVideoEvidence struct {
	SourceRef SourceRef `json:"sourceRef" jsonschema:"FFmpeg video fact"`
	Present   bool      `json:"present" jsonschema:"Video stream present"`
	Codec     string    `json:"codec,omitempty" jsonschema:"Video codec"`
	Width     int       `json:"width,omitempty" jsonschema:"Frame width"`
	Height    int       `json:"height,omitempty" jsonschema:"Frame height"`
	FPS       float64   `json:"fps,omitempty" jsonschema:"Frame rate"`
}

type editPlannerAudioEvidence struct {
	SourceRef  SourceRef `json:"sourceRef" jsonschema:"FFmpeg audio fact"`
	Present    bool      `json:"present" jsonschema:"Audio stream present"`
	Codec      string    `json:"codec,omitempty" jsonschema:"Audio codec"`
	Channels   int       `json:"channels,omitempty" jsonschema:"Audio channel count"`
	SampleRate int       `json:"sampleRate,omitempty" jsonschema:"Audio sample rate"`
}

type editPlannerSilenceEvidence struct {
	SourceRef SourceRef `json:"sourceRef" jsonschema:"FFmpeg silence fact"`
	StartMs   int64     `json:"startMs" jsonschema:"Inclusive silence start in milliseconds"`
	EndMs     int64     `json:"endMs" jsonschema:"Exclusive silence end in milliseconds"`
}

type editPlannerSpeakerEvidence struct {
	SourceRef     SourceRef `json:"sourceRef" jsonschema:"Video Indexer speaker fact"`
	SpeakerID     string    `json:"speakerId" jsonschema:"Stable speaker identifier"`
	Name          string    `json:"name,omitempty" jsonschema:"Speaker display name"`
	Language      string    `json:"language,omitempty" jsonschema:"Speaker language"`
	TranscriptIDs []string  `json:"transcriptIds,omitempty" jsonschema:"Transcript facts linked to this speaker"`
}

type editPlannerSceneEvidence struct {
	SourceRef  SourceRef                       `json:"sourceRef" jsonschema:"Video Indexer scene fact"`
	SceneID    string                          `json:"sceneId" jsonschema:"Stable scene identifier"`
	StartMs    int64                           `json:"startMs" jsonschema:"Inclusive scene start in milliseconds"`
	EndMs      int64                           `json:"endMs" jsonschema:"Exclusive scene end in milliseconds"`
	Confidence float64                         `json:"confidence,omitempty" jsonschema:"Scene confidence"`
	Shots      []editPlannerShotEvidence       `json:"shots,omitempty" jsonschema:"Shot facts in this scene"`
	Transcript []editPlannerTranscriptEvidence `json:"transcript,omitempty" jsonschema:"Transcript facts in this scene"`
	OCR        []editPlannerTextEvidence       `json:"ocr,omitempty" jsonschema:"OCR facts in this scene"`
	Labels     []editPlannerLabelEvidence      `json:"labels,omitempty" jsonschema:"Label facts in this scene"`
	Objects    []editPlannerObjectEvidence     `json:"objects,omitempty" jsonschema:"Object facts in this scene"`
}

type editPlannerShotEvidence struct {
	SourceRef   SourceRef `json:"sourceRef" jsonschema:"Video Indexer shot fact"`
	ShotID      string    `json:"shotId" jsonschema:"Stable shot identifier"`
	StartMs     int64     `json:"startMs" jsonschema:"Inclusive shot start in milliseconds"`
	EndMs       int64     `json:"endMs" jsonschema:"Exclusive shot end in milliseconds"`
	Confidence  float64   `json:"confidence,omitempty" jsonschema:"Shot confidence"`
	Tags        []string  `json:"tags,omitempty" jsonschema:"Shot tags"`
	KeyframeIDs []string  `json:"keyframeIds,omitempty" jsonschema:"Keyframes grouped under the shot"`
}

type editPlannerTranscriptEvidence struct {
	SourceRef    SourceRef `json:"sourceRef" jsonschema:"Transcript fact"`
	TranscriptID string    `json:"transcriptId" jsonschema:"Stable transcript identifier"`
	SpeakerID    string    `json:"speakerId,omitempty" jsonschema:"Speaker identifier if known"`
	SpeakerName  string    `json:"speakerName,omitempty" jsonschema:"Speaker name if known"`
	Language     string    `json:"language,omitempty" jsonschema:"Transcript language"`
	StartMs      int64     `json:"startMs" jsonschema:"Inclusive transcript start in milliseconds"`
	EndMs        int64     `json:"endMs" jsonschema:"Exclusive transcript end in milliseconds"`
	Text         string    `json:"text" jsonschema:"Transcript text"`
	Confidence   float64   `json:"confidence,omitempty" jsonschema:"Transcript confidence"`
}

type editPlannerTextEvidence struct {
	SourceRef  SourceRef `json:"sourceRef" jsonschema:"Text fact"`
	TextID     string    `json:"textId" jsonschema:"Stable text identifier"`
	Language   string    `json:"language,omitempty" jsonschema:"Text language"`
	StartMs    int64     `json:"startMs" jsonschema:"Inclusive text start in milliseconds"`
	EndMs      int64     `json:"endMs" jsonschema:"Exclusive text end in milliseconds"`
	Text       string    `json:"text" jsonschema:"Detected text"`
	Confidence float64   `json:"confidence,omitempty" jsonschema:"Text confidence"`
}

type editPlannerLabelEvidence struct {
	SourceRef   SourceRef `json:"sourceRef" jsonschema:"Label fact"`
	LabelID     string    `json:"labelId" jsonschema:"Stable label identifier"`
	Language    string    `json:"language,omitempty" jsonschema:"Label language"`
	Name        string    `json:"name" jsonschema:"Label name"`
	ReferenceID string    `json:"referenceId,omitempty" jsonschema:"Label reference id"`
	StartMs     int64     `json:"startMs" jsonschema:"Inclusive label start in milliseconds"`
	EndMs       int64     `json:"endMs" jsonschema:"Exclusive label end in milliseconds"`
	Confidence  float64   `json:"confidence,omitempty" jsonschema:"Label confidence"`
}

type editPlannerObjectEvidence struct {
	SourceRef   SourceRef `json:"sourceRef" jsonschema:"Detected object fact"`
	ObjectID    string    `json:"objectId" jsonschema:"Stable object identifier"`
	Type        string    `json:"type" jsonschema:"Object type"`
	DisplayName string    `json:"displayName,omitempty" jsonschema:"Object display name"`
	WikiDataID  string    `json:"wikiDataId,omitempty" jsonschema:"Wikidata identifier"`
	StartMs     int64     `json:"startMs" jsonschema:"Inclusive object start in milliseconds"`
	EndMs       int64     `json:"endMs" jsonschema:"Exclusive object end in milliseconds"`
	Confidence  float64   `json:"confidence,omitempty" jsonschema:"Object confidence"`
}

type editPlannerEvidenceIndex struct {
	DurationMs int64
	assetIDs   map[string]struct{}
	refs       map[string]sourceRefEvidence
}

type sourceRefEvidence struct {
	AssetID string
	Kind    string
	StartMs int64
	EndMs   int64
}

func newEditPlannerEvidenceIndex(durationMs int64) editPlannerEvidenceIndex {
	return editPlannerEvidenceIndex{
		DurationMs: durationMs,
		assetIDs:   map[string]struct{}{},
		refs:       map[string]sourceRefEvidence{},
	}
}

func (i *editPlannerEvidenceIndex) register(ref SourceRef) error {
	if i == nil {
		return errors.New("evidence index is nil")
	}
	if strings.TrimSpace(ref.RefID) == "" {
		return errors.New("source ref id is required")
	}
	if strings.TrimSpace(ref.SourceAssetID) == "" {
		return fmt.Errorf("source ref %s is missing a source asset id", ref.RefID)
	}
	if existing, ok := i.refs[ref.RefID]; ok {
		if existing.AssetID != ref.SourceAssetID || existing.Kind != ref.SourceKind || existing.StartMs != ref.StartMs || existing.EndMs != ref.EndMs {
			return fmt.Errorf("source ref %s is defined more than once with different metadata", ref.RefID)
		}
		return nil
	}
	i.refs[ref.RefID] = sourceRefEvidence{
		AssetID: ref.SourceAssetID,
		Kind:    ref.SourceKind,
		StartMs: ref.StartMs,
		EndMs:   ref.EndMs,
	}
	i.assetIDs[ref.SourceAssetID] = struct{}{}
	return nil
}

func (i *editPlannerEvidenceIndex) knownRef(refID string) (sourceRefEvidence, bool) {
	if i == nil {
		return sourceRefEvidence{}, false
	}
	ref, ok := i.refs[refID]
	return ref, ok
}

type editPlannerEvidenceTooLargeError struct {
	Size  int
	Limit int
}

func (e *editPlannerEvidenceTooLargeError) Error() string {
	return fmt.Sprintf("edit planner evidence packet is %d bytes, exceeds limit %d", e.Size, e.Limit)
}

type editPlannerValidationError struct {
	Problems []string
}

func (e *editPlannerValidationError) Error() string {
	return "edit plan validation failed: " + strings.Join(e.Problems, "; ")
}

func newEditPlannerValidationError(format string, args ...any) error {
	return &editPlannerValidationError{Problems: []string{fmt.Sprintf(format, args...)}}
}

func appendEditPlannerError(err error, format string, args ...any) error {
	msg := fmt.Sprintf(format, args...)
	if err == nil {
		return &editPlannerValidationError{Problems: []string{msg}}
	}
	if v, ok := err.(*editPlannerValidationError); ok {
		v.Problems = append(v.Problems, msg)
		return v
	}
	return &editPlannerValidationError{Problems: []string{err.Error(), msg}}
}

type editPlannerPacketBuilder struct {
	jobID      string
	videoID    string
	assetID    string
	durationMs int64
	index      editPlannerEvidenceIndex
	signals    editPlannerSignalsEvidence
	speakers   map[string]*editPlannerSpeakerEvidence
	scenes     []*editPlannerSceneEvidence
}

func buildEditPlannerPrompt(ctx context.Context, job JobDocument, asset StagedAsset, result VideoIndexResult, limitBytes int, obs *Observability) (string, editPlannerEvidenceIndex, error) {
	if limitBytes <= 0 {
		limitBytes = defaultEditPlannerEvidenceLimit
	}
	builder, err := newEditPlannerPacketBuilder(job, asset, result)
	if err != nil {
		return "", editPlannerEvidenceIndex{}, err
	}
	packet, err := builder.packet()
	if err != nil {
		return "", editPlannerEvidenceIndex{}, err
	}
	raw, err := json.Marshal(packet)
	if err != nil {
		return "", editPlannerEvidenceIndex{}, err
	}
	if len(raw) > limitBytes {
		return "", editPlannerEvidenceIndex{}, &editPlannerEvidenceTooLargeError{Size: len(raw), Limit: limitBytes}
	}
	if obs != nil {
		obs.RecordEvidencePacket(ctx, len(raw), len(packet.Scenes), attribute.String("video_id", packet.VideoID), attribute.String("prompt_version", editPlannerInstructionsVersion))
	}
	return string(raw), builder.index, nil
}

func newEditPlannerPacketBuilder(job JobDocument, asset StagedAsset, result VideoIndexResult) (*editPlannerPacketBuilder, error) {
	videoID := firstNonEmpty(result.VideoID, job.VideoIndexerVideoID)
	if videoID == "" {
		return nil, errors.New("video id is required")
	}
	assetID := strings.TrimSpace(job.OneDriveItemID)
	if assetID == "" {
		return nil, errors.New("oneDriveItemId is required")
	}
	signals := result.TechnicalSignals
	durationMs := result.DurationMs
	if durationMs <= 0 && signals != nil && signals.Duration > 0 {
		durationMs = signals.Duration.Milliseconds()
	}
	if durationMs <= 0 {
		durationMs = maxEvidenceDuration(result, signals)
	}
	if durationMs <= 0 {
		return nil, errors.New("video duration is required")
	}
	b := &editPlannerPacketBuilder{
		jobID:      job.ID,
		videoID:    videoID,
		assetID:    assetID,
		durationMs: durationMs,
		index:      newEditPlannerEvidenceIndex(durationMs),
		speakers:   map[string]*editPlannerSpeakerEvidence{},
	}
	if signals == nil {
		return nil, errors.New("technical media signals are required")
	}
	sceneRows := result.Insights.Scenes
	if len(sceneRows) == 0 {
		return nil, errors.New("at least one scene is required for edit planning")
	}
	sort.SliceStable(sceneRows, func(i, j int) bool {
		if sceneRows[i].StartMs == sceneRows[j].StartMs {
			if sceneRows[i].EndMs == sceneRows[j].EndMs {
				return sceneRows[i].ID < sceneRows[j].ID
			}
			return sceneRows[i].EndMs < sceneRows[j].EndMs
		}
		return sceneRows[i].StartMs < sceneRows[j].StartMs
	})
	sceneBuilders := make([]*editPlannerSceneEvidence, 0, len(sceneRows))
	for _, scene := range sceneRows {
		if scene.EndMs < scene.StartMs {
			return nil, fmt.Errorf("scene %s end is before start", scene.ID)
		}
		sceneRef := SourceRef{
			RefID:         editPlannerSceneRefID(scene.ID),
			SourceKind:    "video_indexer",
			SourceAssetID: b.assetID,
			FactKind:      "scene",
			StartMs:       scene.StartMs,
			EndMs:         scene.EndMs,
		}
		if err := b.index.register(sceneRef); err != nil {
			return nil, err
		}
		sceneBuilders = append(sceneBuilders, &editPlannerSceneEvidence{
			SourceRef:  sceneRef,
			SceneID:    scene.ID,
			StartMs:    scene.StartMs,
			EndMs:      scene.EndMs,
			Confidence: scene.Confidence,
		})
	}
	if err := b.populateSignals(result, signals); err != nil {
		return nil, err
	}
	if err := b.populateSpeakers(result, sceneBuilders); err != nil {
		return nil, err
	}
	if err := b.populateShots(result, sceneBuilders); err != nil {
		return nil, err
	}
	if err := b.populateTranscript(result, sceneBuilders); err != nil {
		return nil, err
	}
	if err := b.populateTextFacts(result.Insights.OCR, "ocr", sceneBuilders); err != nil {
		return nil, err
	}
	if err := b.populateLabelFacts(result.Insights.Labels, sceneBuilders); err != nil {
		return nil, err
	}
	if err := b.populateObjectFacts(result.Insights.Objects, sceneBuilders); err != nil {
		return nil, err
	}
	for _, scene := range sceneBuilders {
		sortSceneEvidence(scene)
	}
	b.scenes = sceneBuilders
	return b, nil
}

func (b *editPlannerPacketBuilder) packet() (editPlannerEvidencePacket, error) {
	if b == nil {
		return editPlannerEvidencePacket{}, errors.New("packet builder is nil")
	}
	if len(b.scenes) == 0 {
		return editPlannerEvidencePacket{}, errors.New("evidence packet is missing scene groups")
	}
	speakers := make([]editPlannerSpeakerEvidence, 0, len(b.speakers))
	for _, speaker := range b.speakers {
		if speaker == nil {
			continue
		}
		speakers = append(speakers, *speaker)
	}
	sort.SliceStable(speakers, func(i, j int) bool {
		return speakers[i].SpeakerID < speakers[j].SpeakerID
	})
	sort.SliceStable(b.scenes, func(i, j int) bool {
		if b.scenes[i].StartMs == b.scenes[j].StartMs {
			if b.scenes[i].EndMs == b.scenes[j].EndMs {
				return b.scenes[i].SceneID < b.scenes[j].SceneID
			}
			return b.scenes[i].EndMs < b.scenes[j].EndMs
		}
		return b.scenes[i].StartMs < b.scenes[j].StartMs
	})
	packet := editPlannerEvidencePacket{
		SchemaVersion:  1,
		JobID:          b.jobID,
		VideoID:        b.videoID,
		DurationMs:     b.durationMs,
		SourceAssetIDs: sortedKeys(b.index.assetIDs),
		Signals:        b.signals,
		Speakers:       speakers,
		Scenes:         make([]editPlannerSceneEvidence, 0, len(b.scenes)),
	}
	for _, scene := range b.scenes {
		if scene == nil {
			continue
		}
		packet.Scenes = append(packet.Scenes, *scene)
	}
	return packet, nil
}

func (b *editPlannerPacketBuilder) populateSignals(result VideoIndexResult, signals *MediaSignals) error {
	if signals == nil {
		return errors.New("technical media signals are required")
	}
	durationRef := SourceRef{
		RefID:         editPlannerFFmpegRefID("duration"),
		SourceKind:    "ffmpeg",
		SourceAssetID: b.assetID,
		FactKind:      "duration",
	}
	if err := b.index.register(durationRef); err != nil {
		return err
	}
	packetSignals := editPlannerSignalsEvidence{
		Duration: durationRef,
		Video: &editPlannerVideoEvidence{
			SourceRef: SourceRef{
				RefID:         editPlannerFFmpegRefID("video"),
				SourceKind:    "ffmpeg",
				SourceAssetID: b.assetID,
				FactKind:      "video",
			},
			Present: signals.Video.Present,
			Codec:   signals.Video.Codec,
			Width:   signals.Video.Width,
			Height:  signals.Video.Height,
			FPS:     signals.Video.FPS,
		},
	}
	if err := b.index.register(packetSignals.Video.SourceRef); err != nil {
		return err
	}
	packetSignals.Audio = &editPlannerAudioEvidence{
		SourceRef: SourceRef{
			RefID:         editPlannerFFmpegRefID("audio"),
			SourceKind:    "ffmpeg",
			SourceAssetID: b.assetID,
			FactKind:      "audio",
		},
		Present:    signals.Audio.Present,
		Codec:      signals.Audio.Codec,
		Channels:   signals.Audio.Channels,
		SampleRate: signals.Audio.SampleRate,
	}
	if err := b.index.register(packetSignals.Audio.SourceRef); err != nil {
		return err
	}
	for i, silence := range signals.Silences {
		ref := SourceRef{
			RefID:         editPlannerFFmpegRefID("silence", strconv.Itoa(i)),
			SourceKind:    "ffmpeg",
			SourceAssetID: b.assetID,
			FactKind:      "silence",
			StartMs:       silence.Start.Milliseconds(),
			EndMs:         silence.End.Milliseconds(),
		}
		if err := b.index.register(ref); err != nil {
			return err
		}
		packetSignals.Silence = append(packetSignals.Silence, editPlannerSilenceEvidence{
			SourceRef: ref,
			StartMs:   silence.Start.Milliseconds(),
			EndMs:     silence.End.Milliseconds(),
		})
	}
	sort.SliceStable(packetSignals.Silence, func(i, j int) bool {
		if packetSignals.Silence[i].StartMs == packetSignals.Silence[j].StartMs {
			return packetSignals.Silence[i].EndMs < packetSignals.Silence[j].EndMs
		}
		return packetSignals.Silence[i].StartMs < packetSignals.Silence[j].StartMs
	})
	b.signals = packetSignals
	return nil
}

func (b *editPlannerPacketBuilder) populateSpeakers(result VideoIndexResult, scenes []*editPlannerSceneEvidence) error {
	for _, speaker := range result.Insights.Speakers {
		if strings.TrimSpace(speaker.ID) == "" {
			continue
		}
		ref := SourceRef{
			RefID:         editPlannerSpeakerRefID(speaker.ID),
			SourceKind:    "video_indexer",
			SourceAssetID: b.assetID,
			FactKind:      "speaker",
		}
		if err := b.index.register(ref); err != nil {
			return err
		}
		transcriptIDs := append([]string(nil), speaker.TranscriptIDs...)
		sort.Strings(transcriptIDs)
		b.speakers[speaker.ID] = &editPlannerSpeakerEvidence{
			SourceRef:     ref,
			SpeakerID:     speaker.ID,
			Name:          speaker.Name,
			TranscriptIDs: dedupeStrings(transcriptIDs),
		}
	}
	return nil
}

func (b *editPlannerPacketBuilder) populateShots(result VideoIndexResult, scenes []*editPlannerSceneEvidence) error {
	for _, shot := range result.Insights.Shots {
		scene := sceneForRange(scenes, shot.StartMs, shot.EndMs)
		if scene == nil {
			return fmt.Errorf("shot %s could not be assigned to a scene", shot.ID)
		}
		ref := SourceRef{
			RefID:         editPlannerShotRefID(shot.ID),
			SourceKind:    "video_indexer",
			SourceAssetID: b.assetID,
			FactKind:      "shot",
			StartMs:       shot.StartMs,
			EndMs:         shot.EndMs,
		}
		if err := b.index.register(ref); err != nil {
			return err
		}
		scene.Shots = append(scene.Shots, editPlannerShotEvidence{
			SourceRef:   ref,
			ShotID:      shot.ID,
			StartMs:     shot.StartMs,
			EndMs:       shot.EndMs,
			Confidence:  shot.Confidence,
			Tags:        dedupeStrings(append([]string(nil), shot.Tags...)),
			KeyframeIDs: dedupeStrings(append([]string(nil), shot.KeyframeIDs...)),
		})
	}
	return nil
}

func (b *editPlannerPacketBuilder) populateTranscript(result VideoIndexResult, scenes []*editPlannerSceneEvidence) error {
	seen := map[string]struct{}{}
	for _, entry := range result.Insights.Transcript {
		scene := sceneForRange(scenes, entry.StartMs, entry.EndMs)
		if scene == nil {
			return fmt.Errorf("transcript %s could not be assigned to a scene", entry.ID)
		}
		key := strings.ToLower(strings.TrimSpace(entry.Text))
		if key == "" {
			continue
		}
		if _, ok := seen["transcript|"+key+"|"+scene.SceneID]; ok {
			continue
		}
		seen["transcript|"+key+"|"+scene.SceneID] = struct{}{}
		ref := SourceRef{
			RefID:         editPlannerTranscriptRefID(entry.ID),
			SourceKind:    "video_indexer",
			SourceAssetID: b.assetID,
			FactKind:      "transcript",
			StartMs:       entry.StartMs,
			EndMs:         entry.EndMs,
			Text:          entry.Text,
		}
		if err := b.index.register(ref); err != nil {
			return err
		}
		scene.Transcript = append(scene.Transcript, editPlannerTranscriptEvidence{
			SourceRef:    ref,
			TranscriptID: entry.ID,
			SpeakerID:    entry.SpeakerID,
			SpeakerName:  entry.SpeakerName,
			Language:     entry.Language,
			StartMs:      entry.StartMs,
			EndMs:        entry.EndMs,
			Text:         strings.TrimSpace(entry.Text),
			Confidence:   entry.Confidence,
		})
		if speaker := b.speakers[entry.SpeakerID]; speaker != nil {
			speaker.TranscriptIDs = dedupeStrings(append(speaker.TranscriptIDs, entry.ID))
			sort.Strings(speaker.TranscriptIDs)
		}
	}
	return nil
}

func (b *editPlannerPacketBuilder) populateTextFacts(entries []VideoIndexOCR, kind string, scenes []*editPlannerSceneEvidence) error {
	seen := map[string]struct{}{}
	for _, entry := range entries {
		scene := sceneForRange(scenes, entry.StartMs, entry.EndMs)
		if scene == nil {
			return fmt.Errorf("%s %s could not be assigned to a scene", kind, entry.ID)
		}
		text := strings.TrimSpace(entry.Text)
		if text == "" {
			continue
		}
		key := kind + "|" + strings.ToLower(text) + "|" + scene.SceneID
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		ref := SourceRef{
			RefID:         editPlannerTextRefID(kind, entry.ID),
			SourceKind:    "video_indexer",
			SourceAssetID: b.assetID,
			FactKind:      kind,
			StartMs:       entry.StartMs,
			EndMs:         entry.EndMs,
			Text:          text,
		}
		if err := b.index.register(ref); err != nil {
			return err
		}
		scene.OCR = append(scene.OCR, editPlannerTextEvidence{
			SourceRef:  ref,
			TextID:     entry.ID,
			Language:   entry.Language,
			StartMs:    entry.StartMs,
			EndMs:      entry.EndMs,
			Text:       text,
			Confidence: entry.Confidence,
		})
	}
	return nil
}

func (b *editPlannerPacketBuilder) populateLabelFacts(entries []VideoIndexLabel, scenes []*editPlannerSceneEvidence) error {
	seen := map[string]struct{}{}
	for _, entry := range entries {
		scene := sceneForRange(scenes, entry.StartMs, entry.EndMs)
		if scene == nil {
			return fmt.Errorf("label %s could not be assigned to a scene", entry.ID)
		}
		key := strings.ToLower(strings.TrimSpace(entry.Name)) + "|" + scene.SceneID
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		ref := SourceRef{
			RefID:         editPlannerLabelRefID(entry.ID),
			SourceKind:    "video_indexer",
			SourceAssetID: b.assetID,
			FactKind:      "label",
			StartMs:       entry.StartMs,
			EndMs:         entry.EndMs,
			Text:          entry.Name,
		}
		if err := b.index.register(ref); err != nil {
			return err
		}
		scene.Labels = append(scene.Labels, editPlannerLabelEvidence{
			SourceRef:   ref,
			LabelID:     entry.ID,
			Language:    entry.Language,
			Name:        strings.TrimSpace(entry.Name),
			ReferenceID: entry.ReferenceID,
			StartMs:     entry.StartMs,
			EndMs:       entry.EndMs,
			Confidence:  entry.Confidence,
		})
	}
	return nil
}

func (b *editPlannerPacketBuilder) populateObjectFacts(entries []VideoIndexObject, scenes []*editPlannerSceneEvidence) error {
	seen := map[string]struct{}{}
	for _, entry := range entries {
		scene := sceneForRange(scenes, entry.StartMs, entry.EndMs)
		if scene == nil {
			return fmt.Errorf("object %s could not be assigned to a scene", entry.ID)
		}
		key := strings.ToLower(strings.TrimSpace(entry.DisplayName)) + "|" + strings.ToLower(strings.TrimSpace(entry.Type)) + "|" + scene.SceneID
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		ref := SourceRef{
			RefID:         editPlannerObjectRefID(entry.ID),
			SourceKind:    "video_indexer",
			SourceAssetID: b.assetID,
			FactKind:      "object",
			StartMs:       entry.StartMs,
			EndMs:         entry.EndMs,
			Text:          entry.DisplayName,
		}
		if err := b.index.register(ref); err != nil {
			return err
		}
		scene.Objects = append(scene.Objects, editPlannerObjectEvidence{
			SourceRef:   ref,
			ObjectID:    entry.ID,
			Type:        strings.TrimSpace(entry.Type),
			DisplayName: strings.TrimSpace(entry.DisplayName),
			WikiDataID:  entry.WikiDataID,
			StartMs:     entry.StartMs,
			EndMs:       entry.EndMs,
			Confidence:  entry.Confidence,
		})
	}
	return nil
}

func sortSceneEvidence(scene *editPlannerSceneEvidence) {
	if scene == nil {
		return
	}
	sort.SliceStable(scene.Shots, func(i, j int) bool {
		if scene.Shots[i].StartMs == scene.Shots[j].StartMs {
			if scene.Shots[i].EndMs == scene.Shots[j].EndMs {
				return scene.Shots[i].ShotID < scene.Shots[j].ShotID
			}
			return scene.Shots[i].EndMs < scene.Shots[j].EndMs
		}
		return scene.Shots[i].StartMs < scene.Shots[j].StartMs
	})
	sort.SliceStable(scene.Transcript, func(i, j int) bool {
		if scene.Transcript[i].StartMs == scene.Transcript[j].StartMs {
			if scene.Transcript[i].EndMs == scene.Transcript[j].EndMs {
				return scene.Transcript[i].TranscriptID < scene.Transcript[j].TranscriptID
			}
			return scene.Transcript[i].EndMs < scene.Transcript[j].EndMs
		}
		return scene.Transcript[i].StartMs < scene.Transcript[j].StartMs
	})
	sort.SliceStable(scene.OCR, func(i, j int) bool {
		if scene.OCR[i].StartMs == scene.OCR[j].StartMs {
			if scene.OCR[i].EndMs == scene.OCR[j].EndMs {
				return scene.OCR[i].TextID < scene.OCR[j].TextID
			}
			return scene.OCR[i].EndMs < scene.OCR[j].EndMs
		}
		return scene.OCR[i].StartMs < scene.OCR[j].StartMs
	})
	sort.SliceStable(scene.Labels, func(i, j int) bool {
		if scene.Labels[i].StartMs == scene.Labels[j].StartMs {
			if scene.Labels[i].EndMs == scene.Labels[j].EndMs {
				return scene.Labels[i].LabelID < scene.Labels[j].LabelID
			}
			return scene.Labels[i].EndMs < scene.Labels[j].EndMs
		}
		return scene.Labels[i].StartMs < scene.Labels[j].StartMs
	})
	sort.SliceStable(scene.Objects, func(i, j int) bool {
		if scene.Objects[i].StartMs == scene.Objects[j].StartMs {
			if scene.Objects[i].EndMs == scene.Objects[j].EndMs {
				return scene.Objects[i].ObjectID < scene.Objects[j].ObjectID
			}
			return scene.Objects[i].EndMs < scene.Objects[j].EndMs
		}
		return scene.Objects[i].StartMs < scene.Objects[j].StartMs
	})
}

func sceneForRange(scenes []*editPlannerSceneEvidence, startMs, endMs int64) *editPlannerSceneEvidence {
	if len(scenes) == 0 {
		return nil
	}
	if endMs < startMs {
		startMs, endMs = endMs, startMs
	}
	if endMs == startMs {
		endMs = startMs + 1
	}
	midpoint := startMs + (endMs-startMs)/2
	for _, scene := range scenes {
		if scene == nil {
			continue
		}
		if midpoint >= scene.StartMs && midpoint < scene.EndMs {
			return scene
		}
	}
	for _, scene := range scenes {
		if scene == nil {
			continue
		}
		if startMs < scene.EndMs && endMs > scene.StartMs {
			return scene
		}
	}
	var best *editPlannerSceneEvidence
	var bestDistance int64
	for _, scene := range scenes {
		if scene == nil {
			continue
		}
		distance := abs64(midpoint - scene.StartMs)
		if best == nil || distance < bestDistance {
			best = scene
			bestDistance = distance
		}
	}
	return best
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

func maxEvidenceDuration(result VideoIndexResult, signals *MediaSignals) int64 {
	var maxMs int64
	walk := func(start, end int64) {
		if end > maxMs {
			maxMs = end
		}
		if start > maxMs {
			maxMs = start
		}
	}
	for _, scene := range result.Insights.Scenes {
		walk(scene.StartMs, scene.EndMs)
	}
	for _, shot := range result.Insights.Shots {
		walk(shot.StartMs, shot.EndMs)
	}
	for _, entry := range result.Insights.Transcript {
		walk(entry.StartMs, entry.EndMs)
	}
	for _, entry := range result.Insights.OCR {
		walk(entry.StartMs, entry.EndMs)
	}
	for _, entry := range result.Insights.Labels {
		walk(entry.StartMs, entry.EndMs)
	}
	for _, entry := range result.Insights.Objects {
		walk(entry.StartMs, entry.EndMs)
	}
	if signals != nil {
		if d := signals.Duration.Milliseconds(); d > maxMs {
			maxMs = d
		}
		for _, silence := range signals.Silences {
			walk(silence.Start.Milliseconds(), silence.End.Milliseconds())
		}
	}
	return maxMs
}

func sortedKeys(values map[string]struct{}) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func editPlannerSceneRefID(sceneID string) string {
	return "vi:scene:" + strings.TrimSpace(sceneID)
}

func editPlannerShotRefID(shotID string) string {
	return "vi:shot:" + strings.TrimSpace(shotID)
}

func editPlannerTranscriptRefID(transcriptID string) string {
	return "vi:transcript:" + strings.TrimSpace(transcriptID)
}

func editPlannerTextRefID(kind, id string) string {
	return "vi:" + strings.TrimSpace(kind) + ":" + strings.TrimSpace(id)
}

func editPlannerLabelRefID(labelID string) string {
	return "vi:label:" + strings.TrimSpace(labelID)
}

func editPlannerObjectRefID(objectID string) string {
	return "vi:object:" + strings.TrimSpace(objectID)
}

func editPlannerSpeakerRefID(speakerID string) string {
	return "vi:speaker:" + strings.TrimSpace(speakerID)
}

func editPlannerFFmpegRefID(parts ...string) string {
	return "ffmpeg:" + strings.Join(cleanParts(parts), ":")
}

func cleanParts(parts []string) []string {
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		out = append(out, part)
	}
	return out
}

func validateEditPlan(plan EditPlan, index editPlannerEvidenceIndex) (EditPlan, error) {
	var problems []string
	if plan.SchemaVersion != editPlanSchemaVersion {
		problems = append(problems, fmt.Sprintf("schemaVersion must be %d", editPlanSchemaVersion))
	}
	plan.Title = strings.TrimSpace(plan.Title)
	plan.Summary = strings.TrimSpace(plan.Summary)
	plan.VideoID = strings.TrimSpace(plan.VideoID)
	plan.AssetID = strings.TrimSpace(plan.AssetID)
	if plan.VideoID == "" {
		problems = append(problems, "videoId is required")
	}
	if plan.AssetID == "" {
		problems = append(problems, "assetId is required")
	}
	sourceAssetIDs := sortedKeys(index.assetIDs)
	switch len(sourceAssetIDs) {
	case 0:
		problems = append(problems, "source asset ids are required")
	case 1:
		if plan.AssetID != sourceAssetIDs[0] {
			problems = append(problems, fmt.Sprintf("assetId must match %q", sourceAssetIDs[0]))
		}
	default:
		problems = append(problems, "multiple source asset ids are not supported")
	}
	if plan.Title == "" {
		problems = append(problems, "title is required")
	}
	if plan.Summary == "" {
		problems = append(problems, "summary is required")
	}
	if len(plan.SourceRefs) > 0 {
		plan.SourceRefs = normalizeAndValidateSourceRefs(plan.SourceRefs, index, &problems, "plan.sourceRefs")
	} else {
		plan.SourceRefs = nil
	}
	plan.Highlights = normalizeHighlights(plan.Highlights)
	plan.Suggestions = normalizeSuggestions(plan.Suggestions)
	if len(plan.Highlights) > maxEditPlanHighlights {
		problems = append(problems, fmt.Sprintf("highlights must not exceed %d items", maxEditPlanHighlights))
	}
	if len(plan.Suggestions) > maxEditPlanSuggestions {
		problems = append(problems, fmt.Sprintf("suggestions must not exceed %d items", maxEditPlanSuggestions))
	}
	var totalClips int
	for hi, highlight := range plan.Highlights {
		if err := validateHighlight(&highlight, index, hi); err != nil {
			problems = append(problems, err.Error())
		}
		plan.Highlights[hi] = highlight
	}
	for si, suggestion := range plan.Suggestions {
		if err := validateSuggestion(&suggestion, index, si, plan); err != nil {
			problems = append(problems, err.Error())
		}
		totalClips += len(suggestion.Clips)
		limit := min64(index.DurationMs, int64(maxEditPlanTotalClipDuration/time.Millisecond))
		if duration := mergedClipDuration(suggestion.Clips); duration > limit {
			problems = append(problems, fmt.Sprintf("suggestions[%d] total clip duration %dms exceeds limit %dms", si, duration, limit))
		}
		plan.Suggestions[si] = suggestion
	}
	if totalClips > maxEditPlanClips {
		problems = append(problems, fmt.Sprintf("clips must not exceed %d items", maxEditPlanClips))
	}
	if len(problems) > 0 {
		return EditPlan{}, &editPlannerValidationError{Problems: problems}
	}
	return plan, nil
}

func mergedClipDuration(clips []SuggestedClip) int64 {
	if len(clips) == 0 {
		return 0
	}
	byAsset := make(map[string][]SuggestedClip)
	for _, clip := range clips {
		byAsset[strings.TrimSpace(clip.SourceAssetID)] = append(byAsset[strings.TrimSpace(clip.SourceAssetID)], clip)
	}
	var total int64
	for _, intervals := range byAsset {
		total += mergedClipDurationForAsset(intervals)
	}
	return total
}

func mergedClipDurationForAsset(clips []SuggestedClip) int64 {
	intervals := append([]SuggestedClip(nil), clips...)
	sort.Slice(intervals, func(i, j int) bool {
		if intervals[i].StartMs == intervals[j].StartMs {
			return intervals[i].EndMs < intervals[j].EndMs
		}
		return intervals[i].StartMs < intervals[j].StartMs
	})
	var total int64
	start, end := intervals[0].StartMs, intervals[0].EndMs
	for _, clip := range intervals[1:] {
		if clip.StartMs <= end {
			if clip.EndMs > end {
				end = clip.EndMs
			}
			continue
		}
		total += end - start
		start, end = clip.StartMs, clip.EndMs
	}
	return total + end - start
}

func validateHighlight(highlight *Highlight, index editPlannerEvidenceIndex, idx int) error {
	var err error
	var refProblems []string
	highlight.ID = strings.TrimSpace(highlight.ID)
	highlight.Title = strings.TrimSpace(highlight.Title)
	highlight.Reason = strings.TrimSpace(highlight.Reason)
	highlight.SourceRefs = normalizeAndValidateSourceRefs(highlight.SourceRefs, index, &refProblems, fmt.Sprintf("highlights[%d].sourceRefs", idx))
	if highlight.ID == "" {
		err = appendEditPlannerError(err, "highlights[%d].id is required", idx)
	}
	if highlight.Title == "" {
		err = appendEditPlannerError(err, "highlights[%d].title is required", idx)
	}
	if highlight.Reason == "" {
		err = appendEditPlannerError(err, "highlights[%d].reason is required", idx)
	}
	if scoreErr := validateScore(highlight.Score); scoreErr != nil {
		err = appendEditPlannerError(err, "highlights[%d].score %v", idx, scoreErr)
	}
	if timeErr := validateTimeRange(highlight.StartMs, highlight.EndMs, index.DurationMs, fmt.Sprintf("highlights[%d]", idx)); timeErr != nil {
		err = appendEditPlannerError(err, "%s", timeErr.Error())
	}
	if len(refProblems) > 0 {
		for _, problem := range refProblems {
			err = appendEditPlannerError(err, "%s", problem)
		}
	}
	if len(highlight.SourceRefs) == 0 {
		err = appendEditPlannerError(err, "highlights[%d].sourceRefs is required", idx)
	}
	return err
}

func validateSuggestion(suggestion *EditSuggestion, index editPlannerEvidenceIndex, idx int, plan EditPlan) error {
	var err error
	var refProblems []string
	suggestion.ID = strings.TrimSpace(suggestion.ID)
	suggestion.Title = strings.TrimSpace(suggestion.Title)
	suggestion.Reason = strings.TrimSpace(suggestion.Reason)
	suggestion.SourceRefs = normalizeAndValidateSourceRefs(suggestion.SourceRefs, index, &refProblems, fmt.Sprintf("suggestions[%d].sourceRefs", idx))
	if suggestion.ID == "" {
		err = appendEditPlannerError(err, "suggestions[%d].id is required", idx)
	}
	if suggestion.Title == "" {
		err = appendEditPlannerError(err, "suggestions[%d].title is required", idx)
	}
	if suggestion.Reason == "" {
		err = appendEditPlannerError(err, "suggestions[%d].reason is required", idx)
	}
	if scoreErr := validateScore(suggestion.Score); scoreErr != nil {
		err = appendEditPlannerError(err, "suggestions[%d].score %v", idx, scoreErr)
	}
	if timeErr := validateTimeRange(suggestion.StartMs, suggestion.EndMs, index.DurationMs, fmt.Sprintf("suggestions[%d]", idx)); timeErr != nil {
		err = appendEditPlannerError(err, "%s", timeErr.Error())
	}
	if len(refProblems) > 0 {
		for _, problem := range refProblems {
			err = appendEditPlannerError(err, "%s", problem)
		}
	}
	if len(suggestion.SourceRefs) == 0 {
		err = appendEditPlannerError(err, "suggestions[%d].sourceRefs is required", idx)
	}
	if len(suggestion.Clips) == 0 {
		err = appendEditPlannerError(err, "suggestions[%d].clips is required", idx)
	}
	suggestion.Clips = normalizeClips(suggestion.Clips)
	if len(suggestion.Clips) == 0 {
		err = appendEditPlannerError(err, "suggestions[%d].clips is required", idx)
	}
	for ci, clip := range suggestion.Clips {
		if clipErr := validateClip(&clip, index, idx, ci); clipErr != nil {
			err = appendEditPlannerError(err, "%s", clipErr.Error())
		}
		suggestion.Clips[ci] = clip
	}
	return err
}

func validateClip(clip *SuggestedClip, index editPlannerEvidenceIndex, suggestionIdx, clipIdx int) error {
	var err error
	var refProblems []string
	clip.ID = strings.TrimSpace(clip.ID)
	clip.Title = strings.TrimSpace(clip.Title)
	clip.Reason = strings.TrimSpace(clip.Reason)
	clip.SourceAssetID = strings.TrimSpace(clip.SourceAssetID)
	clip.SourceRefs = normalizeAndValidateSourceRefs(clip.SourceRefs, index, &refProblems, fmt.Sprintf("suggestions[%d].clips[%d].sourceRefs", suggestionIdx, clipIdx))
	if clip.ID == "" {
		err = appendEditPlannerError(err, "suggestions[%d].clips[%d].id is required", suggestionIdx, clipIdx)
	}
	if clip.Title == "" {
		err = appendEditPlannerError(err, "suggestions[%d].clips[%d].title is required", suggestionIdx, clipIdx)
	}
	if clip.Reason == "" {
		err = appendEditPlannerError(err, "suggestions[%d].clips[%d].reason is required", suggestionIdx, clipIdx)
	}
	if scoreErr := validateScore(clip.Score); scoreErr != nil {
		err = appendEditPlannerError(err, "suggestions[%d].clips[%d].score %v", suggestionIdx, clipIdx, scoreErr)
	}
	if timeErr := validateTimeRange(clip.StartMs, clip.EndMs, index.DurationMs, fmt.Sprintf("suggestions[%d].clips[%d]", suggestionIdx, clipIdx)); timeErr != nil {
		err = appendEditPlannerError(err, "%s", timeErr.Error())
	}
	if clip.EndMs-clip.StartMs > min64(index.DurationMs, int64(maxEditPlanClipDuration/time.Millisecond)) {
		err = appendEditPlannerError(err, "suggestions[%d].clips[%d] duration exceeds %dms", suggestionIdx, clipIdx, min64(index.DurationMs, int64(maxEditPlanClipDuration/time.Millisecond)))
	}
	if len(refProblems) > 0 {
		for _, problem := range refProblems {
			err = appendEditPlannerError(err, "%s", problem)
		}
	}
	if len(clip.SourceRefs) == 0 {
		err = appendEditPlannerError(err, "suggestions[%d].clips[%d].sourceRefs is required", suggestionIdx, clipIdx)
	} else {
		if clip.SourceAssetID == "" {
			clip.SourceAssetID = clip.SourceRefs[0].SourceAssetID
		}
		for _, ref := range clip.SourceRefs {
			if ref.SourceAssetID != "" && ref.SourceAssetID != clip.SourceAssetID {
				err = appendEditPlannerError(err, "suggestions[%d].clips[%d] sourceAssetId %q does not match source ref asset %q", suggestionIdx, clipIdx, clip.SourceAssetID, ref.SourceAssetID)
			}
		}
	}
	return err
}

func validateScore(score float64) error {
	if math.IsNaN(score) || math.IsInf(score, 0) {
		return errors.New("must be a finite number")
	}
	if score < 0 || score > 1 {
		return fmt.Errorf("must be between 0 and 1, got %v", score)
	}
	return nil
}

func validateTimeRange(startMs, endMs, durationMs int64, field string) error {
	if startMs < 0 || endMs < 0 {
		return fmt.Errorf("%s timecodes must be non-negative", field)
	}
	if endMs <= startMs {
		return fmt.Errorf("%s end must be greater than start", field)
	}
	if durationMs > 0 && endMs > durationMs {
		return fmt.Errorf("%s end %d exceeds duration %d", field, endMs, durationMs)
	}
	if durationMs > 0 && startMs >= durationMs {
		return fmt.Errorf("%s start %d exceeds duration %d", field, startMs, durationMs)
	}
	return nil
}

func normalizeHighlights(highlights []Highlight) []Highlight {
	out := make([]Highlight, 0, len(highlights))
	seen := map[string]struct{}{}
	for _, highlight := range highlights {
		highlight.SourceRefs = normalizeSourceRefs(highlight.SourceRefs)
		key := canonicalHighlightKey(highlight)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, highlight)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].StartMs == out[j].StartMs {
			if out[i].EndMs == out[j].EndMs {
				return out[i].ID < out[j].ID
			}
			return out[i].EndMs < out[j].EndMs
		}
		return out[i].StartMs < out[j].StartMs
	})
	return out
}

func normalizeSuggestions(suggestions []EditSuggestion) []EditSuggestion {
	out := make([]EditSuggestion, 0, len(suggestions))
	seen := map[string]struct{}{}
	for _, suggestion := range suggestions {
		suggestion.SourceRefs = normalizeSourceRefs(suggestion.SourceRefs)
		suggestion.Clips = normalizeClips(suggestion.Clips)
		key := canonicalSuggestionKey(suggestion)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, suggestion)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].StartMs == out[j].StartMs {
			if out[i].EndMs == out[j].EndMs {
				return out[i].ID < out[j].ID
			}
			return out[i].EndMs < out[j].EndMs
		}
		return out[i].StartMs < out[j].StartMs
	})
	return out
}

func normalizeClips(clips []SuggestedClip) []SuggestedClip {
	out := make([]SuggestedClip, 0, len(clips))
	seen := map[string]struct{}{}
	for _, clip := range clips {
		clip.SourceRefs = normalizeSourceRefs(clip.SourceRefs)
		key := canonicalClipKey(clip)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, clip)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].StartMs == out[j].StartMs {
			if out[i].EndMs == out[j].EndMs {
				return out[i].ID < out[j].ID
			}
			return out[i].EndMs < out[j].EndMs
		}
		return out[i].StartMs < out[j].StartMs
	})
	return out
}

func normalizeSourceRefs(refs []SourceRef) []SourceRef {
	out := make([]SourceRef, 0, len(refs))
	seen := map[string]struct{}{}
	for _, ref := range refs {
		ref.RefID = strings.TrimSpace(ref.RefID)
		ref.SourceKind = strings.TrimSpace(ref.SourceKind)
		ref.SourceAssetID = strings.TrimSpace(ref.SourceAssetID)
		ref.FactKind = strings.TrimSpace(ref.FactKind)
		ref.Text = strings.TrimSpace(ref.Text)
		key := canonicalSourceRefKey(ref)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, ref)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].RefID == out[j].RefID {
			if out[i].SourceAssetID == out[j].SourceAssetID {
				return out[i].SourceKind < out[j].SourceKind
			}
			return out[i].SourceAssetID < out[j].SourceAssetID
		}
		return out[i].RefID < out[j].RefID
	})
	return out
}

func normalizeAndValidateSourceRefs(refs []SourceRef, index editPlannerEvidenceIndex, problems *[]string, field string) []SourceRef {
	refs = normalizeSourceRefs(refs)
	if len(refs) == 0 {
		if problems != nil {
			*problems = append(*problems, fmt.Sprintf("%s must contain at least one source ref", field))
		}
		return nil
	}
	for _, ref := range refs {
		if ref.RefID == "" {
			if problems != nil {
				*problems = append(*problems, fmt.Sprintf("%s contains an empty source ref id", field))
			}
			continue
		}
		known, ok := index.knownRef(ref.RefID)
		if !ok {
			if problems != nil {
				*problems = append(*problems, fmt.Sprintf("%s contains unknown source ref %q", field, ref.RefID))
			}
			continue
		}
		if ref.SourceAssetID != "" && ref.SourceAssetID != known.AssetID {
			if problems != nil {
				*problems = append(*problems, fmt.Sprintf("%s source ref %q must cite asset %q, got %q", field, ref.RefID, known.AssetID, ref.SourceAssetID))
			}
		}
		if ref.SourceKind != "" && ref.SourceKind != known.Kind {
			if problems != nil {
				*problems = append(*problems, fmt.Sprintf("%s source ref %q must use source kind %q, got %q", field, ref.RefID, known.Kind, ref.SourceKind))
			}
		}
		if ref.StartMs != 0 || ref.EndMs != 0 {
			if err := validateTimeRange(ref.StartMs, ref.EndMs, index.DurationMs, field+"."+ref.RefID); err != nil && problems != nil {
				*problems = append(*problems, err.Error())
			}
		}
	}
	return refs
}

func canonicalSourceRefKey(ref SourceRef) string {
	return strings.Join([]string{
		ref.RefID,
		ref.SourceKind,
		ref.SourceAssetID,
		ref.FactKind,
		strconv.FormatInt(ref.StartMs, 10),
		strconv.FormatInt(ref.EndMs, 10),
		ref.Text,
	}, "|")
}

func canonicalHighlightKey(value Highlight) string {
	return strings.Join([]string{
		value.ID,
		value.Title,
		value.Reason,
		strconv.FormatInt(value.StartMs, 10),
		strconv.FormatInt(value.EndMs, 10),
		strconv.FormatFloat(value.Score, 'f', -1, 64),
		canonicalRefsKey(value.SourceRefs),
	}, "|")
}

func canonicalSuggestionKey(value EditSuggestion) string {
	return strings.Join([]string{
		value.ID,
		value.Title,
		value.Reason,
		strconv.FormatInt(value.StartMs, 10),
		strconv.FormatInt(value.EndMs, 10),
		strconv.FormatFloat(value.Score, 'f', -1, 64),
		canonicalRefsKey(value.SourceRefs),
		canonicalClipsKey(value.Clips),
	}, "|")
}

func canonicalClipKey(value SuggestedClip) string {
	return strings.Join([]string{
		value.ID,
		value.Title,
		value.Reason,
		value.SourceAssetID,
		strconv.FormatInt(value.StartMs, 10),
		strconv.FormatInt(value.EndMs, 10),
		strconv.FormatFloat(value.Score, 'f', -1, 64),
		canonicalRefsKey(value.SourceRefs),
	}, "|")
}

func canonicalClipsKey(values []SuggestedClip) string {
	if len(values) == 0 {
		return ""
	}
	keys := make([]string, 0, len(values))
	for _, value := range values {
		keys = append(keys, canonicalClipKey(value))
	}
	sort.Strings(keys)
	return strings.Join(keys, "\x1f")
}

func canonicalRefsKey(values []SourceRef) string {
	if len(values) == 0 {
		return ""
	}
	keys := make([]string, 0, len(values))
	for _, value := range values {
		keys = append(keys, canonicalSourceRefKey(value))
	}
	sort.Strings(keys)
	return strings.Join(keys, "\x1f")
}

func min64(a, b int64) int64 {
	if a <= 0 {
		return b
	}
	if b <= 0 {
		return a
	}
	if a < b {
		return a
	}
	return b
}

func previewText(text string, max int) string {
	text = strings.TrimSpace(text)
	if max <= 0 || len(text) <= max {
		return text
	}
	return strings.TrimSpace(text[:max]) + "…"
}

func redactPacketText(value any) any {
	raw, err := json.Marshal(value)
	if err != nil {
		return value
	}
	return json.RawMessage(bytes.TrimSpace(raw))
}
