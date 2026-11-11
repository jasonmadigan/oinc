package version

import (
	"encoding/json"
	"os"
	"regexp"
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
	if v.Minor != "5.0" || v.APIBranch != "release-5.0" || v.MicroShiftTag != v.Version {
		t.Fatalf("wrong compatibility settings: %+v", v)
	}
	if want := ImageRegistry + ":5.0.0-okd-scos.rc.1-" + v.Arch(); v.MicroShiftImage() != want {
		t.Fatalf("image = %s, want %s", v.MicroShiftImage(), want)
	}
}

// 5.0.0-okd-scos.0 was republished as an oinc revision. the payload's own tag
// names the superseded image, which stays recognisable but is never selected
func TestReplacementImage(t *testing.T) {
	v, err := Resolve("5.0.0-okd-scos.0")
	if err != nil {
		t.Fatal(err)
	}
	want := ImageRegistry + ":5.0.0-okd-scos.0-oinc.1-" + v.Arch()
	if v.MicroShiftImage() != want {
		t.Fatalf("selected %s, want %s", v.MicroShiftImage(), want)
	}
	for _, image := range []string{want, ImageRegistry + ":5.0.0-okd-scos.0-" + v.Arch()} {
		found, ok := ResolveFromImage(image)
		if !ok || found.Version != "5.0.0-okd-scos.0" || found.MicroShiftImage() != want {
			t.Fatalf("%s resolved to %+v, %v", image, found, ok)
		}
	}
}

// a minor and its pinned payload select one image, recognised as the minor
func TestPinnedPayloadImage(t *testing.T) {
	for _, pin := range catalogue {
		exact, err := Resolve(pin.MicroShiftTag)
		if err != nil {
			t.Fatal(err)
		}
		if exact.MicroShiftImage() != pin.MicroShiftImage() {
			t.Errorf("%s selects %s, its payload %s", pin.Version, pin.MicroShiftImage(), exact.MicroShiftImage())
		}
		if found, ok := ResolveFromImage(pin.MicroShiftImage()); !ok || found.Version != pin.Version {
			t.Errorf("%s resolved to %+v, %v", pin.MicroShiftImage(), found, ok)
		}
	}
}

// the image workflow builds what the lock names; the cli has to select it
func TestSourceLockMatchesCatalogue(t *testing.T) {
	data, err := os.ReadFile("../../images/5.0.json")
	if err != nil {
		t.Fatal(err)
	}
	var lock struct {
		OKDVersion string `json:"okd_version"`
		ImageTag   string `json:"image_tag"`
		Minor      string `json:"microshift_minor"`
	}
	if err := json.Unmarshal(data, &lock); err != nil {
		t.Fatal(err)
	}
	v, err := Resolve(lock.Minor)
	if err != nil {
		t.Fatal(err)
	}
	if v.MicroShiftTag != lock.OKDVersion || v.imageTag() != lock.ImageTag {
		t.Fatalf("catalogue pins %s (image tag %s), lock builds %s (image tag %s)", v.MicroShiftTag, v.imageTag(), lock.OKDVersion, lock.ImageTag)
	}
}

func TestConsoleImageFor(t *testing.T) {
	// the 4.22 payload console is amd64-only, and newer 4.22 builds reuse it
	payload := "quay.io/okd/scos-content@sha256:923f8700184642c1ddf2ba8a27f33c76d35930a463c76ee47e0fff335574c728"
	for _, selector := range []string{"4.22", "4.22.0-okd-scos.ec.17"} {
		v, err := Resolve(selector)
		if err != nil {
			t.Fatal(err)
		}
		for _, arch := range []string{"amd64", "arm64"} {
			if image, platform := v.consoleImageFor(arch); image != payload || platform != "linux/amd64" {
				t.Errorf("%s on %s: %s (%s), want %s (linux/amd64)", selector, arch, image, platform, payload)
			}
		}
	}
	// newer 5.0 builds reuse the minor's console
	v, err := Resolve("5.0.0-okd-scos.5")
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct{ arch, image, platform string }{
		{"amd64", "quay.io/okd/scos-content@sha256:be3b11ee9d96b959c8ba899ca00b25df8cf142dcb12ceb2722285d334d33e02d", "linux/amd64"},
		{"arm64", ConsoleRegistry + ":5.0.0-okd-scos.4-arm64", "linux/arm64"},
		{"s390x", "quay.io/okd/scos-content@sha256:be3b11ee9d96b959c8ba899ca00b25df8cf142dcb12ceb2722285d334d33e02d", "linux/amd64"},
	} {
		if image, platform := v.consoleImageFor(tt.arch); image != tt.image || platform != tt.platform {
			t.Errorf("5.0 on %s: %s (%s), want %s (%s)", tt.arch, image, platform, tt.image, tt.platform)
		}
	}
}

// a moving tag leaves each host on whatever build it cached, so the amd64
// console, which also serves arches without their own, must name a digest
func TestConsolesPinnedByDigest(t *testing.T) {
	digest := regexp.MustCompile(`^[^@]+@sha256:[0-9a-f]{64}$`)
	for _, v := range All() {
		if image, _ := v.consoleImageFor("amd64"); !digest.MatchString(image) {
			t.Errorf("%s console %q is not pinned by digest", v.Version, image)
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
