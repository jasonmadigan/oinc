package addons

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	kruntime "k8s.io/apimachinery/pkg/runtime"
)

// storePullSecret points oinc's config at a temp dir holding a pull secret
// with credentials for the given registries.
func storePullSecret(t *testing.T, registries ...string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("OINC_CONFIG_DIR", dir)
	if len(registries) == 0 {
		return
	}
	var auths []string
	for _, registry := range registries {
		auths = append(auths, `"`+registry+`":{"auth":"dXNlcjpwYXNzd29yZA=="}`)
	}
	secret := `{"auths":{` + strings.Join(auths, ",") + `}}`
	if err := os.WriteFile(filepath.Join(dir, "pull-secret.json"), []byte(secret), 0600); err != nil {
		t.Fatal(err)
	}
}

// catalogRHCL is an rhcl addon reading the captured catalog instead of the
// network. lookups counts how often it asked.
func catalogRHCL(t *testing.T, opts map[string]string) (addon *rhcl, lookups *int) {
	t.Helper()
	catalog := fixtureRHCLCatalog(t)
	count := 0
	addon = newRHCL()
	addon.fetchCatalog = func(context.Context, *slog.Logger) (rhclCatalog, error) {
		count++
		return catalog, nil
	}
	addon.SetOptions(opts)
	return addon, &count
}

func TestRHCLReplacesKuadrantOnTheSameStack(t *testing.T) {
	registered, ok := Get("rhcl")
	if !ok {
		t.Fatal("rhcl addon is not registered")
	}
	upstream, _ := Get("kuadrant")

	if !reflect.DeepEqual(registered.Dependencies(), upstream.Dependencies()) {
		t.Errorf("dependencies = %v, want kuadrant's %v", registered.Dependencies(), upstream.Dependencies())
	}

	_, err := Resolve([]string{"kuadrant", "rhcl"})
	if err == nil || !strings.Contains(err.Error(), "cannot be installed together") {
		t.Errorf("Resolve(kuadrant, rhcl) = %v, want a conflict", err)
	}
}

func TestRHCLValidateRejectsMalformedVersions(t *testing.T) {
	storePullSecret(t, "registry.redhat.io")

	for _, version := range []string{"latest", "1.4", "v1.4.3", "1.4.3-rc1"} {
		addon, lookups := catalogRHCL(t, map[string]string{"version": version})
		err := addon.Validate()
		if err == nil || !strings.Contains(err.Error(), "rhcl@1.4.3") {
			t.Errorf("Validate(%q) = %v, want an error showing the x.y.z form", version, err)
		}
		if *lookups != 0 {
			t.Errorf("Validate(%q) queried the catalog for a malformed version", version)
		}
	}
}

func TestRHCLValidateNeedsARedHatPullSecret(t *testing.T) {
	t.Run("none stored", func(t *testing.T) {
		storePullSecret(t)
		addon, _ := catalogRHCL(t, map[string]string{"version": "1.4.3"})
		err := addon.Validate()
		if err == nil || !strings.Contains(err.Error(), "pull-secret set") {
			t.Errorf("Validate = %v, want a hint to run oinc pull-secret set", err)
		}
	})

	t.Run("stored without registry.redhat.io", func(t *testing.T) {
		storePullSecret(t, "quay.io")
		addon, _ := catalogRHCL(t, map[string]string{"version": "1.4.3"})
		err := addon.Validate()
		if err == nil || !strings.Contains(err.Error(), "registry.redhat.io") {
			t.Errorf("Validate = %v, want it to name registry.redhat.io", err)
		}
	})
}

func TestRHCLValidateRejectsAVersionTheCatalogLacks(t *testing.T) {
	storePullSecret(t, "registry.redhat.io")
	addon, _ := catalogRHCL(t, map[string]string{"version": "9.9.9"})

	err := addon.Validate()
	if err == nil || !strings.Contains(err.Error(), "9.9.9") || !strings.Contains(err.Error(), "1.4.3") {
		t.Errorf("Validate = %v, want it to name the missing version and the available ones", err)
	}
}

func TestRHCLInstallsWhatValidateResolved(t *testing.T) {
	storePullSecret(t, "registry.redhat.io")

	tests := []struct {
		name string
		opts map[string]string
		want olmOperator
	}{
		{
			name: "pinned version",
			opts: map[string]string{"version": "1.4.3"},
			want: olmOperator{
				namespace:          "kuadrant-system",
				catalog:            "redhat-operators",
				catalogImage:       "registry.redhat.io/redhat/redhat-operator-index:v4.22",
				catalogDisplayName: "Red Hat Operators",
				pkg:                "rhcl-operator",
				channel:            "stable-1.4",
				manual:             true,
				csv:                "rhcl-operator.v1.4.3",
			},
		},
		{
			name: "no version",
			opts: nil,
			want: olmOperator{
				namespace:          "kuadrant-system",
				catalog:            "redhat-operators",
				catalogImage:       "registry.redhat.io/redhat/redhat-operator-index:v4.22",
				catalogDisplayName: "Red Hat Operators",
				pkg:                "rhcl-operator",
				channel:            "stable",
				manual:             true,
				csv:                "rhcl-operator.v1.4.3",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			addon, _ := catalogRHCL(t, tt.opts)
			if err := addon.Validate(); err != nil {
				t.Fatal(err)
			}
			if got := addon.operator(); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("operator = %+v\nwant       %+v", got, tt.want)
			}
		})
	}
}

// an index override is for runs that cannot reach the catalog API, or for an
// index it does not describe, so it must not be consulted at all.
func TestRHCLIndexOverrideSkipsTheCatalog(t *testing.T) {
	storePullSecret(t, "registry.redhat.io")

	t.Run("pinned version maps to its minor channel", func(t *testing.T) {
		addon, lookups := catalogRHCL(t, map[string]string{"version": "1.6.2", "index": "mirror.example/redhat-operator-index:v4.22"})
		if err := addon.Validate(); err != nil {
			t.Fatal(err)
		}
		if *lookups != 0 {
			t.Errorf("made %d catalog lookups, want none", *lookups)
		}
		got := addon.operator()
		if got.catalogImage != "mirror.example/redhat-operator-index:v4.22" || got.channel != "stable-1.6" || got.csv != "rhcl-operator.v1.6.2" {
			t.Errorf("operator = %+v, want the override index, channel stable-1.6 and csv rhcl-operator.v1.6.2", got)
		}
	})

	t.Run("no version follows the head of stable", func(t *testing.T) {
		addon, lookups := catalogRHCL(t, map[string]string{"index": "mirror.example/redhat-operator-index:v4.22"})
		if err := addon.Validate(); err != nil {
			t.Fatal(err)
		}
		if *lookups != 0 {
			t.Errorf("made %d catalog lookups, want none", *lookups)
		}
		got := addon.operator()
		if got.channel != "stable" || got.csv != "" || !got.manual {
			t.Errorf("operator = %+v, want channel stable with its head approved manually", got)
		}
	})
}

func TestRHCLValidateSuggestsTheIndexFlagWhenTheCatalogIsUnreachable(t *testing.T) {
	storePullSecret(t, "registry.redhat.io")
	addon := newRHCL()
	addon.fetchCatalog = func(context.Context, *slog.Logger) (rhclCatalog, error) {
		return nil, errors.New("curl: (6) Could not resolve host: catalog.redhat.com")
	}

	err := addon.Validate()
	if err == nil {
		t.Fatal("want an error when the catalog cannot be reached")
	}
	for _, want := range []string{"Could not resolve host", "--rhcl-index"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

// create validates in pre-flight and again when installing; the second pass
// must reuse the first lookup so both act on the same release.
func TestRHCLResolvesItsReleaseOnce(t *testing.T) {
	storePullSecret(t, "registry.redhat.io")
	addon, lookups := catalogRHCL(t, map[string]string{"version": "1.4.3"})

	for range 2 {
		if err := addon.Validate(); err != nil {
			t.Fatal(err)
		}
	}
	if *lookups != 1 {
		t.Errorf("made %d catalog lookups, want 1", *lookups)
	}

	addon.SetOptions(map[string]string{"version": "1.4.4"})
	if err := addon.Validate(); err != nil {
		t.Fatal(err)
	}
	if got := addon.operator().csv; got != "rhcl-operator.v1.4.4" {
		t.Errorf("csv = %q after changing the version, want rhcl-operator.v1.4.4", got)
	}
}

func TestRHCLVersionsOffersTheCatalogWithStableHeadAsDefault(t *testing.T) {
	addon, _ := catalogRHCL(t, nil)

	choices, err := addon.Versions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(choices) != 21 {
		t.Fatalf("got %d versions, want 21", len(choices))
	}
	if choices[0].Version != "1.5.0" {
		t.Errorf("first version = %q, want the newest, 1.5.0", choices[0].Version)
	}
	var defaults []string
	for _, c := range choices {
		if c.Default {
			defaults = append(defaults, c.Version)
		}
	}
	if !reflect.DeepEqual(defaults, []string{"1.4.3"}) {
		t.Errorf("default versions = %v, want only 1.4.3, the head of stable", defaults)
	}
	if want := "stable-1.5, index v5.0"; choices[0].Hint != want {
		t.Errorf("hint = %q, want %q", choices[0].Hint, want)
	}
}

func makeDeployment(namespace, name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"metadata":   map[string]any{"name": name, "namespace": namespace},
	}}
}

// clusterRHCL is an rhcl addon whose cluster container has, or lacks, the
// mounted pull secret.
func clusterRHCL(pullSecretMounted bool) *rhcl {
	addon := newRHCL()
	addon.clusterHasFile = func(_ *Config, path string) bool {
		return pullSecretMounted && path == "/etc/crio/openshift-pull-secret"
	}
	return addon
}

func TestRHCLValidateCluster(t *testing.T) {
	rhclSubscription := makeSubscription("rhcl-operator", map[string]any{}, map[string]any{})
	operator := makeDeployment("kuadrant-system", "kuadrant-operator-controller-manager")

	tests := []struct {
		name    string
		mounted bool
		objects []kruntime.Object
		wantErr string
	}{
		{name: "fresh cluster with the pull secret", mounted: true},
		{name: "cluster created without the pull secret", mounted: false, wantErr: "pull secret"},
		{name: "upstream kuadrant already installed", mounted: true, objects: []kruntime.Object{operator}, wantErr: "upstream kuadrant"},
		{name: "rhcl already installed", mounted: true, objects: []kruntime.Object{operator, rhclSubscription}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := quietConfig(olmFakeClient(tt.objects...))
			err := clusterRHCL(tt.mounted).ValidateCluster(context.Background(), cfg)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidateCluster = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("ValidateCluster = %v, want an error mentioning %q", err, tt.wantErr)
			}
		})
	}
}

func TestKuadrantRefusesAClusterRunningRHCL(t *testing.T) {
	cfg := quietConfig(olmFakeClient(makeSubscription("rhcl-operator", map[string]any{}, map[string]any{})))
	err := (&kuadrant{}).ValidateCluster(context.Background(), cfg)
	if err == nil || !strings.Contains(err.Error(), "RHCL") {
		t.Errorf("ValidateCluster = %v, want an error saying RHCL is installed", err)
	}

	if err := (&kuadrant{}).ValidateCluster(context.Background(), quietConfig(olmFakeClient())); err != nil {
		t.Errorf("ValidateCluster on a cluster without RHCL = %v, want nil", err)
	}
}

// with an index override the catalog is not consulted, so there is nothing
// to offer and the install follows the override.
func TestRHCLVersionsOffersNothingForAnIndexOverride(t *testing.T) {
	addon, lookups := catalogRHCL(t, map[string]string{"index": "mirror.example/redhat-operator-index:v4.22"})

	choices, err := addon.Versions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(choices) != 0 || *lookups != 0 {
		t.Errorf("got %d choices from %d catalog lookups, want none of either", len(choices), *lookups)
	}
}

// asking for another version of an installed RHCL must be refused before the
// dependencies are reinstalled, not when the operator's turn comes.
func TestRHCLValidateClusterRefusesToChangeTheInstalledVersion(t *testing.T) {
	storePullSecret(t, "registry.redhat.io")
	installed := makeSubscription("rhcl-operator", map[string]any{}, map[string]any{"installedCSV": "rhcl-operator.v1.4.3"})
	cfg := quietConfig(olmFakeClient(installed))

	wanted, _ := catalogRHCL(t, map[string]string{"version": "1.4.4"})
	wanted.clusterHasFile = func(*Config, string) bool { return true }
	if err := wanted.Validate(); err != nil {
		t.Fatal(err)
	}
	err := wanted.ValidateCluster(context.Background(), cfg)
	if err == nil || !strings.Contains(err.Error(), "rhcl-operator.v1.4.3") || !strings.Contains(err.Error(), "rhcl-operator.v1.4.4") {
		t.Errorf("ValidateCluster = %v, want it to name the installed and the requested version", err)
	}

	same, _ := catalogRHCL(t, map[string]string{"version": "1.4.3"})
	same.clusterHasFile = func(*Config, string) bool { return true }
	if err := same.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := same.ValidateCluster(context.Background(), cfg); err != nil {
		t.Errorf("ValidateCluster for the installed version = %v, want nil", err)
	}
}
