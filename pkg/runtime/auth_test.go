package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAuthenticatedPullKeepsContextAndRemovesTemporaryConfig(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[failure], func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("DOCKER_CONFIG", filepath.Join(dir, "existing"))
			if err := os.MkdirAll(filepath.Join(dir, "existing", "contexts"), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("OINC_TEST_CONFIG_PATH", filepath.Join(dir, "config-path"))
			t.Setenv("OINC_TEST_EXIT", map[bool]string{false: "0", true: "1"}[failure])
			fake := filepath.Join(dir, "docker")
			script := `#!/bin/sh
if [ "$1" = image ]; then exit 1; fi
if [ "$1" = context ]; then echo orbstack; exit 0; fi
[ "$1" = --config ] || exit 2
printf '%s' "$2" > "$OINC_TEST_CONFIG_PATH"
[ -d "$2/contexts" ] || exit 3
[ "$3" = pull ] || exit 4
python3 - "$2/config.json" <<'PY'
import json,sys,os,stat
p=sys.argv[1]
c=json.load(open(p))
assert c['currentContext']=='orbstack'
assert c['auths']['quay.io']['auth']=='test-credential'
assert 'credsStore' not in c
assert stat.S_IMODE(os.stat(p).st_mode)==0o600
PY
[ "$?" = 0 ] || exit 5
exit "$OINC_TEST_EXIT"
`
			if err := os.WriteFile(fake, []byte(script), 0755); err != nil {
				t.Fatal(err)
			}
			auth := filepath.Join(dir, "secret.json")
			if err := os.WriteFile(auth, []byte(`{"auths":{"quay.io":{"auth":"test-credential"}},"credsStore":"unused"}`), 0600); err != nil {
				t.Fatal(err)
			}
			rt := &Runtime{binary: fake}
			err := rt.PullImageWithAuth("quay.io/example/image@sha256:abc", "linux/arm64", auth)
			if (err != nil) != failure {
				t.Fatalf("pull error = %v", err)
			}
			if err != nil && strings.Contains(err.Error(), "test-credential") {
				t.Fatal("credentials exposed")
			}
			config, err := os.ReadFile(filepath.Join(dir, "config-path"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(string(config)); !os.IsNotExist(err) {
				t.Fatalf("temporary auth remains: %v", err)
			}
		})
	}
}

func TestPodmanAuthenticatedPull(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "podman")
	args := filepath.Join(dir, "args")
	t.Setenv("OINC_TEST_ARGS", args)
	if err := os.WriteFile(fake, []byte("#!/bin/sh\n[ \"$1\" = image ] && exit 1\nprintf '%s\\n' \"$@\" > \"$OINC_TEST_ARGS\"\n"), 0755); err != nil {
		t.Fatal(err)
	}
	rt := &Runtime{binary: fake}
	if err := rt.PullImageWithAuth("example/image", "linux/arm64", "/private/secret.json"); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(args)
	if string(got) != "pull\n--platform\nlinux/arm64\n--authfile\n/private/secret.json\nexample/image\n" {
		t.Fatalf("args: %s", got)
	}
}
