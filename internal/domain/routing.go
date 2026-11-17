package domain

import (
	"cadenceforge/internal/routing"
	"fmt"
	"math"
	"sort"
	"strings"
)

// Independent sound path roles. Program is the historical main sound;
// reference and field are paths that must remain separately processable.
const (
	RoleProgram   = "program"
	RoleField     = "field"
	RoleReference = "reference"

	PolicyIntelligible = "intelligible"
	PolicyFullSum      = "full-sum"
	PolicyAnchorOnly   = "anchor-only"
)

var audioRoleOrder = []string{RoleProgram, RoleField, RoleReference}

func KnownAudioRole(role string) bool {
	for _, r := range audioRoleOrder {
		if r == role {
			return true
		}
	}
	return false
}

// NormalizeAudioRole maps an omitted role to the historical program path.
func NormalizeAudioRole(role string) string {
	r := strings.ToLower(strings.TrimSpace(role))
	if r == "" {
		return RoleProgram
	}
	return r
}

// PathLevel is a real measurement of one assembled sound path.
type PathLevel struct {
	RMSDB  *float64
	PeakDB *float64
	Silent bool
}

// PathCorrelation is the lag-zero correlation of one path against the review
// anchor, measured per delivered channel.
type PathCorrelation struct {
	Overall *float64
	Left    *float64
	Right   *float64
}

// RoutingMeasurement carries real-audio results for the assembled paths. Keys
// are roles; correlation keys come from CorrelationKey.
type RoutingMeasurement struct {
	WindowMS     int64
	Measured     bool
	Levels       map[string]PathLevel
	Correlations map[string]PathCorrelation
}

func CorrelationKey(a, b string) string {
	if a > b {
		a, b = b, a
	}
	return a + "\x00" + b
}

func (m RoutingMeasurement) Correlation(a, b string) PathCorrelation {
	if a == b {
		one := 1.0
		return PathCorrelation{Overall: &one, Left: &one, Right: &one}
	}
	return m.Correlations[CorrelationKey(a, b)]
}

const (
	reviewPolarityThreshold = -0.25
	reviewDuckDB            = -6.0
	reviewLimit             = 0.891
	reviewQuietAnchorDB     = -45.0
)

// ReviewLimit is exported so the render graph applies the exact peak ceiling
// the decision records.
const ReviewLimit = reviewLimit

// DecideRouting turns frozen placements plus real measurements into the routing
// record rendering executes. Identity and timing come from the plan; phase and
// level facts come from the supplied measurement. When measurement was not
// performed, levels and correlations stay nil instead of being guessed.
func DecideRouting(plan Plan, m RoutingMeasurement) (*RoutingDecision, error) {
	recipe := plan.Recipe
	policy := recipe.AudioReviewPolicy
	if policy == "" {
		policy = PolicyIntelligible
	}
	switch policy {
	case PolicyIntelligible, PolicyFullSum, PolicyAnchorOnly:
	default:
		return nil, Invalid("audio_review_policy must be intelligible, full-sum or anchor-only")
	}
	if recipe.AudioReviewAnchor != "" && !KnownAudioRole(recipe.AudioReviewAnchor) {
		return nil, Invalid("audio_review_anchor must be program, field or reference")
	}

	paths := map[string]*RoutePath{}
	order := []string{}
	for _, p := range plan.Placements {
		roles := map[string]bool{}
		for _, take := range p.AudioTakes {
			roles[NormalizeAudioRole(take.Role)] = true
		}
		for role := range roles {
			path, ok := paths[role]
			if !ok {
				path = &RoutePath{Role: role, Layout: "stereo", Channels: 2, ReviewPolarity: "normal"}
				paths[role] = path
				order = append(order, role)
			}
			path.Placements++
		}
		for _, take := range p.AudioTakes {
			role := NormalizeAudioRole(take.Role)
			path := paths[role]
			path.Takes++
			path.Streams = appendUniqueInt(path.Streams, take.Stream)
			path.SourceIDs = appendUniqueString(path.SourceIDs, p.SourceID)
			if p.AudioMissing {
				path.SilentPlacements++
			}
		}
	}
	if len(order) == 0 {
		return nil, Invalid("routing requires at least one audio take")
	}
	sort.SliceStable(order, func(i, j int) bool { return roleRank(order[i]) < roleRank(order[j]) })
	anchor := chooseReviewAnchor(order, recipe.AudioReviewAnchor, m)
	if anchor == "" {
		return nil, Invalid("audio_review_anchor is not present in the sheet")
	}

	multi := len(order) > 1
	decision := &RoutingDecision{Schema: 1, Mode: "single-path", Policy: policy, AnchorRole: anchor, WindowMS: m.WindowMS, Paths: []RoutePath{}}
	if multi {
		decision.Mode = "multi-path"
	}
	included := 0
	audible := 0
	corrected := 0
	for _, role := range order {
		path := paths[role]
		level := m.Levels[role]
		// A path built entirely from unpadded silence is known silent by plan
		// construction. Otherwise silence is asserted only from a real
		// measurement; an unmeasured path is never assumed silent.
		knownSilent := path.SilentPlacements == path.Placements
		silent := knownSilent || (m.Measured && (level.Silent || level.RMSDB == nil))
		path.RMSDB = level.RMSDB
		path.PeakDB = level.PeakDB
		path.Silent = silent
		path.Measured = m.Measured
		if !silent {
			audible++
		}
		corr := m.Correlation(role, anchor)
		if role != anchor {
			path.Correlation = corr.Overall
			path.LeftCorrelation = corr.Left
			path.RightCorrelation = corr.Right
		}
		path.ReviewIncluded = false
		path.ReviewGainDB = 0
		path.ReviewPolarity = "normal"
		if knownSilent {
			path.Notes = append(path.Notes, "every placement is unpadded silence in the frozen plan")
		}
		switch policy {
		case PolicyAnchorOnly:
			path.ReviewIncluded = role == anchor && !silent
		default:
			path.ReviewIncluded = !silent
		}
		if path.ReviewIncluded {
			included++
			if policy == PolicyIntelligible && role != anchor {
				path.ReviewGainDB = reviewDuckDB
			}
			if role != anchor && m.Measured {
				left, right := reviewPolarity(corr.Left), reviewPolarity(corr.Right)
				switch {
				case left < 0 && right < 0:
					path.ReviewPolarity = "both-inverted"
				case left < 0:
					path.ReviewPolarity = "left-inverted"
				case right < 0:
					path.ReviewPolarity = "right-inverted"
				}
				if path.ReviewPolarity != "normal" {
					corrected++
				}
			}
		}
		if multi {
			path.StemFile = "stem-" + role + ".wav"
		}
		if silent && m.Measured && path.SilentPlacements != path.Placements {
			path.Notes = append(path.Notes, "silent across the measured composition window")
		}
		decision.Paths = append(decision.Paths, *path)
	}
	anchorLevel := m.Levels[anchor]
	if anchorLevel.RMSDB != nil && *anchorLevel.RMSDB < reviewQuietAnchorDB && included > 0 {
		decision.LossNotes = append(decision.LossNotes, fmt.Sprintf("review anchor %s measures %.1f dBFS RMS; the two-channel review may be faint even though every stem is preserved", anchor, *anchorLevel.RMSDB))
	}

	if multi && included > 1 {
		decision.LossNotes = append(decision.LossNotes, fmt.Sprintf("%d audible paths feed one two-channel review; per-path separation survives only in the WAV stems", included))
	}
	if policy == PolicyIntelligible && multi && included > 1 {
		decision.LossNotes = append(decision.LossNotes, fmt.Sprintf("non-anchor paths are attenuated %.1f dB in the review to keep %s intelligible; stems keep authored gain", reviewDuckDB, anchor))
	}
	if corrected > 0 {
		decision.LossNotes = append(decision.LossNotes, fmt.Sprintf("review polarity of %d conflicting path(s) is corrected against %s so key content does not cancel; stems preserve original polarity", corrected, anchor))
	}
	if policy == PolicyFullSum && included > 1 {
		decision.LossNotes = append(decision.LossNotes, fmt.Sprintf("all audible paths are summed at authored gain in review and peak-limited at %.1f dBFS; quieter paths may be masked", 20*math.Log10(reviewLimit)))
	}
	if audible > 2 && corrected > 0 {
		decision.LossNotes = append(decision.LossNotes, fmt.Sprintf("%d independent paths with conflicting polarity cannot remain both fully separated and clearly audible through a fixed two-channel sum; this decision keeps every path in stems and favours %s in review", audible, anchor))
	}
	if policy == PolicyAnchorOnly && multi {
		decision.LossNotes = append(decision.LossNotes, fmt.Sprintf("anchor-only review carries %s alone; every other audible path is review-only absent and survives solely as a stem", anchor))
	}
	if multi {
		for _, path := range decision.Paths {
			if !path.ReviewIncluded {
				reason := "policy"
				if path.Silent {
					reason = "silent"
				} else if path.ReviewPolarity != "normal" {
					reason = "polarity conflict"
				}
				decision.LossNotes = append(decision.LossNotes, fmt.Sprintf("path %s is absent from the review mix (%s); its stem preserves the path", path.Role, reason))
			}
		}
	}
	if m.Measured {
		decision.LossNotes = append(decision.LossNotes, fmt.Sprintf("routing was measured over a %d ms window of the assembled composition at %d Hz; it is a routing measurement, not perceptual approval", m.WindowMS, routing.AnalysisRate))
	} else {
		decision.LossNotes = append(decision.LossNotes, "this routing record carries frozen stream identity and timing only; levels, phase and review treatment were not measured here")
	}
	return decision, nil
}

// PlanAudioRoles returns the sound paths a plan records, in stable role order.
func PlanAudioRoles(plan Plan) []string {
	seen := map[string]bool{}
	roles := []string{}
	for _, p := range plan.Placements {
		for _, take := range p.AudioTakes {
			role := NormalizeAudioRole(take.Role)
			if !seen[role] {
				seen[role] = true
				roles = append(roles, role)
			}
		}
	}
	sort.SliceStable(roles, func(i, j int) bool { return roleRank(roles[i]) < roleRank(roles[j]) })
	return roles
}

func chooseReviewAnchor(order []string, requested string, m RoutingMeasurement) string {
	find := func(role string) bool {
		for _, r := range order {
			if r == role {
				return true
			}
		}
		return false
	}
	if requested != "" {
		if find(requested) {
			return requested
		}
		return ""
	}
	for _, role := range audioRoleOrder {
		if find(role) {
			level := m.Levels[role]
			if !level.Silent && level.RMSDB != nil {
				return role
			}
		}
	}
	return order[0]
}

// reviewPolarity returns -1 when the measured correlation is clearly negative.
func reviewPolarity(value *float64) float64 {
	if value != nil && *value < reviewPolarityThreshold {
		return -1
	}
	return 1
}

func roleRank(role string) int {
	for i, r := range audioRoleOrder {
		if r == role {
			return i
		}
	}
	return len(audioRoleOrder)
}

func appendUniqueString(values []string, value string) []string {
	for _, v := range values {
		if v == value {
			return values
		}
	}
	return append(values, value)
}
func appendUniqueInt(values []int, value int) []int {
	for _, v := range values {
		if v == value {
			return values
		}
	}
	return append(values, value)
}
