package media

import (
	"cadenceforge/internal/audiograph"
	"cadenceforge/internal/domain"
	"cadenceforge/internal/routing"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// RoutingSource resolves a frozen source id to its retained object path.
type RoutingSource func(sourceID string) (string, bool)

// MeasureRouting assembles every recorded sound path from the frozen plan and
// measures its real level and per-channel phase. One bounded FFmpeg pass writes
// the stems; a second bounded pass reads them.
func MeasureRouting(ctx context.Context, binary string, plan domain.Plan, resolve RoutingSource) (domain.RoutingMeasurement, error) {
	window := plan.DurationMS
	if window > routing.AnalysisWindowMS {
		window = routing.AnalysisWindowMS
	}
	out := domain.RoutingMeasurement{WindowMS: window, Levels: map[string]domain.PathLevel{}, Correlations: map[string]domain.PathCorrelation{}}
	roles := domain.PlanAudioRoles(plan)
	if len(roles) == 0 || window <= 0 {
		return out, nil
	}
	out.Measured = true

	inputs := map[string]int{}
	paths := []string{}
	for _, p := range plan.Placements {
		if p.AudioMissing || len(p.AudioTakes) == 0 {
			continue
		}
		if _, ok := inputs[p.SourceID]; ok {
			continue
		}
		path, ok := resolve(p.SourceID)
		if !ok {
			return out, fmt.Errorf("source %s object missing", p.SourceID)
		}
		inputs[p.SourceID] = len(paths)
		paths = append(paths, path)
	}
	if len(paths) == 0 {
		return out, nil
	}

	dir, err := os.MkdirTemp("", "cadence-routing-*")
	if err != nil {
		return out, err
	}
	defer os.RemoveAll(dir)

	stems, labels, err := audiograph.Build(plan, roles, inputs, routing.AnalysisRate)
	if err != nil {
		return out, err
	}
	files := map[string]string{}
	outputs := []string{}
	order := []string{}
	args := []string{"-nostdin", "-hide_banner", "-loglevel", "error", "-filter_complex_threads", "1"}
	for _, path := range paths {
		args = append(args, "-protocol_whitelist", "file,pipe", "-format_whitelist", Formats, "-i", path)
	}
	// One graph assembles every path; readings happen on the written stems.
	for _, role := range roles {
		file := filepath.Join(dir, "stem-"+role+".wav")
		files[role] = file
		order = append(order, role)
		outputs = append(outputs, "-map", labels[role], "-c:a", "pcm_s16le", "-ar", strconv.Itoa(routing.AnalysisRate), "-ac", "2", file)
	}
	args = append(args, "-filter_complex", strings.Join(stems, ";"))
	args = append(args, outputs...)
	args = append(args, "-t", audiograph.Seconds(window))
	passCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	if _, err = Run(passCtx, binary, args, 65536); err != nil {
		return out, fmt.Errorf("routing stem assembly: %w", err)
	}
	for _, role := range order {
		level, err := pathLevel(passCtx, binary, files[role])
		if err != nil {
			return out, err
		}
		out.Levels[role] = level
	}
	// Measure every pair so the recorded decision can name any path as anchor
	// without invalidating a reading. Three paths is the documented ceiling, so
	// this stays bounded.
	for i := 0; i < len(order); i++ {
		for j := i + 1; j < len(order); j++ {
			corr, err := pathCorrelation(passCtx, binary, files[order[i]], files[order[j]], out.WindowMS, dir, order[i]+"-"+order[j])
			if err != nil {
				return out, err
			}
			out.Correlations[domain.CorrelationKey(order[i], order[j])] = corr
		}
	}
	return out, nil
}

// pathLevel reads a stem's real level. Digital silence (-inf) is reported as
// silent, never as a fabricated number.
func pathLevel(ctx context.Context, binary, file string) (domain.PathLevel, error) {
	text, err := RunReport(ctx, binary, []string{"-nostdin", "-hide_banner", "-i", file, "-af", "astats=metadata=0:reset=0:measure_perchannel=none:measure_overall=RMS_level+Peak_level", "-f", "null", "-"}, 1<<20)
	if err != nil {
		return domain.PathLevel{}, err
	}
	rms, _ := lastNumber(text, "RMS level dB:")
	peak, _ := lastNumber(text, "Peak level dB:")
	return domain.PathLevel{RMSDB: rms, PeakDB: peak, Silent: rms == nil}, nil
}

// pathCorrelation computes lag-zero correlation against the anchor for both
// delivered channels, so a partial inversion (one channel only) is reported as
// such rather than averaged away.
func pathCorrelation(ctx context.Context, binary, anchorFile, pathFile string, window int64, dir, role string) (domain.PathCorrelation, error) {
	leftFile := filepath.Join(dir, "corr-"+role+"-L.txt")
	rightFile := filepath.Join(dir, "corr-"+role+"-R.txt")
	graph := fmt.Sprintf("[0:a]aformat=sample_fmts=fltp:channel_layouts=stereo,pan=mono|c0=c0[al];[1:a]aformat=sample_fmts=fltp:channel_layouts=stereo,pan=mono|c0=c0[bl];[al][bl]axcorrelate=size=2048:algo=fast,atrim=start=%s,astats=metadata=1:reset=0:measure_perchannel=none:measure_overall=DC_offset,ametadata=mode=print:file=%s:direct=1[ol];", audiograph.Seconds(routing.WarmupMS), leftFile)
	graph += fmt.Sprintf("[0:a]aformat=sample_fmts=fltp:channel_layouts=stereo,pan=mono|c0=c1[ar];[1:a]aformat=sample_fmts=fltp:channel_layouts=stereo,pan=mono|c0=c1[br];[ar][br]axcorrelate=size=2048:algo=fast,atrim=start=%s,astats=metadata=1:reset=0:measure_perchannel=none:measure_overall=DC_offset,ametadata=mode=print:file=%s:direct=1[orr]", audiograph.Seconds(routing.WarmupMS), rightFile)
	args := []string{"-nostdin", "-hide_banner", "-loglevel", "error", "-i", anchorFile, "-i", pathFile, "-filter_complex", graph, "-map", "[ol]", "-map", "[orr]", "-t", audiograph.Seconds(window), "-f", "null", "-"}
	if _, err := RunReport(ctx, binary, args, 65536); err != nil {
		return domain.PathCorrelation{}, fmt.Errorf("routing correlation: %w", err)
	}
	left, err := readLastValue(leftFile)
	if err != nil {
		return domain.PathCorrelation{}, err
	}
	right, err := readLastValue(rightFile)
	if err != nil {
		return domain.PathCorrelation{}, err
	}
	out := domain.PathCorrelation{Left: left, Right: right}
	switch {
	case left != nil && right != nil:
		average := (*left + *right) / 2
		out.Overall = &average
	case left != nil:
		out.Overall = left
	case right != nil:
		out.Overall = right
	}
	return out, nil
}

func readLastValue(path string) (*float64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	value, _ := lastNumber(string(data), "lavfi.astats.Overall.DC_offset=")
	return value, nil
}
func lastNumber(text, marker string) (*float64, bool) {
	value := (*float64)(nil)
	found := false
	for _, line := range strings.Split(text, "\n") {
		index := strings.Index(line, marker)
		if index < 0 {
			continue
		}
		raw := strings.TrimSpace(line[index+len(marker):])
		found = true
		if strings.Contains(strings.ToLower(raw), "inf") || strings.Contains(strings.ToLower(raw), "nan") {
			value = nil
			continue
		}
		parsed, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			continue
		}
		value = &parsed
	}
	return value, found
}

func peakOKText(text string) bool {
	v, ok := lastNumber(text, "Peak level dB:")
	return ok && v != nil
}
