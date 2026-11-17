package service

import (
	"cadenceforge/internal/caption"
	"cadenceforge/internal/domain"
	"cadenceforge/internal/media"
	"cadenceforge/internal/timeline"
	"context"
	"fmt"
	"strings"
	"time"
)

type Request struct {
	SheetID        string `json:"sheet_id"`
	SheetRevision  int    `json:"sheet_revision"`
	RecipeID       string `json:"recipe_id"`
	RecipeRevision int    `json:"recipe_revision"`
}

// Submit freezes a sheet revision and recipe revision into a render. The
// expensive part is reading the frozen source selection; it happens before the
// state lock is taken, and the final update re-checks idempotency and capacity
// so the same decision is still made once.
func (s *Service) Submit(key string, request Request) (domain.Render, error) {
	if strings.TrimSpace(key) == "" || len(key) > 128 {
		return domain.Render{}, domain.Invalid("Idempotency-Key requires 1..128 bytes")
	}
	if request.SheetRevision < 1 || request.RecipeRevision < 1 {
		return domain.Render{}, domain.Invalid("explicit positive revisions are required")
	}
	fingerprint := hash(request)
	if existing, ok, err := s.existingRender(key, fingerprint); err != nil || ok {
		return existing, err
	}
	// Reject cheap, certain outcomes before paying for a routing measurement.
	if err := s.admit(request); err != nil {
		return domain.Render{}, err
	}
	sheet, recipe, sources, plan, err := s.freeze(request)
	if err != nil {
		return domain.Render{}, err
	}
	s.mutation.Lock()
	defer s.mutation.Unlock()
	var out domain.Render
	err = s.Store.Update(func(st *domain.State) error {
		for _, r := range st.Renders {
			if r.Key == key {
				if r.RequestHash != fingerprint {
					return domain.Conflict("idempotency key has different input")
				}
				out = r
				return nil
			}
		}
		pending := 0
		for _, r := range st.Renders {
			if r.State == "queued" || r.State == "running" {
				pending++
			}
		}
		if pending >= s.Config.QueueLimit {
			return domain.Capacity("render queue is full")
		}
		// Measurement ran outside the lock; confirm the facts it was based on
		// are still exactly the frozen ones. Revisions are append-only, but a
		// source may have been retired in the meantime.
		if request.SheetRevision > len(st.Sheets[request.SheetID]) {
			return domain.Conflict("sheet revision changed during submission")
		}
		if request.RecipeRevision > len(st.Recipes[request.RecipeID]) {
			return domain.Conflict("recipe revision changed during submission")
		}
		for id, frozen := range sources {
			current, ok := st.Sources[id]
			if !ok || current.Digest != frozen.Digest || current.Bytes != frozen.Bytes {
				return domain.Conflict("source facts changed during submission")
			}
		}
		now := time.Now().UTC()
		id := ID()
		out = domain.Render{ID: id, Key: key, RequestHash: fingerprint, Sheet: sheet, Recipe: recipe, Sources: sources, Plan: plan, State: "queued", Attempts: []domain.Attempt{}, CreatedAt: now, UpdatedAt: now}
		st.Renders[id] = out
		st.Emit(id, "render.queued", "frozen source, plan and audio routing")
		return nil
	})
	if err == nil {
		s.Notify()
	}
	return out, err
}

// admit performs the submission checks that need no measurement or media pass,
// so a missing revision or a full queue is reported without spending a routing
// measurement first. The final update repeats every decision atomically.
func (s *Service) admit(request Request) error {
	st, err := s.Store.Snapshot()
	if err != nil {
		return err
	}
	if request.SheetRevision > len(st.Sheets[request.SheetID]) {
		return domain.Missing("sheet revision", request.SheetID)
	}
	if request.RecipeRevision > len(st.Recipes[request.RecipeID]) {
		return domain.Missing("recipe revision", request.RecipeID)
	}
	pending := 0
	for _, r := range st.Renders {
		if r.State == "queued" || r.State == "running" {
			pending++
		}
	}
	if pending >= s.Config.QueueLimit {
		return domain.Capacity("render queue is full")
	}
	return nil
}

func (s *Service) existingRender(key, fingerprint string) (domain.Render, bool, error) {
	st, err := s.Store.Snapshot()
	if err != nil {
		return domain.Render{}, false, err
	}
	for _, r := range st.Renders {
		if r.Key != key {
			continue
		}
		if r.RequestHash != fingerprint {
			return domain.Render{}, false, domain.Conflict("idempotency key has different input")
		}
		return r, true, nil
	}
	return domain.Render{}, false, nil
}

// freeze resolves the exact revisions a render will use and builds the plan,
// including the recorded audio routing. Nothing here consults live probe state
// for stream identity; it reads the ingest facts frozen into each source.
func (s *Service) freeze(request Request) (domain.Sheet, domain.Recipe, map[string]domain.Source, domain.Plan, error) {
	st, err := s.Store.Snapshot()
	if err != nil {
		return domain.Sheet{}, domain.Recipe{}, nil, domain.Plan{}, err
	}
	sheets := st.Sheets[request.SheetID]
	recipes := st.Recipes[request.RecipeID]
	if request.SheetRevision > len(sheets) {
		return domain.Sheet{}, domain.Recipe{}, nil, domain.Plan{}, domain.Missing("sheet revision", request.SheetID)
	}
	if request.RecipeRevision > len(recipes) {
		return domain.Sheet{}, domain.Recipe{}, nil, domain.Plan{}, domain.Missing("recipe revision", request.RecipeID)
	}
	sheet := sheets[request.SheetRevision-1]
	recipe := recipes[request.RecipeRevision-1]
	sources := map[string]domain.Source{}
	for _, p := range sheet.Spans {
		sources[p.SourceID] = st.Sources[p.SourceID]
	}
	for _, c := range sheet.Cues {
		sources[c.SourceID] = st.Sources[c.SourceID]
	}
	plan, err := timeline.Build(sheet, recipe, sources)
	if err != nil {
		return domain.Sheet{}, domain.Recipe{}, nil, domain.Plan{}, err
	}
	plan.VTT = caption.VTT(caption.Map(sheet, plan))
	decision, err := s.route(context.Background(), plan, sources)
	if err != nil {
		return domain.Sheet{}, domain.Recipe{}, nil, domain.Plan{}, err
	}
	plan.Routing = decision
	if err = domain.ValidateRouting(plan); err != nil {
		return domain.Sheet{}, domain.Recipe{}, nil, domain.Plan{}, fmt.Errorf("routing decision: %w", err)
	}
	return sheet, recipe, sources, plan, nil
}

// route measures the assembled paths before a render exists. A single-path
// sheet cannot conflict, so it records identity without paying for a media
// pass; multiple paths are measured so phase conflicts are a frozen fact
// rather than a render-time guess.
func (s *Service) route(ctx context.Context, plan domain.Plan, sources map[string]domain.Source) (*domain.RoutingDecision, error) {
	if len(domain.PlanAudioRoles(plan)) <= 1 {
		return domain.DecideRouting(plan, domain.RoutingMeasurement{WindowMS: plan.DurationMS})
	}
	measurement, err := media.MeasureRouting(ctx, s.Config.FFmpeg, plan, func(sourceID string) (string, bool) {
		source, ok := sources[sourceID]
		if !ok {
			return "", false
		}
		path, err := s.Blobs.Object(source.Digest)
		return path, err == nil
	})
	if err != nil {
		return nil, err
	}
	return domain.DecideRouting(plan, measurement)
}
