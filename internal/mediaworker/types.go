// Package mediaworker adapts the repository's Python media worker to Go.
//
// The package deliberately exposes the small part of the worker contract that
// Go callers need: production input, validation/run results, artifacts, and
// actionable capability blockers. Python implementation details stay behind
// CommandWorker.
package mediaworker

import "encoding/json"

// ProductionSpec is the stable input contract written to the Python worker's
// job file. Optional sections are pointers so callers can omit them without
// manufacturing Python-specific defaults.
type ProductionSpec struct {
	ProductionSpecVersion string         `json:"productionSpecVersion"`
	JobID                 string         `json:"jobId"`
	Segments              []SegmentSpec  `json:"segments"`
	Timeline              *TimelineSpec  `json:"timeline,omitempty"`
	Audio                 *AudioSpec     `json:"audio,omitempty"`
	Outputs               *OutputsSpec   `json:"outputs,omitempty"`
	Scenes                []SceneSpec    `json:"scenes,omitempty"`
	Assets                []AssetSpec    `json:"assets,omitempty"`
	Render                *RenderSpec    `json:"render,omitempty"`
	Paths                 *PathsSpec     `json:"paths,omitempty"`
	Voice                 *VoiceSpec     `json:"voice,omitempty"`
	Additional            map[string]any `json:"-"`
}

// MarshalJSON preserves explicitly supported fields and allows forward
// compatible contract fields to pass through without exposing them in the Go
// result types.
func (s ProductionSpec) MarshalJSON() ([]byte, error) {
	type plain ProductionSpec
	base, err := json.Marshal(plain(s))
	if err != nil {
		return nil, err
	}
	if len(s.Additional) == 0 {
		return base, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(base, &fields); err != nil {
		return nil, err
	}
	for key, value := range s.Additional {
		if _, exists := fields[key]; !exists {
			encoded, err := json.Marshal(value)
			if err != nil {
				return nil, err
			}
			fields[key] = encoded
		}
	}
	return json.Marshal(fields)
}

// SegmentSpec describes one narration or supplied-audio segment.
type SegmentSpec struct {
	ID             string         `json:"id"`
	Text           string         `json:"text,omitempty"`
	AudioPath      any            `json:"audioPath,omitempty"`
	PauseAfterMs   int            `json:"pauseAfterMs,omitempty"`
	PauseMs        int            `json:"pauseMs,omitempty"`
	Profile        string         `json:"profile,omitempty"`
	CaptionLines   []string       `json:"captionLines,omitempty"`
	EditorialLabel string         `json:"editorialLabel,omitempty"`
	TTS            map[string]any `json:"tts,omitempty"`
}

// TimelineSpec controls the effective playback rate.
type TimelineSpec struct {
	PlaybackRate      float64 `json:"playbackRate,omitempty"`
	InputPlaybackRate float64 `json:"inputPlaybackRate,omitempty"`
}

// AudioSpec controls PCM normalization.
type AudioSpec struct {
	SampleRate int `json:"sampleRate,omitempty"`
	Channels   int `json:"channels,omitempty"`
}

// OutputsSpec controls worker output encoding.
type OutputsSpec struct {
	AudioFormat string `json:"audioFormat,omitempty"`
}

// SceneSpec describes a scene window and optional compositor properties.
type SceneSpec struct {
	ID         string         `json:"id"`
	SegmentIDs []string       `json:"segmentIds"`
	Props      map[string]any `json:"props,omitempty"`
	Asset      string         `json:"asset,omitempty"`
	AssetRef   string         `json:"assetRef,omitempty"`
	Overlay    map[string]any `json:"overlay,omitempty"`
}

// RenderSpec enables optional Remotion rendering.
type RenderSpec struct {
	Enabled     bool     `json:"enabled,omitempty"`
	CWD         string   `json:"cwd,omitempty"`
	Output      string   `json:"output,omitempty"`
	Entry       string   `json:"entry,omitempty"`
	Composition string   `json:"composition,omitempty"`
	Codec       string   `json:"codec,omitempty"`
	ExtraArgs   []string `json:"extraArgs,omitempty"`
	FPS         float64  `json:"fps,omitempty"`
	BurnInASS   bool     `json:"burnInAss,omitempty"`
}

// PathsSpec contains optional external capability paths understood by the
// worker. It is input configuration, not a description of worker internals.
type PathsSpec struct {
	Python             string   `json:"python,omitempty"`
	FFmpeg             string   `json:"ffmpeg,omitempty"`
	FFprobe            string   `json:"ffprobe,omitempty"`
	Remotion           string   `json:"remotion,omitempty"`
	EdgeTTSRoot        string   `json:"edgeTtsRoot,omitempty"`
	RemotionArgsPrefix []string `json:"remotionArgsPrefix,omitempty"`
}

// VoiceSpec contains the minimum voice configuration needed for TTS segments.
type VoiceSpec struct {
	Voice    string                       `json:"voice,omitempty"`
	Default  map[string]string            `json:"default,omitempty"`
	Profiles map[string]map[string]string `json:"profiles,omitempty"`
}

// CapabilityBlocker identifies a required capability that is unavailable.
type CapabilityBlocker struct {
	Capability  string `json:"capability"`
	Message     string `json:"message"`
	Remediation string `json:"remediation,omitempty"`
}

// Result is the Go-level result of validation or execution.
type Result struct {
	OK                   bool
	Valid                bool
	JobID                string
	OutputDir            string
	ArtifactManifestPath string
	Manifest             *ArtifactManifest
	DurationMs           int64
	Segments             int
	Blockers             []CapabilityBlocker
}

// ArtifactManifest is the validated output index produced by a successful
// worker run.
type ArtifactManifest struct {
	SchemaVersion         string     `json:"schemaVersion"`
	Kind                  string     `json:"kind"`
	JobID                 string     `json:"jobId"`
	ProductionSpecVersion string     `json:"productionSpecVersion"`
	Artifacts             []Artifact `json:"artifacts"`
}

// Artifact is one file owned by the worker output directory.
type Artifact struct {
	ID              string  `json:"id"`
	Kind            string  `json:"kind"`
	Path            string  `json:"path"`
	RelativePath    string  `json:"relativePath"`
	Bytes           int64   `json:"bytes"`
	SHA256          string  `json:"sha256"`
	MediaType       string  `json:"mediaType,omitempty"`
	DurationSeconds float64 `json:"durationSeconds,omitempty"`
	Width           int     `json:"width,omitempty"`
	Height          int     `json:"height,omitempty"`
	SampleRate      int     `json:"sampleRate,omitempty"`
	Channels        int     `json:"channels,omitempty"`
}
