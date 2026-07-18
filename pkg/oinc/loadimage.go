package oinc

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jasonmadigan/oinc/pkg/runtime"
)

// LoadImage streams a locally built image into the cluster's cri-o store,
// the kind load docker-image equivalent. The ref is preserved exactly so
// pods with imagePullPolicy: IfNotPresent resolve it without a registry.
func LoadImage(runtimeOverride, image string, logger *slog.Logger) error {
	// docker-archive re-serialises layers, so the manifest digest of the copy
	// can never match a digest ref and skopeo rejects the destination
	if strings.Contains(image, "@") {
		return fmt.Errorf("digest refs cannot be loaded (the archive round-trip changes the digest): use a tag instead of %q", image)
	}

	rt, err := runtime.DetectOwner(runtimeOverride, containerName)
	if err != nil {
		return err
	}
	if !rt.ContainerRunning(containerName) {
		return fmt.Errorf("oinc container found in %s but not running", rt.Name())
	}
	logger.Info("detected runtime owning the cluster", "runtime", rt.Name())

	if !rt.ImageExists(image) {
		return fmt.Errorf("image %q not found in %s (build or pull it first)", image, rt.Name())
	}

	if _, err := rt.ExecInContainer(containerName, "sh", "-c", "command -v skopeo"); err != nil {
		clusterImage := "unknown"
		if info, ierr := rt.InspectContainer(containerName); ierr == nil {
			clusterImage = info.Image
		}
		return fmt.Errorf("skopeo not found in the oinc container: the oinc image is expected to ship skopeo, but %s does not", clusterImage)
	}

	logger.Info("streaming image into the cluster", "image", image)
	if err := rt.StreamImageToContainer(image, containerName,
		"skopeo", "copy", "docker-archive:/dev/stdin", "containers-storage:"+image); err != nil {
		return fmt.Errorf("importing %s: %w", image, err)
	}

	out, err := rt.ExecInContainer(containerName, "crictl", "images", "--output", "json")
	if err != nil {
		return fmt.Errorf("listing cri-o images: %w", err)
	}
	present, err := refInCrictlImages(out, image)
	if err != nil {
		return fmt.Errorf("parsing crictl images output: %w", err)
	}
	if !present {
		return fmt.Errorf("image %s not present in cri-o after import", image)
	}

	logger.Info("image loaded", "image", image)
	return nil
}

// normalizeRef mirrors how containers-storage qualifies a tagged image ref, so
// verification can match what cri-o reports for refs given without a registry.
// digest refs are rejected before this is reached.
func normalizeRef(ref string) string {
	name := ref
	tag := "latest"
	if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		name = ref[:i]
		tag = ref[i+1:]
	}

	first, _, found := strings.Cut(name, "/")
	switch {
	case !found:
		name = "docker.io/library/" + name
	case !strings.ContainsAny(first, ".:") && first != "localhost":
		name = "docker.io/" + name
	}
	return name + ":" + tag
}

// refInCrictlImages reports whether ref, as given or storage-normalised,
// appears in `crictl images --output json` output.
func refInCrictlImages(out []byte, ref string) (bool, error) {
	var parsed struct {
		Images []struct {
			RepoTags []string `json:"repoTags"`
		} `json:"images"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		return false, err
	}

	want := normalizeRef(ref)
	for _, img := range parsed.Images {
		for _, tag := range img.RepoTags {
			if tag == ref || tag == want {
				return true, nil
			}
		}
	}
	return false, nil
}
