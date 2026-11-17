package encoding

import (
	"cadenceforge/internal/blob"
	"cadenceforge/internal/domain"
	"cadenceforge/internal/media"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type Runner struct {
	Binary string
	Blobs  *blob.Store
}

// Encode executes the frozen render inside its staging directory. It returns
// the delivered media file name; stems and captions land beside it.
func (r Runner) Encode(ctx context.Context, render domain.Render, stage string) (string, error) {
	paths := map[string]string{}
	for id, s := range render.Sources {
		path, err := r.Blobs.Object(s.Digest)
		if err != nil {
			return "", err
		}
		if _, err = os.Stat(path); err != nil {
			return "", fmt.Errorf("source %s unavailable: %w", id, err)
		}
		paths[id] = path
	}
	name := "composition." + render.Recipe.Format
	output, err := Arguments(render, paths, stage, name)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	if _, err = media.Run(ctx, r.Binary, output.Arguments, 65536); err != nil {
		return "", err
	}
	if err = os.WriteFile(filepath.Join(stage, "captions.vtt"), []byte(render.Plan.VTT), 0600); err != nil {
		return "", err
	}
	return name, nil
}
