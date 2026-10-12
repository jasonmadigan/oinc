package addons

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jasonmadigan/oinc/pkg/download"
)

// fails the first $HELM_TEST_FAILS pulls the way helm reports a 504, then
// writes an archive. every call is logged, one line each.
const flakyHelm = `#!/bin/sh
set -eu
printf '%s\n' "$*" >> "$HELM_TEST_LOG"
if [ "$1" = pull ]; then
  if [ "$(grep -c '^pull ' "$HELM_TEST_LOG")" -le "${HELM_TEST_FAILS:-0}" ]; then
    echo "Error: failed to fetch $2 : 504 Gateway Time-out" >&2
    exit 1
  fi
  while [ "$1" != --destination ]; do shift; done
  touch "$2/chart-1.0.0.tgz"
fi
`

// fakeHelm puts the script first on PATH and returns its call log.
func fakeHelm(t *testing.T, script string) string {
	t.Helper()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "helm"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(bin, "calls")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HELM_TEST_LOG", log)
	return log
}

func helmCalls(t *testing.T, log string) []string {
	t.Helper()
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

// noDownloadDelays makes download retries immediate.
func noDownloadDelays(t *testing.T) {
	t.Helper()
	saved := download.Delays
	download.Delays = make([]time.Duration, len(saved))
	t.Cleanup(func() { download.Delays = saved })
}

// isolatedTempDir points TMPDIR at an empty directory so leaked chart
// directories are visible.
func isolatedTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	return dir
}

func assertEmptyDir(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Errorf("temporary chart files leaked: %v, %v", entries, err)
	}
}

func TestPullChartRetriesFailedDownloads(t *testing.T) {
	noDownloadDelays(t)
	log := fakeHelm(t, flakyHelm)
	t.Setenv("HELM_TEST_FAILS", "2")
	var logs bytes.Buffer
	cfg := &Config{Logger: slog.New(slog.NewTextHandler(&logs, nil))}

	dir, archive, err := pullChart(context.Background(), cfg, "https://example.com/chart-1.0.0.tgz", "--version", "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	if archive != filepath.Join(dir, "chart-1.0.0.tgz") {
		t.Errorf("archive = %s, want the download in %s", archive, dir)
	}
	want := "pull https://example.com/chart-1.0.0.tgz --destination " + dir + " --version 1.0.0"
	calls := helmCalls(t, log)
	if len(calls) != 3 {
		t.Fatalf("expected two retries, got calls %q", calls)
	}
	for _, call := range calls {
		if call != want {
			t.Errorf("call = %q, want %q", call, want)
		}
	}
	if !strings.Contains(logs.String(), "attempt=2") || !strings.Contains(logs.String(), "504 Gateway Time-out") {
		t.Errorf("retries must be visible with the helm error: %s", logs.String())
	}
}

func TestPullChartGivesUpAfterRetries(t *testing.T) {
	noDownloadDelays(t)
	log := fakeHelm(t, flakyHelm)
	t.Setenv("HELM_TEST_FAILS", "100")
	tmp := isolatedTempDir(t)
	cfg := &Config{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}

	_, _, err := pullChart(context.Background(), cfg, "https://example.com/chart-1.0.0.tgz")
	if err == nil || !strings.Contains(err.Error(), "504 Gateway Time-out") {
		t.Fatalf("expected the last helm error, got %v", err)
	}
	if calls := helmCalls(t, log); len(calls) != len(download.Delays)+1 {
		t.Errorf("expected %d attempts, got %q", len(download.Delays)+1, calls)
	}
	assertEmptyDir(t, tmp)
}

// a transient failure must not fail the addon, and the install must use the
// downloaded archive so helm never fetches the chart again.
func TestHelmAddonsInstallDownloadedChart(t *testing.T) {
	noDownloadDelays(t)
	for _, tc := range []struct {
		name    string
		install func(context.Context, *Config) error
		chart   string
		args    []string
		release string
	}{
		{
			name:    "istio",
			install: (&istio{}).Install,
			chart:   "https://github.com/istio-ecosystem/sail-operator/releases/download/1.29.0/sail-operator-1.29.0.tgz",
			release: "sail-operator",
		},
		{
			name:    "kuadrant",
			install: (&kuadrant{version: "1.4.1"}).Install,
			chart:   "kuadrant-operator",
			args:    []string{"--repo", kuadrantChartRepoURL, "--version", "1.4.1"},
			release: "kuadrant-operator",
		},
		{
			name:    "rhdh",
			install: (&rhdh{version: "6.2.2"}).Install,
			chart:   "backstage",
			args:    []string{"--repo", rhdhChartRepoURL, "--version", "6.2.2"},
			release: "rhdh",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			log := fakeHelm(t, flakyHelm)
			t.Setenv("HELM_TEST_FAILS", "1")
			tmp := isolatedTempDir(t)
			cfg := &Config{
				Logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
				IngressHost:     "127.0.0.1.nip.io",
				IngressHTTPPort: 9080,
			}
			if err := tc.install(context.Background(), cfg); err != nil {
				t.Fatal(err)
			}

			calls := helmCalls(t, log)
			if len(calls) != 3 {
				t.Fatalf("expected a retried pull then one install, got %q", calls)
			}
			pull := strings.Fields(calls[1])
			if len(pull) < 4 || pull[0] != "pull" || pull[1] != tc.chart || pull[2] != "--destination" {
				t.Fatalf("unexpected pull: %q", calls[1])
			}
			if got := strings.Join(pull[4:], " "); got != strings.Join(tc.args, " ") {
				t.Errorf("pull options = %q, want %q", got, strings.Join(tc.args, " "))
			}
			install := "upgrade --install " + tc.release + " " + filepath.Join(pull[3], "chart-1.0.0.tgz") + " "
			if !strings.HasPrefix(calls[2], install) {
				t.Errorf("install = %q, want prefix %q", calls[2], install)
			}
			if strings.Contains(calls[2], "--version") {
				t.Errorf("install must not resolve a chart version: %q", calls[2])
			}
			assertEmptyDir(t, tmp)
		})
	}
}
