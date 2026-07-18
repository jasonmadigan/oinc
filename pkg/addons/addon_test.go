package addons

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestParseAddonSpec(t *testing.T) {
	tests := []struct {
		name     string
		spec     string
		wantName string
		wantOpts map[string]string
	}{
		{"plain name", "cert-manager", "cert-manager", map[string]string{}},
		{"name with version", "cert-manager@1.16.0", "cert-manager", map[string]string{"version": "1.16.0"}},
		{"empty string", "", "", map[string]string{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			name, opts := ParseAddonSpec(tt.spec)
			if name != tt.wantName {
				t.Errorf("name = %q, want %q", name, tt.wantName)
			}
			if len(opts) != len(tt.wantOpts) {
				t.Errorf("opts = %v, want %v", opts, tt.wantOpts)
				return
			}
			for k, v := range tt.wantOpts {
				if opts[k] != v {
					t.Errorf("opts[%q] = %q, want %q", k, opts[k], v)
				}
			}
		})
	}
}

func TestResolve(t *testing.T) {
	tests := []struct {
		name      string
		specs     []string
		wantNames []string
		wantErr   bool
	}{
		{
			"single addon",
			[]string{"cert-manager"},
			[]string{"cert-manager"},
			false,
		},
		{
			"addon with transitive deps",
			[]string{"kuadrant"},
			[]string{"gateway-api", "cert-manager", "metallb", "istio", "kuadrant"},
			false,
		},
		{
			"unknown addon",
			[]string{"nonexistent"},
			nil,
			true,
		},
		{
			"empty version after @",
			[]string{"cert-manager@"},
			nil,
			true,
		},
		{
			"empty addon name",
			[]string{"@1.16.0"},
			nil,
			true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Resolve(tt.specs)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}

			gotNames := make([]string, len(got))
			for i, a := range got {
				gotNames[i] = a.Name()
			}

			if len(gotNames) != len(tt.wantNames) {
				t.Fatalf("got %v, want %v", gotNames, tt.wantNames)
			}

			// kuadrant must come after all its deps
			if tt.name == "addon with transitive deps" {
				idx := map[string]int{}
				for i, n := range gotNames {
					idx[n] = i
				}
				for _, dep := range []string{"gateway-api", "cert-manager", "metallb", "istio"} {
					if idx[dep] >= idx["kuadrant"] {
						t.Errorf("%s (index %d) should come before kuadrant (index %d)", dep, idx[dep], idx["kuadrant"])
					}
				}
			}
		})
	}
}

func TestResolveCycleDetection(t *testing.T) {
	// save and restore the real registry
	orig := make(map[string]Addon, len(registry))
	for k, v := range registry {
		orig[k] = v
	}
	defer func() {
		registry = orig
	}()

	registry = map[string]Addon{}
	Register(&fakeAddon{name: "a", deps: []string{"b"}})
	Register(&fakeAddon{name: "b", deps: []string{"a"}})

	_, err := Resolve([]string{"a"})
	if err == nil {
		t.Fatal("expected cycle error, got nil")
	}
}

// a broken config on an addon pulled in as a dependency must fail validation
// even when only the parent was requested.
func TestValidateDependencyPulled(t *testing.T) {
	orig := make(map[string]Addon, len(registry))
	for k, v := range registry {
		orig[k] = v
	}
	defer func() {
		registry = orig
	}()

	registry = map[string]Addon{}
	Register(&fakeValidatingAddon{
		fakeAddon:   fakeAddon{name: "child"},
		validateErr: errors.New("bad child config"),
	})
	Register(&fakeAddon{name: "parent", deps: []string{"child"}})

	sorted, err := Resolve([]string{"parent"})
	if err != nil {
		t.Fatalf("Resolve(parent) = %v", err)
	}
	err = Validate(sorted)
	if err == nil {
		t.Fatal("Validate = nil, want child config error")
	}
	for _, want := range []string{"child", "bad child config"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want mention of %q", err, want)
		}
	}
}

func TestConfigure(t *testing.T) {
	orig := make(map[string]Addon, len(registry))
	for k, v := range registry {
		orig[k] = v
	}
	defer func() {
		registry = orig
		optionsSet = map[string]map[string]bool{}
	}()

	registry = map[string]Addon{}
	fake := &fakeConfigurableAddon{fakeAddon: fakeAddon{name: "cfg"}}
	Register(fake)

	Configure("cfg", map[string]string{"image": "localhost/x:y"})
	if fake.opts["image"] != "localhost/x:y" {
		t.Errorf("opts = %v, want image set", fake.opts)
	}

	// unknown addons and empty opts are no-ops
	Configure("missing", map[string]string{"a": "b"})
	Configure("cfg", nil)
}

type fakeAddon struct {
	name string
	deps []string
}

func (f *fakeAddon) Name() string                               { return f.name }
func (f *fakeAddon) Dependencies() []string                     { return f.deps }
func (f *fakeAddon) Install(_ context.Context, _ *Config) error { return nil }
func (f *fakeAddon) Ready(_ context.Context, _ *Config) error   { return nil }

type fakeConfigurableAddon struct {
	fakeAddon
	opts map[string]string
}

func (f *fakeConfigurableAddon) SetOptions(opts map[string]string) { f.opts = opts }

type fakeValidatingAddon struct {
	fakeAddon
	validateErr error
}

func (f *fakeValidatingAddon) Validate() error { return f.validateErr }

// resetOptions clears option tracking and the touched singletons directly,
// bypassing Configure so the reset itself is not recorded.
func resetOptions(t *testing.T, opts map[string]map[string]string) {
	t.Helper()
	t.Cleanup(func() {
		for name, o := range opts {
			if c, ok := registry[name].(Configurable); ok {
				c.SetOptions(o)
			}
		}
		optionsSet = map[string]map[string]bool{}
	})
}

// options for an addon outside the resolved closure must fail pre-flight,
// naming the flag, instead of being silently dropped.
func TestResolveRejectsOptionsOutsideClosure(t *testing.T) {
	resetOptions(t, map[string]map[string]string{
		"metallb": {"address-pool": ""},
	})
	Configure("metallb", map[string]string{"address-pool": "auto"})

	_, err := Resolve([]string{"cert-manager"})
	if err == nil {
		t.Fatal("expected error for options on an addon outside the requested set")
	}
	if !strings.Contains(err.Error(), "--metallb-address-pool") || !strings.Contains(err.Error(), "metallb") {
		t.Errorf("error %q should name the flag and the missing addon", err)
	}
}

// dependency-pulled addons are in the closure: --gateway-api-gateway pulls
// metallb, so metallb options with only gateway-api listed must pass.
func TestResolveAcceptsOptionsForDepPulledAddon(t *testing.T) {
	resetOptions(t, map[string]map[string]string{
		"metallb":     {"address-pool": ""},
		"gateway-api": {"gateway": "false"},
	})
	Configure("gateway-api", map[string]string{"gateway": "true"})
	Configure("metallb", map[string]string{"address-pool": "auto"})

	sorted, err := Resolve([]string{"gateway-api"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	names := map[string]bool{}
	for _, a := range sorted {
		names[a.Name()] = true
	}
	if !names["metallb"] || !names["istio"] {
		t.Errorf("closure %v should include the option-pulled dependencies", names)
	}
}

// version pins are not instance options: they must not mark an addon as
// having options, or plain @version installs would defeat the ready-skip.
func TestConfigureVersionOnlyIsNotTracked(t *testing.T) {
	resetOptions(t, map[string]map[string]string{
		"metallb": {"version": ""},
	})
	Configure("metallb", map[string]string{"version": "0.14.8"})

	if HasOptions("metallb") {
		t.Error("version-only configuration must not count as options")
	}
	if AnyOptions() {
		t.Error("AnyOptions must stay false for version-only configuration")
	}
}
