package domain

import (
	"fmt"
	"math"
	"strings"
	"unicode/utf8"
)

func ValidateRecipe(r Recipe) error {
	if strings.TrimSpace(r.Name) == "" || len(r.Name) > 160 {
		return Invalid("recipe name must contain 1..160 bytes")
	}
	if r.Format != "mp4" && r.Format != "wav" {
		return Invalid("format must be mp4 or wav")
	}
	if r.SampleRate != 48000 && r.SampleRate != 44100 {
		return Invalid("sample_rate must be 44100 or 48000")
	}
	if r.Format == "mp4" {
		if r.Width < 64 || r.Height < 64 || r.Width > 1920 || r.Height > 1080 || r.Width%2 != 0 || r.Height%2 != 0 {
			return Invalid("invalid even video dimensions")
		}
		if r.FPS < 1 || r.FPS > 60 {
			return Invalid("fps must be 1..60")
		}
	}
	if r.Normalize && (math.IsNaN(r.TargetLUFS) || math.IsInf(r.TargetLUFS, 0) || r.TargetLUFS < -36 || r.TargetLUFS > -9) {
		return Invalid("normalization target must be -36..-9 LUFS")
	}
	policy := r.AudioReviewPolicy
	if policy != "" && policy != PolicyIntelligible && policy != PolicyFullSum && policy != PolicyAnchorOnly {
		return Invalid("audio_review_policy must be intelligible, full-sum or anchor-only")
	}
	if r.AudioReviewAnchor != "" && !KnownAudioRole(r.AudioReviewAnchor) {
		return Invalid("audio_review_anchor must be program, field or reference")
	}
	return nil
}
func ValidateSheet(s Sheet, sources map[string]Source) error {
	if strings.TrimSpace(s.Name) == "" || len(s.Name) > 160 {
		return Invalid("sheet name must contain 1..160 bytes")
	}
	if len(s.Spans) == 0 || len(s.Spans) > 32 {
		return Invalid("a sheet requires 1..32 spans")
	}
	var duration int64
	for i, p := range s.Spans {
		source, ok := sources[p.SourceID]
		if !ok {
			return Missing("source", p.SourceID)
		}
		if p.InMS < 0 || p.OutMS <= p.InMS || p.OutMS > source.Media.DurationMS {
			return Invalid("span %d is outside source duration", i)
		}
		if p.OutMS-p.InMS < 100 {
			return Invalid("span %d is shorter than 100ms", i)
		}
		if math.IsNaN(p.GainDB) || math.IsInf(p.GainDB, 0) || p.GainDB < -60 || p.GainDB > 12 {
			return Invalid("span %d has invalid gain", i)
		}
		roles := map[string]bool{}
		streams := map[int]bool{}
		for j, take := range p.AudioTakes {
			role := NormalizeAudioRole(take.Role)
			if !KnownAudioRole(role) {
				return Invalid("span %d take %d has unknown audio role", i, j)
			}
			if roles[role] {
				return Invalid("span %d routes audio role %s twice", i, role)
			}
			roles[role] = true
			if streams[take.Stream] {
				return Invalid("span %d routes audio stream %d twice", i, take.Stream)
			}
			streams[take.Stream] = true
			if take.Stream < 0 {
				return Invalid("span %d take %d has invalid audio stream", i, j)
			}
			if _, ok := source.Media.AudioStreamAt(take.Stream); !ok {
				return Invalid("span %d take %d references audio stream %d beyond probed streams", i, j, take.Stream)
			}
		}
		duration += p.OutMS - p.InMS
	}
	if duration > 120000 {
		return Invalid("sheet exceeds 120 seconds")
	}
	if len(s.Cues) > 500 {
		return Invalid("sheet exceeds 500 cues")
	}
	for i, c := range s.Cues {
		source, ok := sources[c.SourceID]
		if !ok {
			return Missing("cue source", c.SourceID)
		}
		if c.StartMS < 0 || c.EndMS <= c.StartMS || c.EndMS > source.Media.DurationMS {
			return Invalid("cue %d has invalid interval", i)
		}
		if !utf8.ValidString(c.Text) || strings.TrimSpace(c.Text) == "" || len(c.Text) > 2000 || strings.Contains(c.Text, "-->") || strings.ContainsAny(c.Text, "\x00\r") {
			return Invalid("cue %d has invalid text", i)
		}
	}
	return nil
}

// ValidateRouting checks that a frozen routing record is internally coherent
// and consistent with the timeline it describes. It is applied when state is
// loaded and when a render plan is persisted, so a corrupt decision cannot be
// executed silently.
func ValidateRouting(plan Plan) error {
	r := plan.Routing
	if r == nil {
		return nil
	}
	if r.Schema != 1 {
		return fmt.Errorf("unsupported routing schema")
	}
	if r.Mode != "single-path" && r.Mode != "multi-path" {
		return fmt.Errorf("invalid routing mode")
	}
	switch r.Policy {
	case PolicyIntelligible, PolicyFullSum, PolicyAnchorOnly:
	default:
		return fmt.Errorf("invalid routing policy")
	}
	if !KnownAudioRole(r.AnchorRole) {
		return fmt.Errorf("invalid routing anchor")
	}
	if r.WindowMS < 0 || r.WindowMS > plan.DurationMS {
		return fmt.Errorf("routing window exceeds plan duration")
	}
	if len(r.Paths) == 0 {
		return fmt.Errorf("routing records no paths")
	}
	seen := map[string]bool{}
	anchorSeen := false
	for _, path := range r.Paths {
		if !KnownAudioRole(path.Role) || seen[path.Role] {
			return fmt.Errorf("invalid or duplicate routing path")
		}
		seen[path.Role] = true
		if path.Role == r.AnchorRole {
			anchorSeen = true
		}
		if path.Placements <= 0 || path.Takes < 0 || path.SilentPlacements < 0 || path.SilentPlacements > path.Placements {
			return fmt.Errorf("invalid routing path placement counts")
		}
		if path.Channels < 1 || path.Channels > 8 {
			return fmt.Errorf("invalid routing path channels")
		}
		switch path.ReviewPolarity {
		case "normal", "left-inverted", "right-inverted", "both-inverted":
		default:
			return fmt.Errorf("invalid review polarity")
		}
		if path.ReviewIncluded && path.ReviewGainDB < -60 {
			return fmt.Errorf("invalid review gain")
		}
		if (r.Mode == "single-path") != (len(r.Paths) == 1) {
			return fmt.Errorf("routing mode disagrees with path count")
		}
	}
	if !anchorSeen {
		return fmt.Errorf("routing anchor has no path")
	}
	if r.Mode == "single-path" {
		if r.Paths[0].Role != r.AnchorRole {
			return fmt.Errorf("single-path routing anchors a different path")
		}
		if r.Paths[0].StemFile != "" {
			return fmt.Errorf("single-path routing must not claim stems")
		}
	}
	return nil
}
func ValidateState(s State) error {
	if s.Schema != 1 || s.Sources == nil || s.Sheets == nil || s.Recipes == nil || s.Renders == nil {
		return fmt.Errorf("unsupported or incomplete state schema")
	}
	for id, source := range s.Sources {
		if source.ID != id || len(source.Digest) != 64 || source.Bytes <= 0 {
			return fmt.Errorf("invalid source %s", id)
		}
	}
	for id, versions := range s.Sheets {
		if len(versions) == 0 {
			return fmt.Errorf("empty sheet history")
		}
		for i, v := range versions {
			if v.ID != id || v.Revision != i+1 {
				return fmt.Errorf("invalid sheet history")
			}
			if err := ValidateSheet(v, s.Sources); err != nil {
				return err
			}
		}
	}
	for id, versions := range s.Recipes {
		if len(versions) == 0 {
			return fmt.Errorf("empty recipe history")
		}
		for i, v := range versions {
			if v.ID != id || v.Revision != i+1 {
				return fmt.Errorf("invalid recipe history")
			}
			if err := ValidateRecipe(v); err != nil {
				return err
			}
		}
	}
	keys := map[string]bool{}
	for id, r := range s.Renders {
		if id != r.ID || keys[r.Key] {
			return fmt.Errorf("invalid render identity")
		}
		keys[r.Key] = true
		switch r.State {
		case "queued", "running", "succeeded", "failed", "cancelled":
		default:
			return fmt.Errorf("invalid render state")
		}
		if (r.State == "succeeded") != (r.Artifact != nil) {
			return fmt.Errorf("artifact state mismatch")
		}
		if r.Sources == nil {
			return fmt.Errorf("missing render source snapshot")
		}
		if err := ValidateSheet(r.Sheet, r.Sources); err != nil {
			return err
		}
		if err := ValidateRecipe(r.Recipe); err != nil {
			return err
		}
		if err := ValidateRouting(r.Plan); err != nil {
			return err
		}
	}
	var previous uint64
	for _, e := range s.Events {
		if e.Sequence <= previous || e.Sequence > s.Sequence {
			return fmt.Errorf("event sequence is invalid")
		}
		previous = e.Sequence
	}
	return nil
}
