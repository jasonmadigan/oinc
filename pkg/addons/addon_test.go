package addons

import (
	"context"
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

type fakeAddon struct {
	name string
	deps []string
}

func (f *fakeAddon) Name() string                               { return f.name }
func (f *fakeAddon) Dependencies() []string                     { return f.deps }
func (f *fakeAddon) Install(_ context.Context, _ *Config) error { return nil }
func (f *fakeAddon) Ready(_ context.Context, _ *Config) error   { return nil }
