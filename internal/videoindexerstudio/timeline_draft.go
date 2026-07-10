package videoindexerstudio

import (
	"errors"
	"fmt"
	"strings"
)

const (
	TimelineDraftSchemaVersion = 1
	TimelineTrackKindVideo     = "video"
	TimelineTransitionKindCut  = "cut"
)

type TimelineDraft struct {
	SchemaVersion     int           `json:"schemaVersion"`
	OriginJobID       string        `json:"originJobId"`
	SuggestionID      string        `json:"suggestionId"`
	PromptVersion     string        `json:"promptVersion"`
	PrimaryVideoTrack TimelineTrack `json:"primaryVideoTrack"`
}

type TimelineTrack struct {
	ID    string         `json:"id"`
	Kind  string         `json:"kind"`
	Clips []TimelineClip `json:"clips"`
}

type TimelineClip struct {
	ID              string             `json:"id"`
	SourceAssetID   string             `json:"sourceAssetId"`
	InMS            int64              `json:"inMs"`
	OutMS           int64              `json:"outMs"`
	TimelineStartMS int64              `json:"timelineStartMs"`
	DurationMS      int64              `json:"durationMs"`
	Transition      TimelineTransition `json:"transition"`
}

type TimelineTransition struct {
	Kind       string `json:"kind"`
	DurationMS int64  `json:"durationMs,omitempty"`
}

func (d TimelineDraft) Validate() error {
	var problems []string

	if d.SchemaVersion != TimelineDraftSchemaVersion {
		problems = append(problems, fmt.Sprintf("schemaVersion must be %d", TimelineDraftSchemaVersion))
	}
	d.OriginJobID = strings.TrimSpace(d.OriginJobID)
	d.SuggestionID = strings.TrimSpace(d.SuggestionID)
	d.PromptVersion = strings.TrimSpace(d.PromptVersion)
	if d.OriginJobID == "" {
		problems = append(problems, "originJobId is required")
	}
	if d.SuggestionID == "" {
		problems = append(problems, "suggestionId is required")
	}
	if d.PromptVersion == "" {
		problems = append(problems, "promptVersion is required")
	}

	track := d.PrimaryVideoTrack
	track.ID = strings.TrimSpace(track.ID)
	track.Kind = strings.TrimSpace(track.Kind)
	if track.ID == "" {
		problems = append(problems, "primaryVideoTrack.id is required")
	}
	if track.Kind != TimelineTrackKindVideo {
		problems = append(problems, fmt.Sprintf("primaryVideoTrack.kind must be %q", TimelineTrackKindVideo))
	}
	if len(track.Clips) == 0 {
		problems = append(problems, "primaryVideoTrack.clips is required")
	}

	var (
		sourceAssetID string
		nextStartMS   int64
		seenClipIDs   = map[string]struct{}{}
	)
	for i, clip := range track.Clips {
		clip.ID = strings.TrimSpace(clip.ID)
		clip.SourceAssetID = strings.TrimSpace(clip.SourceAssetID)
		clip.Transition.Kind = strings.TrimSpace(clip.Transition.Kind)

		if clip.ID == "" {
			problems = append(problems, fmt.Sprintf("primaryVideoTrack.clips[%d].id is required", i))
		} else if _, ok := seenClipIDs[clip.ID]; ok {
			problems = append(problems, fmt.Sprintf("primaryVideoTrack.clips[%d].id must be unique", i))
		} else {
			seenClipIDs[clip.ID] = struct{}{}
		}
		if clip.SourceAssetID == "" {
			problems = append(problems, fmt.Sprintf("primaryVideoTrack.clips[%d].sourceAssetId is required", i))
		} else if sourceAssetID == "" {
			sourceAssetID = clip.SourceAssetID
		} else if clip.SourceAssetID != sourceAssetID {
			problems = append(problems, fmt.Sprintf("primaryVideoTrack.clips[%d].sourceAssetId must match %q", i, sourceAssetID))
		}
		if clip.InMS < 0 {
			problems = append(problems, fmt.Sprintf("primaryVideoTrack.clips[%d].inMs must be non-negative", i))
		}
		if clip.OutMS <= clip.InMS {
			problems = append(problems, fmt.Sprintf("primaryVideoTrack.clips[%d].outMs must be greater than inMs", i))
		}
		if clip.DurationMS != clip.OutMS-clip.InMS {
			problems = append(problems, fmt.Sprintf("primaryVideoTrack.clips[%d].durationMs must equal outMs-inMs", i))
		}
		if clip.TimelineStartMS != nextStartMS {
			problems = append(problems, fmt.Sprintf("primaryVideoTrack.clips[%d].timelineStartMs must equal %d", i, nextStartMS))
		}
		if clip.Transition.Kind != TimelineTransitionKindCut {
			problems = append(problems, fmt.Sprintf("primaryVideoTrack.clips[%d].transition.kind must be %q", i, TimelineTransitionKindCut))
		}
		if clip.Transition.DurationMS != 0 {
			problems = append(problems, fmt.Sprintf("primaryVideoTrack.clips[%d].transition.durationMs must be 0 for cut transitions", i))
		}
		nextStartMS += clip.OutMS - clip.InMS
	}

	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}
