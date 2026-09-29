package version

import (
	"fmt"
	"regexp"
	"runtime"
	"strings"
)

type OCPVersion struct {
	Version       string            // minor version or full OKD tag
	MicroShiftTag string            // "4.18.0-okd-scos.9" (arch appended at runtime)
	ImageTag      string            // optional OINC build revision for the same payload
	ConsoleTag    string            // "4.18"
	ConsoleImages map[string]string // per-arch console refs, replacing origin-console:ConsoleTag
	APIBranch     string            // "release-4.18"
	Arches        []string          // supported architectures
}

var catalogue = []OCPVersion{
	{
		Version:       "4.20",
		MicroShiftTag: "4.20.0-okd-scos.16",
		ConsoleTag:    "4.20",
		APIBranch:     "release-4.20",
		Arches:        []string{"amd64", "arm64"},
	},
	{
		Version:       "4.21",
		MicroShiftTag: "4.21.0-okd-scos.ec.15",
		ConsoleTag:    "4.21",
		APIBranch:     "release-4.21",
		Arches:        []string{"amd64", "arm64"},
	},
	{
		Version:       "4.22",
		MicroShiftTag: "4.22.0-okd-scos.ec.16",
		ConsoleTag:    "4.22",
		APIBranch:     "release-4.22",
		Arches:        []string{"amd64", "arm64"},
	},
	{
		Version:       "5.0",
		MicroShiftTag: "5.0.0-okd-scos.0",
		ImageTag:      "5.0.0-okd-scos.0-oinc.1",
		ConsoleTag:    "5.0",
		// console from the 5.0.0-okd-scos.0 payload, plus a native arm64 rebuild
		// of the same commit (images/Containerfile.console)
		ConsoleImages: map[string]string{
			"amd64": "quay.io/okd/scos-content@sha256:947e41d0b4af41f65107639d7ed9c49aacd5162b59cc2b79d48ad1d37ad701bf",
			"arm64": ConsoleRegistry + ":5.0.0-okd-scos.0-arm64",
		},
		APIBranch: "release-5.0",
		Arches:    []string{"amd64", "arm64"},
	},
}

func All() []OCPVersion { return catalogue }

// Default remains an offline pin to the newest stable catalogue entry.
func Default() OCPVersion {
	for i := len(catalogue) - 1; i >= 0; i-- {
		if !catalogue[i].IsPrerelease() {
			return catalogue[i]
		}
	}
	panic("version catalogue must contain a stable default")
}

func (v OCPVersion) IsPrerelease() bool {
	return strings.Contains(v.MicroShiftTag, "-okd-scos.ec.") || strings.Contains(v.MicroShiftTag, "-okd-scos.rc.")
}

var okdTagPattern = regexp.MustCompile(`^([0-9]+\.[0-9]+)\.[0-9]+-okd-scos\.(?:(?:ec|rc)\.)?[0-9]+$`)

func Resolve(v string) (OCPVersion, error) {
	if v == "" {
		return Default(), nil
	}
	for _, ver := range catalogue {
		if ver.Version == v {
			return ver, nil
		}
	}
	// Full OKD tags reuse the minor's Console/API compatibility settings.
	// The image must have been built and published separately.
	if match := okdTagPattern.FindStringSubmatch(v); match != nil && orderVersion(v) != nil {
		for _, ver := range catalogue {
			if ver.Version == match[1] {
				ver.Version = v
				if v != ver.MicroShiftTag {
					ver.ImageTag = ""
				}
				ver.MicroShiftTag = v
				return ver, nil
			}
		}
	}
	var available []string
	for _, ver := range catalogue {
		available = append(available, ver.Version)
	}
	return OCPVersion{}, fmt.Errorf("version %s not available. available minors: %v; or use a full OKD tag for one of these minors (oinc version list --remote)", v, available)
}

const (
	ImageRegistry   = "ghcr.io/jasonmadigan/oinc"
	ConsoleRegistry = "ghcr.io/jasonmadigan/oinc-console"
	ConsoleImage    = "quay.io/openshift/origin-console"
)

func (v OCPVersion) Arch() string {
	for _, a := range v.Arches {
		if a == runtime.GOARCH {
			return a
		}
	}
	// fall back to first supported arch (amd64 via rosetta on apple silicon)
	return v.Arches[0]
}

// Platform returns the OCI platform string, or empty if native.
func (v OCPVersion) Platform() string {
	if v.Arch() != runtime.GOARCH {
		return "linux/" + v.Arch()
	}
	return ""
}

func (v OCPVersion) MicroShiftImage() string {
	return fmt.Sprintf("%s:%s-%s", ImageRegistry, v.imageTag(), v.Arch())
}

func (v OCPVersion) imageTag() string {
	if v.ImageTag != "" {
		return v.ImageTag
	}
	return v.MicroShiftTag
}

// ConsoleImageRef returns the console image and platform for this host.
func (v OCPVersion) ConsoleImageRef() (image, platform string) {
	return v.consoleImageFor(runtime.GOARCH)
}

// origin-console only publishes amd64, which ARM hosts run emulated. From 5.0
// the console base is CentOS Stream 10, whose glibc needs x86-64-v3 and fails
// under that emulation, so those minors pin an image per architecture.
func (v OCPVersion) consoleImageFor(arch string) (image, platform string) {
	if v.ConsoleImages == nil {
		return fmt.Sprintf("%s:%s", ConsoleImage, v.ConsoleTag), "linux/amd64"
	}
	if image, ok := v.ConsoleImages[arch]; ok {
		return image, "linux/" + arch
	}
	return v.ConsoleImages["amd64"], "linux/amd64"
}

func (v OCPVersion) ConsolePluginCRDURL() string {
	return fmt.Sprintf(
		"https://raw.githubusercontent.com/openshift/api/%s/console/v1/zz_generated.crd-manifests/90_consoleplugins.crd.yaml",
		v.APIBranch,
	)
}

// ResolveFromImage recognises both pinned and newer OKD images.
func ResolveFromImage(image string) (OCPVersion, bool) {
	image = strings.SplitN(image, "@", 2)[0]
	colon := strings.LastIndex(image, ":")
	if colon <= strings.LastIndex(image, "/") {
		return OCPVersion{}, false
	}
	tag, ok := strings.CutSuffix(image[colon+1:], "-amd64")
	if !ok {
		tag, ok = strings.CutSuffix(image[colon+1:], "-arm64")
		if !ok {
			return OCPVersion{}, false
		}
	}
	for _, v := range catalogue {
		if tag == v.MicroShiftTag || tag == v.imageTag() {
			return v, true
		}
	}
	v, err := Resolve(tag)
	return v, err == nil
}
