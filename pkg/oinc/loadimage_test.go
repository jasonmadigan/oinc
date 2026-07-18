package oinc

import (
	"io"
	"log/slog"
	"strings"
	"testing"
)

func TestLoadImageRejectsDigestRefs(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	err := LoadImage("", "localhost/img@sha256:abc", logger)
	if err == nil {
		t.Fatal("LoadImage with digest ref = nil error, want rejection")
	}
	if !strings.Contains(err.Error(), "digest") {
		t.Errorf("LoadImage digest ref error = %q, want mention of digest refs", err)
	}
}

func TestNormalizeRef(t *testing.T) {
	tests := []struct {
		name string
		ref  string
		want string
	}{
		{"localhost ref unchanged", "localhost/my-image:dev", "localhost/my-image:dev"},
		{"registry ref unchanged", "quay.io/org/img:v1", "quay.io/org/img:v1"},
		{"registry with port unchanged", "registry:5000/img:v1", "registry:5000/img:v1"},
		{"bare name qualified", "busybox:latest", "docker.io/library/busybox:latest"},
		{"org name qualified", "myorg/img:v1", "docker.io/myorg/img:v1"},
		{"missing tag defaults to latest", "localhost/my-image", "localhost/my-image:latest"},
		{"bare name missing tag", "busybox", "docker.io/library/busybox:latest"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeRef(tt.ref)
			if got != tt.want {
				t.Errorf("normalizeRef(%q) = %q, want %q", tt.ref, got, tt.want)
			}
		})
	}
}

func TestRefInCrictlImages(t *testing.T) {
	out := []byte(`{
		"images": [
			{"id": "a", "repoTags": ["localhost/my-image:dev"], "repoDigests": []},
			{"id": "b", "repoTags": ["docker.io/library/busybox:latest"], "repoDigests": []},
			{"id": "c", "repoTags": [], "repoDigests": []}
		]
	}`)

	tests := []struct {
		name string
		ref  string
		want bool
	}{
		{"exact match", "localhost/my-image:dev", true},
		{"normalised match", "busybox:latest", true},
		{"absent", "localhost/other:dev", false},
		{"wrong tag", "localhost/my-image:v2", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := refInCrictlImages(out, tt.ref)
			if err != nil {
				t.Fatalf("refInCrictlImages(%q) error: %v", tt.ref, err)
			}
			if got != tt.want {
				t.Errorf("refInCrictlImages(%q) = %v, want %v", tt.ref, got, tt.want)
			}
		})
	}

	t.Run("invalid json is an error", func(t *testing.T) {
		_, err := refInCrictlImages([]byte("not json"), "localhost/my-image:dev")
		if err == nil {
			t.Error("refInCrictlImages on invalid json = nil error, want parse error")
		}
	})
}
