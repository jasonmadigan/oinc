package oinc

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jasonmadigan/oinc/pkg/addons"
)

// configureRHDH applies rhdh options for a test and resets them on cleanup so
// the shared registry singleton does not leak state between tests.
func configureRHDH(t *testing.T, opts map[string]string) {
	t.Helper()
	addons.Configure("rhdh", opts)
	t.Cleanup(func() {
		reset := map[string]string{}
		for k := range opts {
			reset[k] = ""
		}
		addons.Configure("rhdh", reset)
	})
}

func TestPreflightAddons(t *testing.T) {
	// the version pin case configures the shared cert-manager singleton
	t.Cleanup(func() {
		addons.Configure("cert-manager", map[string]string{"version": ""})
	})

	tests := []struct {
		name    string
		list    string
		wantErr string
	}{
		{"valid single", "cert-manager", ""},
		{"valid list with spaces", "cert-manager, gateway-api", ""},
		{"valid version pin", "cert-manager@1.16.0", ""},
		{"unknown addon", "nosuch", "unknown addon"},
		{"empty version after @", "cert-manager@", "empty version"},
		{"empty addon name", "@1.16.0", "empty addon name"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := PreflightAddons(tt.list)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("PreflightAddons(%q) = %v, want nil", tt.list, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("PreflightAddons(%q) = %v, want error containing %q", tt.list, err, tt.wantErr)
			}
		})
	}
}

// a bad values overlay must fail pre-flight with the addon name and the file
// path in the error, not minutes later in the install step.
func TestPreflightAddonsRHDHMissingValues(t *testing.T) {
	configureRHDH(t, map[string]string{"values": "/nonexistent/overlay.yaml"})

	err := PreflightAddons("rhdh")
	if err == nil {
		t.Fatal("PreflightAddons(rhdh) = nil, want values overlay error")
	}
	for _, want := range []string{"rhdh", "/nonexistent/overlay.yaml"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want mention of %q", err, want)
		}
	}
}

func TestPreflightAddonsRHDHValidConfig(t *testing.T) {
	overlay := filepath.Join(t.TempDir(), "overlay.yaml")
	if err := os.WriteFile(overlay, []byte("global: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	configureRHDH(t, map[string]string{"values": overlay})

	if err := PreflightAddons("rhdh"); err != nil {
		t.Fatalf("PreflightAddons(rhdh) = %v, want nil", err)
	}
}

// the pre-flight must be the first create step so bad addon input fails
// before any container work.
func TestCreateStepsAddonPreflight(t *testing.T) {
	_, steps := CreateSteps(context.Background(), CreateOpts{Addons: "nosuch"})
	if len(steps) == 0 {
		t.Fatal("no create steps returned")
	}
	if steps[0].Name != "validating addons" {
		t.Fatalf("first step = %q, want validating addons", steps[0].Name)
	}
	if err := steps[0].Run(); err == nil || !strings.Contains(err.Error(), "unknown addon") {
		t.Errorf("preflight step err = %v, want unknown addon", err)
	}

	_, steps = CreateSteps(context.Background(), CreateOpts{})
	for _, s := range steps {
		if s.Name == "validating addons" {
			t.Error("validating addons step present without addons requested")
		}
	}
}

// non-TTY create must fail pre-flight before detecting a runtime or touching
// containers.
func TestCreatePlainAddonPreflight(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(devNull{}, nil))
	err := createPlain(context.Background(), CreateOpts{Addons: "nosuch"}, logger)
	if err == nil || !strings.Contains(err.Error(), "unknown addon") {
		t.Errorf("createPlain err = %v, want unknown addon", err)
	}
}
