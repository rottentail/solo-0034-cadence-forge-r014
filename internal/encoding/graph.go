package encoding

import (
	"cadenceforge/internal/audiograph"
	"cadenceforge/internal/domain"
	"cadenceforge/internal/media"
	"fmt"
	"path/filepath"
	"strings"
)

func seconds(ms int64) string { return audiograph.Seconds(ms) }

// audioMissing reports whether no placement carries a real audio take.
func audioMissing(plan domain.Plan) bool {
	for _, p := range plan.Placements {
		if !p.AudioMissing && len(p.AudioTakes) > 0 {
			return false
		}
	}
	return true
}

// Output names the files one encode produces: the delivered media, the sidecar
// captions and each preserved path stem, all in the same staging directory.
type Output struct {
	Arguments []string
	Media     string
	Stems     []Stem
}

type Stem struct {
	Role string
	File string
}

// Arguments builds the real FFmpeg invocation for a frozen render. When the
// plan records independent sound paths, the review mix is assembled exactly as
// the recorded routing decision describes, and every path is also written as
// its own PCM stem. Both deliverables share one source identity and one time
// base because both come from the same frozen plan.
func Arguments(render domain.Render, paths map[string]string, stage, name string) (Output, error) {
	r := render.Recipe
	mediaOut := filepath.Join(stage, name)
	args := []string{"-nostdin", "-y", "-hide_banner", "-loglevel", "error", "-filter_complex_threads", "1"}
	inputs := map[string]int{}
	for _, p := range render.Plan.Placements {
		if _, ok := inputs[p.SourceID]; ok {
			continue
		}
		path, ok := paths[p.SourceID]
		if !ok {
			return Output{}, fmt.Errorf("source object missing")
		}
		inputs[p.SourceID] = len(inputs)
		args = append(args, "-protocol_whitelist", "file,pipe", "-format_whitelist", media.Formats, "-i", path)
	}
	if len(inputs) == 0 {
		return Output{}, fmt.Errorf("empty render")
	}
	routing := render.Plan.Routing
	if routing == nil {
		// A frozen plan written before routing existed still renders: its spans
		// are the historical program path and this render claims no stems.
		routing = &domain.RoutingDecision{Schema: 1, Mode: "single-path", Policy: domain.PolicyIntelligible, AnchorRole: domain.RoleProgram, Paths: []domain.RoutePath{{Role: domain.RoleProgram, ReviewIncluded: true, ReviewPolarity: "normal"}}}
	}
	// Build only paths this render actually consumes: a path that is both
	// absent from review and delivered as no stem would leave an unconnected
	// pad. A sheet with no usable audio keeps the historical silent program.
	roles := []string{}
	for _, path := range routing.Paths {
		if path.ReviewIncluded || path.StemFile != "" {
			roles = append(roles, path.Role)
		}
	}
	if len(roles) == 0 && !audioMissing(render.Plan) {
		return Output{}, fmt.Errorf("routing decision consumes no sound path")
	}
	filters := []string{}
	video := []string{}
	if r.Format == "mp4" {
		for i, p := range render.Plan.Placements {
			filters = append(filters, fmt.Sprintf("[%d:v:0]trim=start=%s:end=%s,setpts=PTS-STARTPTS,scale=%d:%d:force_original_aspect_ratio=decrease,pad=%d:%d:(ow-iw)/2:(oh-ih)/2,setsar=1,fps=%d,format=yuv420p[v%d]", inputs[p.SourceID], seconds(p.SourceInMS), seconds(p.SourceOutMS), r.Width, r.Height, r.Width, r.Height, r.FPS, i))
			video = append(video, fmt.Sprintf("[v%d]", i))
		}
	}
	audioStmts, labels, err := audiograph.Build(render.Plan, roles, inputs, r.SampleRate)
	if err != nil {
		return Output{}, err
	}
	filters = append(filters, audioStmts...)
	// A labelled pad can feed only one output, so a path that is both reviewed
	// and delivered as a stem is split. Review and stem therefore carry the
	// same assembled samples without sharing a pad.
	stemLabels := map[string]string{}
	for _, path := range routing.Paths {
		if path.StemFile == "" {
			continue
		}
		label, ok := labels[path.Role]
		if !ok {
			return Output{}, fmt.Errorf("routing stem %s has no assembled path", path.Role)
		}
		stem := fmt.Sprintf("[stem_%s]", path.Role)
		if !path.ReviewIncluded {
			stemLabels[path.Role] = label
			continue
		}
		review := fmt.Sprintf("[mix_%s]", path.Role)
		filters = append(filters, fmt.Sprintf("%sasplit=2%s%s", label, stem, review))
		stemLabels[path.Role] = stem
		labels[path.Role] = review
	}
	review, err := reviewChain(routing, labels, r, render.Plan.DurationMS)
	if err != nil {
		return Output{}, err
	}
	filters = append(filters, review...)
	if r.Format == "mp4" {
		filters = append(filters, strings.Join(video, "")+fmt.Sprintf("concat=n=%d:v=1:a=0[vout]", len(video)))
	}
	if r.Normalize {
		filters = append(filters, fmt.Sprintf("[aout]loudnorm=I=%.2f:TP=-1.5:LRA=11,aresample=%d[aoutn]", r.TargetLUFS, r.SampleRate))
	} else {
		filters = append(filters, "[aout]anull[aoutn]")
	}
	args = append(args, "-filter_complex", strings.Join(filters, ";"))

	out := Output{Media: name}
	mediaArgs := []string{"-map", "[aoutn]"}
	if r.Format == "mp4" {
		mediaArgs = []string{"-map", "[vout]", "-map", "[aoutn]", "-c:v", "libx264", "-preset", "veryfast", "-crf", "23", "-threads", "1", "-c:a", "aac", "-b:a", "160k", "-movflags", "+faststart"}
	} else {
		mediaArgs = []string{"-map", "[aoutn]", "-c:a", "pcm_s16le"}
	}
	args = append(args, mediaArgs...)
	args = append(args, "-ar", fmt.Sprint(r.SampleRate), "-t", seconds(render.Plan.DurationMS), "-map_metadata", "-1", mediaOut)

	for _, path := range routing.Paths {
		if path.StemFile == "" {
			continue
		}
		label, ok := stemLabels[path.Role]
		if !ok {
			return Output{}, fmt.Errorf("routing stem %s has no assembled path", path.Role)
		}
		args = append(args, "-map", label, "-c:a", "pcm_s16le", "-ar", fmt.Sprint(r.SampleRate), "-ac", "2", "-t", seconds(render.Plan.DurationMS), "-map_metadata", "-1", filepath.Join(stage, path.StemFile))
		out.Stems = append(out.Stems, Stem{Role: path.Role, File: path.StemFile})
	}
	out.Arguments = args
	return out, nil
}

// reviewChain builds the two-channel review mix from the recorded decision. It
// applies recorded polarity and ducking only to the review, never to a stem.
func reviewChain(routing *domain.RoutingDecision, labels map[string]string, recipe domain.Recipe, durationMS int64) ([]string, error) {
	statements := []string{}
	inputs := []string{}
	for _, path := range routing.Paths {
		if !path.ReviewIncluded {
			continue
		}
		label, ok := labels[path.Role]
		if !ok {
			return nil, fmt.Errorf("routing review path %s has no assembled audio", path.Role)
		}
		name := fmt.Sprintf("[rev_%s]", path.Role)
		statement := label
		if path.ReviewGainDB != 0 {
			statement += fmt.Sprintf("volume=%.3fdB,", path.ReviewGainDB)
		}
		if path.ReviewPolarity != "normal" {
			statement += reviewPolarityFilter(path.ReviewPolarity) + ","
		}
		statements = append(statements, statement+"anull"+name)
		inputs = append(inputs, name)
	}
	if len(inputs) == 0 {
		// A sheet whose every path is silent still owes an equal-length silent
		// two-channel track, matching the historical silent-video contract.
		statements = append(statements, fmt.Sprintf("anullsrc=r=%d:cl=stereo,atrim=duration=%s,asetpts=PTS-STARTPTS[aout]", recipe.SampleRate, seconds(durationMS)))
		return statements, nil
	}
	if len(inputs) == 1 {
		statements = append(statements, inputs[0]+"anull[aout]")
		return statements, nil
	}
	statements = append(statements, fmt.Sprintf("%samix=inputs=%d:normalize=0:dropout_transition=0,alimiter=limit=%.4f:level=false[aout]", strings.Join(inputs, ""), len(inputs), domain.ReviewLimit))
	return statements, nil
}

func reviewPolarityFilter(polarity string) string {
	switch polarity {
	case "both-inverted":
		return "pan=stereo|c0=-1*c0|c1=-1*c1"
	case "left-inverted":
		return "pan=stereo|c0=-1*c0|c1=c1"
	case "right-inverted":
		return "pan=stereo|c0=c0|c1=-1*c1"
	}
	return "anull"
}
