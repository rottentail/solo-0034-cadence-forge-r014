package media

import (
	"cadenceforge/internal/domain"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"time"
)

type Prober struct{ Binary string }

func (p Prober) Probe(ctx context.Context, path string) (domain.Media, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	data, err := Run(ctx, p.Binary, []string{"-v", "error", "-protocol_whitelist", "file,pipe", "-format_whitelist", Formats, "-show_format", "-show_streams", "-of", "json", path}, 1<<20)
	if err != nil {
		return domain.Media{}, err
	}
	var raw struct {
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
		Streams []struct {
			Index       int    `json:"index"`
			Type        string `json:"codec_type"`
			Codec       string `json:"codec_name"`
			Width       int    `json:"width"`
			Height      int    `json:"height"`
			Rate        string `json:"sample_rate"`
			Channels    int    `json:"channels"`
			Layout      string `json:"channel_layout"`
			Duration    string `json:"duration"`
			Disposition struct {
				Attached int `json:"attached_pic"`
				Default  int `json:"default"`
			} `json:"disposition"`
		} `json:"streams"`
	}
	if err = json.Unmarshal(data, &raw); err != nil {
		return domain.Media{}, err
	}
	duration, _ := strconv.ParseFloat(raw.Format.Duration, 64)
	m := domain.Media{}
	for _, s := range raw.Streams {
		if s.Type == "video" && s.Disposition.Attached == 0 && !m.Video {
			m.Video = true
			m.Width = s.Width
			m.Height = s.Height
			m.VideoCodec = s.Codec
		}
		if s.Type == "audio" {
			rate, _ := strconv.Atoi(s.Rate)
			stream := domain.AudioStream{Ordinal: len(m.AudioStreams), Index: s.Index, Codec: s.Codec, SampleRate: rate, Channels: s.Channels, Layout: s.Layout, Default: s.Disposition.Default != 0}
			if stream.Layout == "" {
				stream.Layout = fallbackLayout(s.Channels)
			}
			m.AudioStreams = append(m.AudioStreams, stream)
			if !m.Audio {
				m.Audio = true
				m.SampleRate = rate
				m.Channels = s.Channels
				m.AudioCodec = s.Codec
			}
		}
		if duration == 0 {
			d, _ := strconv.ParseFloat(s.Duration, 64)
			if d > duration {
				duration = d
			}
		}
	}
	if math.IsNaN(duration) || math.IsInf(duration, 0) || duration <= 0 || duration > 121 {
		return m, fmt.Errorf("media duration must be >0 and <=121 seconds")
	}
	if !m.Video && !m.Audio {
		return m, fmt.Errorf("no usable audio or video stream")
	}
	if m.Video && (m.Width < 1 || m.Height < 1 || m.Width > 3840 || m.Height > 2160) {
		return m, fmt.Errorf("video exceeds processing dimensions")
	}
	if m.Audio && (m.Channels < 1 || m.Channels > 8 || m.SampleRate < 8000 || m.SampleRate > 192000) {
		return m, fmt.Errorf("unsupported audio layout")
	}
	for _, stream := range m.AudioStreams {
		if stream.Channels < 1 || stream.Channels > 8 || stream.SampleRate < 8000 || stream.SampleRate > 192000 {
			return m, fmt.Errorf("unsupported audio layout")
		}
	}
	if len(m.AudioStreams) > 8 {
		return m, fmt.Errorf("source has too many audio streams")
	}
	m.DurationMS = int64(math.Round(duration * 1000))
	return m, nil
}

// fallbackLayout names a layout when the container omits it, so probe facts
// stay self-describing for later routing decisions.
func fallbackLayout(channels int) string {
	switch channels {
	case 1:
		return "mono"
	case 2:
		return "stereo"
	case 3:
		return "3.0"
	case 4:
		return "quad"
	case 5:
		return "5.0"
	case 6:
		return "5.1"
	case 7:
		return "6.1"
	case 8:
		return "7.1"
	}
	return ""
}
