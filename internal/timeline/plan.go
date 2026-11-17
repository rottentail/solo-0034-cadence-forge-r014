package timeline

import (
	"cadenceforge/internal/domain"
)

// Build turns a sheet revision and recipe revision into an immutable plan. It
// resolves each span's audio takes against the frozen probe facts in sources,
// so sound identity is fixed at submit time rather than discovered later.
func Build(sheet domain.Sheet, recipe domain.Recipe, sources map[string]domain.Source) (domain.Plan, error) {
	if err := domain.ValidateSheet(sheet, sources); err != nil {
		return domain.Plan{}, err
	}
	if err := domain.ValidateRecipe(recipe); err != nil {
		return domain.Plan{}, err
	}
	plan := domain.Plan{Recipe: recipe, Placements: []domain.Placement{}}
	for _, span := range sheet.Spans {
		source := sources[span.SourceID]
		if recipe.Format == "mp4" && !source.Media.Video {
			return plan, domain.Invalid("MP4 span requires video: %s", source.ID)
		}
		duration := span.OutMS - span.InMS
		placement := domain.Placement{SourceID: source.ID, SourceInMS: span.InMS, SourceOutMS: span.OutMS, OutputInMS: plan.DurationMS, OutputOutMS: plan.DurationMS + duration, GainDB: span.GainDB, AudioTakes: []domain.AudioTake{}}
		if !source.Media.Audio || source.Media.AudioStreamCount() == 0 {
			if recipe.Format == "wav" {
				return plan, domain.Invalid("WAV span requires audio: %s", source.ID)
			}
			if len(span.AudioTakes) > 0 {
				return plan, domain.Invalid("span routes audio from a source with no probed audio: %s", source.ID)
			}
			// A silent video span still contributes one program path of
			// equal-length silence, keeping the historical MP4 contract.
			placement.AudioMissing = true
			placement.AudioTakes = []domain.AudioTake{{Stream: 0, Role: domain.RoleProgram}}
			plan.Placements = append(plan.Placements, placement)
			plan.DurationMS += duration
			continue
		}
		takes := span.AudioTakes
		if len(takes) == 0 {
			takes = []domain.AudioTake{{Stream: 0, Role: domain.RoleProgram}}
		}
		for _, take := range takes {
			role := domain.NormalizeAudioRole(take.Role)
			stream, ok := source.Media.AudioStreamAt(take.Stream)
			if !ok {
				return plan, domain.Invalid("span references audio stream %d beyond probed streams: %s", take.Stream, source.ID)
			}
			placement.AudioTakes = append(placement.AudioTakes, domain.AudioTake{Stream: stream.Ordinal, Role: role})
		}
		plan.Placements = append(plan.Placements, placement)
		plan.DurationMS += duration
	}
	return plan, nil
}
