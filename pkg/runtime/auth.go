package runtime

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// PullImageWithAuth uses a private, temporary auth configuration without logging
// credentials or changing the user's Docker/Podman login state.
func (r *Runtime) PullImageWithAuth(image, platform, authPath string) error {
	if r.ImageExists(image) {
		return nil
	}
	args := []string{"pull"}
	if platform != "" {
		args = append(args, "--platform", platform)
	}
	if r.isPodman() {
		args = append(args, "--authfile", authPath, image)
		_, err := r.run(args...)
		return err
	}
	data, err := os.ReadFile(authPath)
	if err != nil {
		return fmt.Errorf("reading pull secret: %w", err)
	}
	var config map[string]json.RawMessage
	if json.Unmarshal(data, &config) != nil || config == nil {
		return fmt.Errorf("invalid pull secret JSON")
	}
	// Keep the selected Docker endpoint, including its TLS material. Only auths
	// from the pull secret are copied; credential helpers are not consulted.
	selected, err := r.run("context", "show")
	if err != nil {
		return err
	}
	current, err := json.Marshal(strings.TrimSpace(string(selected)))
	if err != nil {
		return err
	}
	temporary, err := os.MkdirTemp("", "oinc-registry-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temporary)
	dockerConfig := os.Getenv("DOCKER_CONFIG")
	if dockerConfig == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		dockerConfig = filepath.Join(home, ".docker")
	}
	dockerConfig, err = filepath.Abs(dockerConfig)
	if err != nil {
		return err
	}
	if err := os.Symlink(filepath.Join(dockerConfig, "contexts"), filepath.Join(temporary, "contexts")); err != nil {
		return err
	}
	encoded, err := json.Marshal(map[string]json.RawMessage{"auths": config["auths"], "currentContext": current})
	if err != nil {
		return fmt.Errorf("encoding registry configuration")
	}
	if err := os.WriteFile(filepath.Join(temporary, "config.json"), encoded, 0600); err != nil {
		return err
	}
	args = append([]string{"--config", temporary}, args...)
	args = append(args, image)
	_, err = r.run(args...)
	return err
}
