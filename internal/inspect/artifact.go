package inspect

import (
	"cadenceforge/internal/domain"
	"cadenceforge/internal/media"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
)

func Digest(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", n, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}
func Artifact(ctx context.Context, prober media.Prober, path string, r domain.Render) (domain.Inspection, error) {
	m, err := prober.Probe(ctx, path)
	if err != nil {
		return domain.Inspection{}, err
	}
	delta := m.DurationMS - r.Plan.DurationMS
	tolerance := int64(100)
	if r.Recipe.Format == "mp4" {
		tolerance = max(tolerance, int64(2000/r.Recipe.FPS))
	}
	if math.Abs(float64(delta)) > float64(tolerance) {
		return domain.Inspection{}, fmt.Errorf("output duration delta %dms exceeds %dms", delta, tolerance)
	}
	if !m.Audio || m.SampleRate != r.Recipe.SampleRate {
		return domain.Inspection{}, fmt.Errorf("output audio contract mismatch")
	}
	if r.Recipe.Format == "mp4" && (!m.Video || m.Width != r.Recipe.Width || m.Height != r.Recipe.Height) {
		return domain.Inspection{}, fmt.Errorf("output video contract mismatch")
	}
	digest, n, err := Digest(path)
	if err != nil {
		return domain.Inspection{}, err
	}
	inspection := domain.Inspection{Media: m, Digest: digest, Bytes: n, DurationDeltaMS: delta}
	if r.Plan.Routing != nil {
		for _, route := range r.Plan.Routing.Paths {
			if route.StemFile == "" {
				continue
			}
			stem, err := Stem(ctx, prober, filepath.Join(filepath.Dir(path), route.StemFile), route, r)
			if err != nil {
				return domain.Inspection{}, err
			}
			inspection.Stems = append(inspection.Stems, stem)
		}
	}
	return inspection, nil
}

// Stem verifies one preserved path stem is a real two-channel PCM file aligned
// with the composition, and reports its own digest and duration delta.
func Stem(ctx context.Context, prober media.Prober, path string, route domain.RoutePath, r domain.Render) (domain.StemInspection, error) {
	m, err := prober.Probe(ctx, path)
	if err != nil {
		return domain.StemInspection{}, err
	}
	delta := m.DurationMS - r.Plan.DurationMS
	tolerance := int64(100)
	if math.Abs(float64(delta)) > float64(tolerance) {
		return domain.StemInspection{}, fmt.Errorf("stem %s duration delta %dms exceeds %dms", route.Role, delta, tolerance)
	}
	if !m.Audio || m.Video || m.Channels != 2 || m.SampleRate != r.Recipe.SampleRate || m.AudioCodec != "pcm_s16le" {
		return domain.StemInspection{}, fmt.Errorf("stem %s contract mismatch", route.Role)
	}
	digest, n, err := Digest(path)
	if err != nil {
		return domain.StemInspection{}, err
	}
	return domain.StemInspection{Role: route.Role, File: route.StemFile, Digest: digest, Bytes: n, Channels: m.Channels, SampleRate: m.SampleRate, DurationDeltaMS: delta}, nil
}
