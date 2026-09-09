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
