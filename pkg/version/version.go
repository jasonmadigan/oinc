package version

import (
	"fmt"
	"runtime"
	"strings"
)

type OCPVersion struct {
	Version       string   // "4.18", "4.19", "4.20"
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
}

func All() []OCPVersion { return catalogue }

func Default() OCPVersion { return catalogue[len(catalogue)-1] }

func Resolve(v string) (OCPVersion, error) {
	if v == "" {
		return Default(), nil
	}
	for _, ver := range catalogue {
		if ver.Version == v {
			return ver, nil
		}
	}
	var available []string
	for _, ver := range catalogue {
		available = append(available, ver.Version)
	}
	return OCPVersion{}, fmt.Errorf("version %s not available. available: %v", v, available)
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

// ResolveFromImage matches a container image ref against the catalogue.
func ResolveFromImage(image string) (OCPVersion, bool) {
	parts := strings.SplitN(image, ":", 2)
	if len(parts) < 2 {
		return OCPVersion{}, false
	}
	tag := parts[1]
	for _, v := range catalogue {
		if strings.HasPrefix(tag, v.MicroShiftTag) {
			return v, true
		}
	}
	return OCPVersion{}, false
}

