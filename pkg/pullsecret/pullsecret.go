package pullsecret

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const (
	configSubdir = "oinc"
	filename     = "pull-secret.json"
)

// PullSecretURL is where users can obtain a Red Hat pull secret.
const PullSecretURL = "https://cloud.redhat.com/openshift/install/pull-secret"

// configDir returns the platform config dir for oinc
// (macOS: ~/Library/Application Support/oinc, Linux: ~/.config/oinc).
func configDir() (string, error) {
	if dir := os.Getenv("OINC_CONFIG_DIR"); dir != "" {
		return filepath.Abs(dir)
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, configSubdir), nil
}

// Path returns the full path to the stored pull secret.
func Path() (string, error) {
	dir, err := configDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, filename), nil
}

// Exists returns true if a pull secret is configured.
func Exists() bool {
	p, err := Path()
	if err != nil {
		return false
	}
	_, err = os.Stat(p)
	return err == nil
}

// Load reads the stored pull secret bytes.
func Load() ([]byte, error) {
	p, err := Path()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, fmt.Errorf("no pull secret configured. get one from %s and run: oinc pull-secret set <path>", PullSecretURL)
	}
	if err := validate(data); err != nil {
		return nil, err
	}
	return data, nil
}

// Save stores a pull secret from the given file path.
func Save(srcPath string) error {
	data, err := os.ReadFile(srcPath)
	if err != nil {
		return fmt.Errorf("reading %s: %w", srcPath, err)
	}

	if err := validate(data); err != nil {
		return err
	}

	dir, err := configDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("creating config dir: %w", err)
	}

	dst, err := Path()
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".pull-secret-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), dst)
}

// Remove deletes the stored pull secret.
func Remove() error {
	p, err := Path()
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// HasRegistry checks if the stored pull secret has credentials for a specific registry.
func HasRegistry(registry string) bool {
	data, err := Load()
	if err != nil {
		return false
	}
	var parsed struct {
		Auths map[string]any `json:"auths"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return false
	}
	_, ok := parsed.Auths[registry]
	return ok
}

func validate(data []byte) error {
	var secret struct {
		Auths map[string]struct {
			Auth          string `json:"auth"`
			IdentityToken string `json:"identitytoken"`
		} `json:"auths"`
	}
	if json.Unmarshal(data, &secret) != nil {
		return fmt.Errorf("pull secret must be valid Docker auth JSON")
	}
	if len(secret.Auths) == 0 {
		return fmt.Errorf("pull secret must contain registry credentials in 'auths'")
	}
	for _, entry := range secret.Auths {
		if entry.Auth == "" && entry.IdentityToken == "" {
			return fmt.Errorf("pull secret contains an empty registry credential")
		}
	}
	return nil
}

// RequiredPath validates the credentials needed by Red Hat preview components.
func RequiredPath() (string, error) {
	if _, err := Load(); err != nil {
		return "", err
	}
	if !HasRegistry("quay.io") {
		return "", fmt.Errorf("pull secret has no quay.io credentials; run oinc pull-secret set <path>")
	}
	return Path()
}
