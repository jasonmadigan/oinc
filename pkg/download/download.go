// Package download runs network fetches under one retry policy. Release
// hosts and registries return 5xx, 429 and connection resets for tens of
// seconds at a time, which neither helm nor curl's own retries ride out.
package download

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"time"
)

// Delays spaces retries after a failed attempt; tests shorten it.
var Delays = []time.Duration{2 * time.Second, 5 * time.Second, 10 * time.Second}

// Retry runs fetch until it succeeds, logging each failure, and returns the
// last error once Delays is spent. It stops as soon as ctx ends.
func Retry(ctx context.Context, logger *slog.Logger, target string, fetch func() error) error {
	for attempt := 0; ; attempt++ {
		err := fetch()
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if attempt == len(Delays) {
			return err
		}
		logger.Warn("download failed, retrying", "url", target, "attempt", attempt+1, "err", err)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(Delays[attempt]):
		}
	}
}

// URL fetches url with curl, as Go's net stack breaks in privileged
// containers on macOS. HTTP errors fail the attempt, so an error page is
// never returned as content.
func URL(ctx context.Context, logger *slog.Logger, url string) ([]byte, error) {
	if _, err := exec.LookPath("curl"); err != nil {
		return nil, fmt.Errorf("curl is required but not found in PATH")
	}
	var data []byte
	err := Retry(ctx, logger, url, func() error {
		var stderr bytes.Buffer
		cmd := exec.CommandContext(ctx, "curl", "-fsSL", "--max-time", "60", url)
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			return fmt.Errorf("%s: %w", strings.TrimSpace(stderr.String()), err)
		}
		data = out
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("fetching %s: %w", url, err)
	}
	return data, nil
}
