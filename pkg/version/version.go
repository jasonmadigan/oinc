package version

import (
	"fmt"
	"regexp"
	"runtime"
	"strings"
)

type OCPVersion struct {
	Version       string   // minor version or full OKD tag
	MicroShiftTag string   // "4.18.0-okd-scos.9" (arch appended at runtime)
	ConsoleTag    string   // "4.18"
	APIBranch     string   // "release-4.18"
	Arches        []string // supported architectures
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
		MicroShiftTag: "5.0.0-okd-scos.ec.8",
		ConsoleTag:    "5.0",
		APIBranch:     "release-5.0",
		Arches:        []string{"amd64", "arm64"},
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
	ImageRegistry = "ghcr.io/jasonmadigan/oinc"
	ConsoleImage  = "quay.io/openshift/origin-console"
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
	return fmt.Sprintf("%s:%s-%s", ImageRegistry, v.MicroShiftTag, v.Arch())
}

func (v OCPVersion) ConsoleImageRef() string {
	return fmt.Sprintf("%s:%s", ConsoleImage, v.ConsoleTag)
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
		if tag == v.MicroShiftTag {
			return v, true
		}
	}
	v, err := Resolve(tag)
	return v, err == nil
}
