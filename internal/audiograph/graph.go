// Package audiograph derives FFmpeg filter statements for the independent
// sound paths recorded in a frozen domain.Plan. Delivery encoding and routing
// measurement both use it, so the measured assembly is the delivered assembly.
// It performs no probing and makes no routing decisions.
package audiograph

import (
	"cadenceforge/internal/domain"
	"fmt"
	"strings"
)

// Seconds renders milliseconds as an FFmpeg duration literal.
func Seconds(ms int64) string { return fmt.Sprintf("%.3f", float64(ms)/1000) }

// Build emits one complete, composition-aligned filter chain per requested
// sound path. Every chain covers every placement in order and inserts silence
// where a placement contributes no take to that path, so all stems and the
// review mix share the picture's exact duration and time relationship.
//
// inputs maps a frozen source id to its FFmpeg input index. The returned map
// goes from role to the stage label holding that path's stereo audio.
func Build(plan domain.Plan, roles []string, inputs map[string]int, rate int) ([]string, map[string]string, error) {
	statements := []string{}
	labels := map[string]string{}
	// A source may be consumed by no path at all (all takes routed to other
	// roles); such an input is never opened as a filter source.
	for _, role := range roles {
		segments := make([]string, 0, len(plan.Placements))
		names := make([]string, 0, len(plan.Placements))
		for _, p := range plan.Placements {
			name := fmt.Sprintf("[%s_%d]", role, len(segments))
			names = append(names, name)
			segment, err := pathSegment(role, p, inputs, rate, name)
			if err != nil {
				return nil, nil, err
			}
			segments = append(segments, segment)
		}
		if len(segments) == 0 {
			return nil, nil, fmt.Errorf("empty plan has no placements")
		}
		statements = append(statements, segments...)
		label := fmt.Sprintf("[path_%s]", role)
		if len(names) > 1 {
			statements = append(statements, fmt.Sprintf("%sconcat=n=%d:v=0:a=1%s", strings.Join(names, ""), len(names), label))
		} else {
			statements = append(statements, fmt.Sprintf("%sanull%s", names[0], label))
		}
		labels[role] = label
	}
	return statements, labels, nil
}

// pathSegment renders one placement's contribution to one path: a trimmed,
// resampled, downmixed take at authored gain, or exact-length silence.
func pathSegment(role string, p domain.Placement, inputs map[string]int, rate int, name string) (string, error) {
	duration := p.OutputOutMS - p.OutputInMS
	if p.AudioMissing {
		return fmt.Sprintf("anullsrc=r=%d:cl=stereo,atrim=duration=%s,asetpts=PTS-STARTPTS%s", rate, Seconds(duration), name), nil
	}
	for _, take := range p.AudioTakes {
		if domain.NormalizeAudioRole(take.Role) != role {
			continue
		}
		index, ok := inputs[p.SourceID]
		if !ok {
			return "", fmt.Errorf("source %s object missing", p.SourceID)
		}
		return fmt.Sprintf("[%d:a:%d]atrim=start=%s:end=%s,asetpts=PTS-STARTPTS,aresample=%d,aformat=sample_fmts=fltp:channel_layouts=stereo,volume=%.3fdB%s", index, take.Stream, Seconds(p.SourceInMS), Seconds(p.SourceOutMS), rate, p.GainDB, name), nil
	}
	return fmt.Sprintf("anullsrc=r=%d:cl=stereo,atrim=duration=%s,asetpts=PTS-STARTPTS%s", rate, Seconds(duration), name), nil
}

// PlansWithoutAudio reports whether no placement contributes a real take.
func PlansWithoutAudio(plan domain.Plan) bool {
	for _, p := range plan.Placements {
		if !p.AudioMissing && len(p.AudioTakes) > 0 {
			return false
		}
	}
	return true
}
