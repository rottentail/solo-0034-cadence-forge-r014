package media

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
)

const Formats = "mov,matroska,webm,wav,mp3,flac,ogg,avi,mpegts,aac"

type LimitedBuffer struct {
	bytes.Buffer
	Limit int
}

func (b *LimitedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	remaining := b.Limit - b.Len()
	if remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		b.Buffer.Write(p)
	}
	return n, nil
}
func Run(ctx context.Context, binary string, args []string, limit int) ([]byte, error) {
	cmd := exec.CommandContext(ctx, binary, args...)
	var out, errout LimitedBuffer
	out.Limit = limit
	errout.Limit = 8192
	cmd.Stdout = &out
	cmd.Stderr = &errout
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("media process: %w: %s", err, errout.String())
	}
	if out.Len() >= limit {
		return nil, fmt.Errorf("media metadata exceeds limit")
	}
	return out.Bytes(), nil
}

// RunReport runs a media tool whose human-readable report is written to stderr
// (FFmpeg's astats and loudnorm summaries are) and returns that report. A
// non-zero exit still returns the captured report alongside the error so a
// failed pass can be explained.
func RunReport(ctx context.Context, binary string, args []string, limit int) (string, error) {
	cmd := exec.CommandContext(ctx, binary, args...)
	var out, errout LimitedBuffer
	out.Limit = limit
	errout.Limit = limit
	cmd.Stdout = &out
	cmd.Stderr = &errout
	err := cmd.Run()
	if err != nil {
		return errout.String(), fmt.Errorf("media process: %w: %s", err, errout.String())
	}
	return errout.String(), nil
}
