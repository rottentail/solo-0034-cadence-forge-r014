package domain

import "time"

// AudioStream is one probed audio stream. Ordinal is the stable position among
// audio streams (the same ordinal FFmpeg exposes as a:N) and is the identity
// sheets and renders reference. Index is the container index and is
// informational.
type AudioStream struct {
	Ordinal    int    `json:"ordinal"`
	Index      int    `json:"index"`
	Codec      string `json:"codec"`
	SampleRate int    `json:"sample_rate"`
	Channels   int    `json:"channels"`
	Layout     string `json:"channel_layout"`
	Default    bool   `json:"default"`
}
type Media struct {
	DurationMS   int64         `json:"duration_ms"`
	Video        bool          `json:"video"`
	Audio        bool          `json:"audio"`
	Width        int           `json:"width"`
	Height       int           `json:"height"`
	SampleRate   int           `json:"sample_rate"`
	Channels     int           `json:"channels"`
	VideoCodec   string        `json:"video_codec"`
	AudioCodec   string        `json:"audio_codec"`
	AudioStreams []AudioStream `json:"audio_streams,omitempty"`
}
type Source struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Digest    string    `json:"digest"`
	Bytes     int64     `json:"bytes"`
	Media     Media     `json:"media"`
	CreatedAt time.Time `json:"created_at"`
}

// AudioStreamAt resolves an audio ordinal against the frozen probe facts.
// Sources accepted before per-stream facts existed fall back to their recorded
// first-stream facts, so legacy data roots keep rendering audio instead of
// silently degrading to silence.
func (m Media) AudioStreamAt(ordinal int) (AudioStream, bool) {
	if len(m.AudioStreams) > 0 {
		for _, stream := range m.AudioStreams {
			if stream.Ordinal == ordinal {
				return stream, true
			}
		}
		return AudioStream{}, false
	}
	if m.Audio && ordinal == 0 {
		return AudioStream{Ordinal: 0, Index: 0, Codec: m.AudioCodec, SampleRate: m.SampleRate, Channels: m.Channels, Layout: "stereo", Default: true}, true
	}
	return AudioStream{}, false
}

// AudioStreamCount is the number of addressable audio ordinals in these facts.
func (m Media) AudioStreamCount() int {
	if len(m.AudioStreams) > 0 {
		return len(m.AudioStreams)
	}
	if m.Audio {
		return 1
	}
	return 0
}

// AudioTake routes one probed audio stream of the span's source into a named
// sound path. Stream is an audio ordinal from frozen ingest facts; it is never
// resolved by probing at render time.
type AudioTake struct {
	Stream int    `json:"stream,omitempty"`
	Role   string `json:"role,omitempty"`
}
type Span struct {
	SourceID string  `json:"source_id"`
	InMS     int64   `json:"in_ms"`
	OutMS    int64   `json:"out_ms"`
	GainDB   float64 `json:"gain_db"`
	// AudioTakes empty preserves the historical behaviour: audio stream 0 on
	// the program path.
	AudioTakes []AudioTake `json:"audio_takes,omitempty"`
}
type Cue struct {
	SourceID string `json:"source_id"`
	StartMS  int64  `json:"start_ms"`
	EndMS    int64  `json:"end_ms"`
	Text     string `json:"text"`
}
type Sheet struct {
	ID        string    `json:"id"`
	Revision  int       `json:"revision"`
	Name      string    `json:"name"`
	Spans     []Span    `json:"spans"`
	Cues      []Cue     `json:"cues"`
	CreatedAt time.Time `json:"created_at"`
}
type Recipe struct {
	ID         string  `json:"id"`
	Revision   int     `json:"revision"`
	Name       string  `json:"name"`
	Format     string  `json:"format"`
	Width      int     `json:"width"`
	Height     int     `json:"height"`
	FPS        int     `json:"fps"`
	SampleRate int     `json:"sample_rate"`
	Normalize  bool    `json:"normalize"`
	TargetLUFS float64 `json:"target_lufs"`
	// AudioReviewPolicy governs the two-channel review mix only. Per-path stems
	// always keep their authored gain and polarity. Empty means intelligible.
	AudioReviewPolicy string `json:"audio_review_policy,omitempty"`
	// AudioReviewAnchor names the path the review mix prioritises. Empty
	// prefers program, then field, then reference.
	AudioReviewAnchor string `json:"audio_review_anchor,omitempty"`
}
type Placement struct {
	SourceID    string      `json:"source_id"`
	SourceInMS  int64       `json:"source_in_ms"`
	SourceOutMS int64       `json:"source_out_ms"`
	OutputInMS  int64       `json:"output_in_ms"`
	OutputOutMS int64       `json:"output_out_ms"`
	GainDB      float64     `json:"gain_db"`
	AudioTakes  []AudioTake `json:"audio_takes"`
	// AudioMissing marks a span whose frozen source has no usable audio; the
	// placement contributes silence to every path.
	AudioMissing bool `json:"audio_missing,omitempty"`
}

// RoutePath records one sound path's frozen stream choices and its measured
// two-channel review treatment. Stems keep phase and level regardless of the
// review decisions recorded here.
type RoutePath struct {
	Role             string   `json:"role"`
	Placements       int      `json:"placements"`
	Takes            int      `json:"takes"`
	SilentPlacements int      `json:"silent_placements"`
	SourceIDs        []string `json:"source_ids"`
	Streams          []int    `json:"streams"`
	Channels         int      `json:"channels"`
	Layout           string   `json:"layout"`
	RMSDB            *float64 `json:"rms_dbfs,omitempty"`
	PeakDB           *float64 `json:"peak_dbfs,omitempty"`
	Silent           bool     `json:"silent"`
	Correlation      *float64 `json:"correlation_to_anchor,omitempty"`
	LeftCorrelation  *float64 `json:"left_correlation_to_anchor,omitempty"`
	RightCorrelation *float64 `json:"right_correlation_to_anchor,omitempty"`
	Measured         bool     `json:"measured"`
	ReviewIncluded   bool     `json:"review_included"`
	ReviewGainDB     float64  `json:"review_gain_db"`
	ReviewPolarity   string   `json:"review_polarity"`
	StemFile         string   `json:"stem_file,omitempty"`
	Notes            []string `json:"notes,omitempty"`
}

// RoutingDecision is frozen into the render plan at submit time from frozen
// source identities and a bounded measurement of the assembled composition.
// Rendering executes this record and never re-probes sources to guess routing.
type RoutingDecision struct {
	Schema     int         `json:"schema"`
	Mode       string      `json:"mode"`
	Policy     string      `json:"policy"`
	AnchorRole string      `json:"anchor_role"`
	WindowMS   int64       `json:"window_ms"`
	Paths      []RoutePath `json:"paths"`
	LossNotes  []string    `json:"loss_notes"`
}

type Plan struct {
	DurationMS int64       `json:"duration_ms"`
	Placements []Placement `json:"placements"`
	Recipe     Recipe      `json:"recipe"`
	VTT        string      `json:"vtt"`
	// Routing records the frozen path identity and composition-time
	// relationship shared by picture editing and sound editing.
	Routing *RoutingDecision `json:"routing,omitempty"`
}
