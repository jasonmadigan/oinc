package addons

import (
	"fmt"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

func TestRHDHRegistered(t *testing.T) {
	sorted, err := Resolve([]string{"rhdh"})
	if err != nil {
		t.Fatalf("Resolve(rhdh) = %v", err)
	}
	if len(sorted) != 1 || sorted[0].Name() != "rhdh" {
		t.Fatalf("Resolve(rhdh) = %v, want just rhdh (no dependencies)", sorted)
	}
}

func TestRHDHResolveVersion(t *testing.T) {
	tests := []struct {
		name string
		set  string
		want string
	}{
		{"default", "", defaultRHDHChartVersion},
		{"pinned", "7.0.0", "7.0.0"},
		{"latest", "latest", "latest"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &rhdh{version: tt.set}
			if got := r.resolveVersion(); got != tt.want {
				t.Errorf("resolveVersion() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRHDHChartVersionArgs(t *testing.T) {
	if got := (&rhdh{version: "6.2.2"}).chartVersionArgs(); len(got) != 2 || got[0] != "--version" || got[1] != "6.2.2" {
		t.Errorf("chartVersionArgs(6.2.2) = %v, want [--version 6.2.2]", got)
	}
	// latest follows the chart index: no pin
	if got := (&rhdh{version: "latest"}).chartVersionArgs(); got != nil {
		t.Errorf("chartVersionArgs(latest) = %v, want nil", got)
	}
}

func TestSplitImageRef(t *testing.T) {
	tests := []struct {
		ref      string
		wantRepo string
		wantTag  string
	}{
		{"localhost/my-rhdh:dev", "localhost/my-rhdh", "dev"},
		{"localhost:5000/my-rhdh:dev", "localhost:5000/my-rhdh", "dev"},
		{"quay.io/rhdh-community/rhdh:1.10", "quay.io/rhdh-community/rhdh", "1.10"},
		{"my-rhdh", "my-rhdh", "latest"},
		{"localhost:5000/my-rhdh", "localhost:5000/my-rhdh", "latest"},
	}
	for _, tt := range tests {
		t.Run(tt.ref, func(t *testing.T) {
			repo, tag := splitImageRef(tt.ref)
			if repo != tt.wantRepo || tag != tt.wantTag {
				t.Errorf("splitImageRef(%q) = (%q, %q), want (%q, %q)", tt.ref, repo, tag, tt.wantRepo, tt.wantTag)
			}
		})
	}
}

func TestRHDHBaseURL(t *testing.T) {
	tests := []struct {
		name     string
		port     int
		wantHost string
		wantURL  string
		wantErr  bool
	}{
		{"mapped port", 9080, "rhdh.127.0.0.1.nip.io", "http://rhdh.127.0.0.1.nip.io:9080", false},
		{"port 80", 80, "rhdh.127.0.0.1.nip.io", "http://rhdh.127.0.0.1.nip.io", false},
		{"unknown port", 0, "", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{IngressHost: "127.0.0.1.nip.io", IngressHTTPPort: tt.port}
			host, url, err := rhdhBaseURL(cfg)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if host != tt.wantHost || url != tt.wantURL {
				t.Errorf("rhdhBaseURL() = (%q, %q), want (%q, %q)", host, url, tt.wantHost, tt.wantURL)
			}
		})
	}
}

// the underlying inspect failure must surface in the error, not just a
// generic "cannot determine" message.
func TestRHDHBaseURLPropagatesIngressErr(t *testing.T) {
	cfg := &Config{
		IngressHost: "127.0.0.1.nip.io",
		IngressErr:  fmt.Errorf("inspect oinc: no such container"),
	}
	_, _, err := rhdhBaseURL(cfg)
	if err == nil || !strings.Contains(err.Error(), "no such container") {
		t.Errorf("err = %v, want the inspect cause included", err)
	}
}

// dig walks nested map[string]any values by key path.
func dig(t *testing.T, v any, path ...string) any {
	t.Helper()
	for _, k := range path {
		m, ok := v.(map[string]any)
		if !ok {
			t.Fatalf("dig %v: %T is not a map", path, v)
		}
		v, ok = m[k]
		if !ok {
			t.Fatalf("dig %v: key %q missing", path, k)
		}
	}
	return v
}

func parseValues(t *testing.T, r *rhdh) map[string]any {
	t.Helper()
	raw := r.renderValues("rhdh.127.0.0.1.nip.io", "http://rhdh.127.0.0.1.nip.io:9080")
	var out map[string]any
	if err := yaml.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("rendered values are not valid yaml: %v\n%s", err, raw)
	}
	return out
}

func TestRHDHRenderValuesDefaults(t *testing.T) {
	v := parseValues(t, &rhdh{})

	if got := dig(t, v, "global", "host"); got != "rhdh.127.0.0.1.nip.io" {
		t.Errorf("global.host = %v", got)
	}
	if got := dig(t, v, "route", "tls", "enabled"); got != false {
		t.Errorf("route.tls.enabled = %v, want false", got)
	}
	for _, path := range [][]string{
		{"upstream", "backstage", "appConfig", "app", "baseUrl"},
		{"upstream", "backstage", "appConfig", "backend", "baseUrl"},
		{"upstream", "backstage", "appConfig", "backend", "cors", "origin"},
	} {
		if got := dig(t, v, path...); got != "http://rhdh.127.0.0.1.nip.io:9080" {
			t.Errorf("%v = %v, want route url", path, got)
		}
	}

	guest := dig(t, v, "upstream", "backstage", "appConfig", "auth", "providers", "guest")
	if got := dig(t, guest, "dangerouslyAllowOutsideDevelopment"); got != true {
		t.Errorf("guest.dangerouslyAllowOutsideDevelopment = %v, want true", got)
	}

	// the default chart version pairs with a pinned image line: the chart's
	// own default tag is the "next" nightly, which is not deterministic
	img := dig(t, v, "upstream", "backstage", "image")
	if got := dig(t, img, "repository"); got != "quay.io/rhdh-community/rhdh" {
		t.Errorf("image.repository = %v", got)
	}
	if got := dig(t, img, "tag"); got != "1.10" {
		t.Errorf("image.tag = %v, want the line paired with chart %s", got, defaultRHDHChartVersion)
	}

	// quickstart untouched by default
	plugins, ok := dig(t, v, "global", "dynamic", "plugins").([]any)
	if !ok || len(plugins) != 0 {
		t.Errorf("global.dynamic.plugins = %v, want empty list", plugins)
	}
}

// helm replaces extraVolumes wholesale: the override must re-declare the full
// seven-volume set the chart's initContainer and main container mount, with
// dynamic-plugins-root as emptyDir (microshift has no PVC provisioner).
func TestRHDHRenderValuesVolumes(t *testing.T) {
	v := parseValues(t, &rhdh{})

	vols, ok := dig(t, v, "upstream", "backstage", "extraVolumes").([]any)
	if !ok {
		t.Fatal("extraVolumes is not a list")
	}

	want := []string{
		"dynamic-plugins-root", "dynamic-plugins", "dynamic-plugins-npmrc",
		"dynamic-plugins-registry-auth", "npmcacache", "extensions-catalog", "temp",
	}
	if len(vols) != len(want) {
		t.Fatalf("extraVolumes has %d entries, want %d", len(vols), len(want))
	}
	for i, name := range want {
		vol, ok := vols[i].(map[string]any)
		if !ok {
			t.Fatalf("extraVolumes[%d] is not a map", i)
		}
		if vol["name"] != name {
			t.Errorf("extraVolumes[%d].name = %v, want %q", i, vol["name"], name)
		}
	}

	root := vols[0].(map[string]any)
	if _, ok := root["emptyDir"]; !ok {
		t.Error("dynamic-plugins-root must be an emptyDir")
	}
	if _, ok := root["ephemeral"]; ok {
		t.Error("dynamic-plugins-root must not be the chart default ephemeral PVC")
	}
}

func TestRHDHRenderValuesPostgres(t *testing.T) {
	v := parseValues(t, &rhdh{})

	if got := dig(t, v, "upstream", "postgresql", "primary", "persistence", "enabled"); got != false {
		t.Errorf("postgresql persistence enabled = %v, want false", got)
	}
	if got := dig(t, v, "upstream", "postgresql", "primary", "resources", "limits", "ephemeral-storage"); got != "2Gi" {
		t.Errorf("postgresql ephemeral-storage limit = %v, want 2Gi", got)
	}
}

// the paired image default only applies to the default chart version; other
// pins and latest keep the chart image-agnostic so the user owns the pairing
func TestRHDHRenderValuesImagePairing(t *testing.T) {
	for _, version := range []string{"", defaultRHDHChartVersion} {
		v := parseValues(t, &rhdh{version: version})
		if got := dig(t, v, "upstream", "backstage", "image", "tag"); got != "1.10" {
			t.Errorf("version %q: image.tag = %v, want 1.10", version, got)
		}
	}
	for _, version := range []string{"7.0.0", "latest"} {
		v := parseValues(t, &rhdh{version: version})
		backstage, ok := dig(t, v, "upstream", "backstage").(map[string]any)
		if !ok {
			t.Fatal("upstream.backstage is not a map")
		}
		if _, present := backstage["image"]; present {
			t.Errorf("version %q: image block present, want chart defaults", version)
		}
	}
	// explicit override wins regardless of chart version
	v := parseValues(t, &rhdh{version: "7.0.0", image: "localhost/x:y"})
	if got := dig(t, v, "upstream", "backstage", "image", "repository"); got != "localhost/x" {
		t.Errorf("image.repository = %v, want explicit override", got)
	}
}

func TestRHDHRenderValuesImageOverride(t *testing.T) {
	v := parseValues(t, &rhdh{image: "localhost/my-rhdh:dev"})

	img := dig(t, v, "upstream", "backstage", "image")
	if got := dig(t, img, "registry"); got != "" {
		t.Errorf("image.registry = %v, want empty (so localhost/ refs resolve)", got)
	}
	if got := dig(t, img, "repository"); got != "localhost/my-rhdh" {
		t.Errorf("image.repository = %v", got)
	}
	if got := dig(t, img, "tag"); got != "dev" {
		t.Errorf("image.tag = %v", got)
	}
	if got := dig(t, img, "pullPolicy"); got != "IfNotPresent" {
		t.Errorf("image.pullPolicy = %v, want IfNotPresent", got)
	}
}

func TestRHDHRenderValuesDisableQuickstart(t *testing.T) {
	v := parseValues(t, &rhdh{disableQuickstart: true})

	plugins, ok := dig(t, v, "global", "dynamic", "plugins").([]any)
	if !ok || len(plugins) != 1 {
		t.Fatalf("global.dynamic.plugins = %v, want one entry", plugins)
	}
	entry, ok := plugins[0].(map[string]any)
	if !ok {
		t.Fatal("plugins[0] is not a map")
	}
	pkg, _ := entry["package"].(string)
	if !strings.Contains(pkg, "quickstart") {
		t.Errorf("plugins[0].package = %q, want the quickstart plugin", pkg)
	}
	if entry["disabled"] != true {
		t.Errorf("plugins[0].disabled = %v, want true", entry["disabled"])
	}
}

// helm value files merge left to right with the last file winning on
// conflicts: user-wins semantics hold only if the overlay is the final
// --values.
func TestRHDHHelmArgsOverlayLast(t *testing.T) {
	r := &rhdh{valuesFile: "/tmp/overlay.yaml"}
	args := r.helmArgs("/tmp/base.yaml")

	var values []string
	for i, a := range args {
		if a == "--values" && i+1 < len(args) {
			values = append(values, args[i+1])
		}
	}
	if len(values) != 2 || values[0] != "/tmp/base.yaml" || values[1] != "/tmp/overlay.yaml" {
		t.Errorf("--values order = %v, want base then overlay last", values)
	}
}

func TestRHDHHelmArgsVersion(t *testing.T) {
	args := (&rhdh{version: "6.2.2"}).helmArgs("/tmp/base.yaml")
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--version 6.2.2") {
		t.Errorf("args = %v, want chart pin", args)
	}
	for _, want := range []string{"upgrade", "--install", "rhdh/backstage", "--wait"} {
		if !strings.Contains(joined, want) {
			t.Errorf("args = %v, missing %q", args, want)
		}
	}

	if joined := strings.Join((&rhdh{version: "latest"}).helmArgs("/tmp/base.yaml"), " "); strings.Contains(joined, "--version") {
		t.Errorf("latest must not pin the chart: %v", joined)
	}

	// no overlay: single --values
	count := 0
	for _, a := range (&rhdh{}).helmArgs("/tmp/base.yaml") {
		if a == "--values" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("--values count = %d, want 1 without overlay", count)
	}
}

func TestRHDHSetOptions(t *testing.T) {
	r := &rhdh{}
	r.SetOptions(map[string]string{
		"version":            "7.0.0",
		"image":              "localhost/x:y",
		"values":             "/tmp/overlay.yaml",
		"disable-quickstart": "true",
	})
	if r.version != "7.0.0" || r.image != "localhost/x:y" || r.valuesFile != "/tmp/overlay.yaml" || !r.disableQuickstart {
		t.Errorf("SetOptions did not apply: %+v", r)
	}
}
