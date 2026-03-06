package version

import (
	"fmt"
	"runtime"
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
		Version:       "4.18",
		MicroShiftTag: "4.18.0-okd-scos.9",
		ConsoleTag:    "4.18",
		APIBranch:     "release-4.18",
		Arches:        []string{"amd64"},
	},
	{
		Version:       "4.19",
		MicroShiftTag: "4.19.0-okd-scos.17",
		ConsoleTag:    "4.19",
		APIBranch:     "release-4.19",
		Arches:        []string{"amd64"},
	},
	{
		Version:       "4.20",
		MicroShiftTag: "4.20.0-okd-scos.16",
		ConsoleTag:    "4.20",
		APIBranch:     "release-4.20",
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

func (v OCPVersion) MicroShiftImage() string {
	return fmt.Sprintf("%s:%s-%s", ImageRegistry, v.MicroShiftTag, runtime.GOARCH)
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
