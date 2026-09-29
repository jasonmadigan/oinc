package pullsecret

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSaveLoadAndRemove(t *testing.T) {
	t.Setenv("OINC_CONFIG_DIR", t.TempDir())
	source := filepath.Join(t.TempDir(), "source.json")
	secret := []byte(`{"auths":{"quay.io":{"auth":"dXNlcjpwYXNzd29yZA=="}}}`)
	if err := os.WriteFile(source, secret, 0644); err != nil {
		t.Fatal(err)
	}
	path, _ := Path()
	// Replacing an existing permissive file must restore private permissions.
	if err := os.WriteFile(path, secret, 0644); err != nil {
		t.Fatal(err)
	}
	if err := Save(source); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatalf("permissions = %o", info.Mode().Perm())
	}
	data, err := Load()
	if err != nil || string(data) != string(secret) {
		t.Fatalf("load failed: %v", err)
	}
	if _, err := RequiredPath(); err != nil {
		t.Fatal(err)
	}
	if err := Remove(); err != nil {
		t.Fatal(err)
	}
	if Exists() {
		t.Fatal("secret remains after remove")
	}
	if _, err := RequiredPath(); err == nil {
		t.Fatal("missing secret accepted")
	}
}

func TestRejectMalformedSecretsWithoutDisclosure(t *testing.T) {
	t.Setenv("OINC_CONFIG_DIR", t.TempDir())
	source := filepath.Join(t.TempDir(), "source.json")
	for _, data := range []string{`super-secret-invalid-json`, `{"auths":null}`, `{"auths":[]}`, `{"auths":{}}`, `{"auths":{"quay.io":{}}}`, `{"auths":"super-secret"}`} {
		if err := os.WriteFile(source, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		err := Save(source)
		if err == nil {
			t.Fatalf("invalid auth accepted: %s", data)
		}
		if strings.Contains(err.Error(), "super-secret") {
			t.Fatal("error exposed credential contents")
		}
	}
}
