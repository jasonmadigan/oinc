package version

import (
	"testing"
)

func TestResolve(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantVer string
		wantErr bool
	}{
		{"empty returns default", "", Default().Version, false},
		{"valid version", "4.20", "4.20", false},
		{"invalid version", "3.99", "", true},
		{"full ec tag", "5.0.0-okd-scos.ec.8", "5.0.0-okd-scos.ec.8", false},
		{"future rc tag", "5.0.0-okd-scos.rc.1", "5.0.0-okd-scos.rc.1", false},
		{"stable tag", "5.0.0-okd-scos.1", "5.0.0-okd-scos.1", false},
		{"unknown minor tag", "6.0.0-okd-scos.rc.1", "", true},
		{"ocp is not okd", "5.0.0-rc.1", "", true},
		{"arch must not be supplied", "5.0.0-okd-scos.rc.1-arm64", "", true},
		{"invalid tag suffix", "5.0.0-okd-scos.rc.1/other", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Resolve(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tt.wantErr)
			}
			if !tt.wantErr && got.Version != tt.wantVer {
				t.Errorf("Version = %q, want %q", got.Version, tt.wantVer)
			}
		})
	}
}

func TestResolveFromImage(t *testing.T) {
	tests := []struct {
		name    string
		image   string
		wantVer string
		wantOK  bool
	}{
		{
			"valid image tag",
			"ghcr.io/jasonmadigan/oinc:4.21.0-okd-scos.ec.15-arm64",
			"4.21",
			true,
		},
		{"new release", "ghcr.io/jasonmadigan/oinc:5.0.0-okd-scos.rc.1-arm64", "5.0.0-okd-scos.rc.1", true},
		{"registry port", "localhost:5000/oinc:5.0.0-okd-scos.rc.1-amd64", "5.0.0-okd-scos.rc.1", true},
		{"tag and digest", "ghcr.io/jasonmadigan/oinc:5.0.0-okd-scos.rc.1-arm64@sha256:abc", "5.0.0-okd-scos.rc.1", true},
		{"prefix collision", "ghcr.io/jasonmadigan/oinc:4.21.0-okd-scos.ec.150-arm64", "4.21.0-okd-scos.ec.150", true},
		{"invalid trailing data", "ghcr.io/jasonmadigan/oinc:4.21.0-okd-scos.ec.15-arm64-extra", "", false},
		{"missing arch", "ghcr.io/jasonmadigan/oinc:4.21.0-okd-scos.ec.15", "", false},
		{
			"unknown image tag",
			"ghcr.io/jasonmadigan/oinc:9.99.0-unknown",
			"",
			false,
		},
		{
			"no colon in image",
			"ghcr.io/jasonmadigan/oinc",
			"",
			false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ResolveFromImage(tt.image)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if ok && got.Version != tt.wantVer {
				t.Errorf("Version = %q, want %q", got.Version, tt.wantVer)
			}
		})
	}
}

func TestFullTagCompatibility(t *testing.T) {
	v, err := Resolve("5.0.0-okd-scos.rc.1")
	if err != nil {
		t.Fatal(err)
	}
	if v.ConsoleTag != "5.0" || v.APIBranch != "release-5.0" || v.MicroShiftTag != v.Version {
		t.Fatalf("wrong compatibility settings: %+v", v)
	}
	if want := ImageRegistry + ":5.0.0-okd-scos.rc.1-" + v.Arch(); v.MicroShiftImage() != want {
		t.Fatalf("image = %s, want %s", v.MicroShiftImage(), want)
	}
}

func TestReplacementImage(t *testing.T) {
	for _, selector := range []string{"5.0", "5.0.0-okd-scos.0"} {
		v, err := Resolve(selector)
		if err != nil {
			t.Fatal(err)
		}
		want := ImageRegistry + ":5.0.0-okd-scos.0-oinc.1-" + v.Arch()
		if v.MicroShiftImage() != want {
			t.Fatalf("%s selected %s, want %s", selector, v.MicroShiftImage(), want)
		}
		if found, ok := ResolveFromImage(want); !ok || found.Version != "5.0" {
			t.Fatalf("replacement image not recognised: %+v, %v", found, ok)
		}
	}
}

func TestConsoleImageFor(t *testing.T) {
	legacy, err := Resolve("4.22")
	if err != nil {
		t.Fatal(err)
	}
	for _, arch := range []string{"amd64", "arm64"} {
		if image, platform := legacy.consoleImageFor(arch); image != ConsoleImage+":4.22" || platform != "linux/amd64" {
			t.Errorf("4.22 on %s: %s (%s), want origin-console on linux/amd64", arch, image, platform)
		}
	}
	// newer 5.0 builds reuse the minor's console
	v, err := Resolve("5.0.0-okd-scos.1")
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct{ arch, image, platform string }{
		{"amd64", "quay.io/okd/scos-content@sha256:947e41d0b4af41f65107639d7ed9c49aacd5162b59cc2b79d48ad1d37ad701bf", "linux/amd64"},
		{"arm64", ConsoleRegistry + ":5.0.0-okd-scos.0-arm64", "linux/arm64"},
		{"s390x", "quay.io/okd/scos-content@sha256:947e41d0b4af41f65107639d7ed9c49aacd5162b59cc2b79d48ad1d37ad701bf", "linux/amd64"},
	} {
		if image, platform := v.consoleImageFor(tt.arch); image != tt.image || platform != tt.platform {
			t.Errorf("5.0 on %s: %s (%s), want %s (%s)", tt.arch, image, platform, tt.image, tt.platform)
		}
	}
}

func TestRedHatPreviewIsExplicitAndExcludedFromOKDChannels(t *testing.T) {
	for _, selector := range []string{"ocp-4.23", "ocp-4.23.0-ec.1"} {
		v, err := Resolve(selector)
		if err != nil {
			t.Fatal(err)
		}
		if !v.RequiresPullSecret || !v.IsPrerelease() || v.ImageTag != "ocp-4.23.0-ec.1" {
			t.Fatalf("wrong preview: %+v", v)
		}
		if found, ok := ResolveFromImage(v.MicroShiftImage()); !ok || found.Version != "ocp-4.23" {
			t.Fatalf("unrecognised preview: %+v", found)
		}
		for _, arch := range []string{"amd64", "arm64"} {
			image, platform := v.consoleImageFor(arch)
			if image == "" || platform != "linux/"+arch {
				t.Fatalf("wrong console for %s", arch)
			}
		}
	}
	for _, selector := range []string{"4.23", "4.23.0-ec.1", "4.23.0-okd-scos.0"} {
		if _, err := Resolve(selector); err == nil {
			t.Fatalf("ambiguous selector accepted: %s", selector)
		}
	}
	if Default().Version != "5.0" {
		t.Fatal("preview changed default")
	}
	if got := versionsFromTags([]string{"ocp-4.23.0-ec.1-arm64"}); len(got) != 0 {
		t.Fatal("Red Hat preview leaked into OKD discovery")
	}
}
