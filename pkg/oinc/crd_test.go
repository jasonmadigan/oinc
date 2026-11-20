package oinc

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jasonmadigan/oinc/pkg/download"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kscheme "k8s.io/client-go/kubernetes/scheme"
	k8stesting "k8s.io/client-go/testing"
)

const rawGitHub = "https://raw.githubusercontent.com/"

// fakeRawGitHub puts a curl on PATH that serves raw.githubusercontent.com
// paths from the returned directory, failing like curl --fail on anything
// missing, and reports every URL it was asked for.
func fakeRawGitHub(t *testing.T) (root string, fetched func() []string) {
	t.Helper()
	dir := t.TempDir()
	root = filepath.Join(dir, "raw")
	bin := filepath.Join(dir, "bin")
	log := filepath.Join(dir, "fetched")
	for _, d := range []string{root, bin} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	script := `#!/bin/sh
for url; do :; done
printf '%s\n' "$url" >> "$OINC_TEST_FETCHED"
file="$OINC_TEST_RAW/${url#` + rawGitHub + `}"
if [ ! -f "$file" ]; then
	echo "curl: (22) The requested URL returned error: 404" >&2
	exit 22
fi
cat "$file"
`
	if err := os.WriteFile(filepath.Join(bin, "curl"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("OINC_TEST_RAW", root)
	t.Setenv("OINC_TEST_FETCHED", log)

	return root, func() []string {
		data, err := os.ReadFile(log)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			t.Fatal(err)
		}
		return strings.Fields(string(data))
	}
}

// serve publishes content at a raw.githubusercontent.com path.
func serve(t *testing.T, root, path, content string) {
	t.Helper()
	file := filepath.Join(root, path)
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// crdManifestYAML is an established CRD carrying marker, so a test can tell
// which copy of it the cluster holds.
func crdManifestYAML(name, marker string) string {
	return fmt.Sprintf(`apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: %s
  annotations:
    test.oinc/manifest: %s
status:
  conditions:
  - type: Established
    status: "True"
`, name, marker)
}

func crdObject(name, marker string, established bool) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apiextensions.k8s.io/v1",
		"kind":       "CustomResourceDefinition",
		"metadata": map[string]any{
			"name":        name,
			"annotations": map[string]any{"test.oinc/manifest": marker},
		},
	}}
	if established {
		obj.Object["status"] = map[string]any{"conditions": []any{
			map[string]any{"type": "Established", "status": "True"},
		}}
	}
	return obj
}

func fakeCRDClient(objects ...runtime.Object) *dynamicfake.FakeDynamicClient {
	return dynamicfake.NewSimpleDynamicClient(kscheme.Scheme, objects...)
}

func crdMarker(t *testing.T, client *dynamicfake.FakeDynamicClient, name string) string {
	t.Helper()
	crd, err := client.Resource(crdGVR).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("getting CRD %s: %v", name, err)
	}
	return crd.GetAnnotations()["test.oinc/manifest"]
}

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(devNull{}, nil)) }

// noDownloadRetries makes a failed download fail at once.
func noDownloadRetries(t *testing.T) {
	t.Helper()
	saved := download.Delays
	download.Delays = nil
	t.Cleanup(func() { download.Delays = saved })
}

func TestApplyCRDCreatesAndUpdates(t *testing.T) {
	root, _ := fakeRawGitHub(t)
	const name = "widgets.example.com"
	url := rawGitHub + "example/repo/main/widgets.yaml"
	client := fakeCRDClient()

	serve(t, root, "example/repo/main/widgets.yaml", crdManifestYAML(name, "first"))
	if err := applyCRD(context.Background(), client, discardLogger(), url, name); err != nil {
		t.Fatalf("creating: %v", err)
	}
	if got := crdMarker(t, client, name); got != "first" {
		t.Fatalf("created CRD marker = %q, want first", got)
	}

	serve(t, root, "example/repo/main/widgets.yaml", crdManifestYAML(name, "second"))
	if err := applyCRD(context.Background(), client, discardLogger(), url, name); err != nil {
		t.Fatalf("updating: %v", err)
	}
	if got := crdMarker(t, client, name); got != "second" {
		t.Errorf("updated CRD marker = %q, want second", got)
	}
}

// service-ca writes the CRD it injects a CA into, which can land between the
// read and the update
func TestApplyCRDRetriesAConflictingUpdate(t *testing.T) {
	root, _ := fakeRawGitHub(t)
	const name = "widgets.example.com"
	serve(t, root, "example/repo/main/widgets.yaml", crdManifestYAML(name, "new"))
	client := fakeCRDClient(crdObject(name, "old", true))

	conflicts := 0
	client.PrependReactor("update", "customresourcedefinitions", func(k8stesting.Action) (bool, runtime.Object, error) {
		if conflicts == 0 {
			conflicts++
			return true, nil, errors.NewConflict(crdGVR.GroupResource(), name, fmt.Errorf("written meanwhile"))
		}
		return false, nil, nil
	})

	if err := applyCRD(context.Background(), client, discardLogger(), rawGitHub+"example/repo/main/widgets.yaml", name); err != nil {
		t.Fatalf("applyCRD: %v", err)
	}
	if conflicts != 1 {
		t.Fatalf("conflicts = %d, want the one injected", conflicts)
	}
	if got := crdMarker(t, client, name); got != "new" {
		t.Errorf("CRD marker = %q, want new", got)
	}
}

func TestDecodeCRDRejectsAnythingButTheNamedCRD(t *testing.T) {
	const name = "widgets.example.com"
	cases := map[string]string{
		"another CRD": crdManifestYAML("gadgets.example.com", "x"),
		"another kind": `apiVersion: v1
kind: ConfigMap
metadata:
  name: widgets.example.com
`,
		"two documents": crdManifestYAML(name, "x") + "---\n" + crdManifestYAML(name, "y"),
		"empty":         "---\n",
	}
	for desc, manifest := range cases {
		if _, err := decodeCRD([]byte(manifest), name); err == nil {
			t.Errorf("%s: decoded, want an error", desc)
		}
	}

	crd, err := decodeCRD([]byte("---\n"+crdManifestYAML(name, "x")), name)
	if err != nil {
		t.Fatalf("leading separator: %v", err)
	}
	if crd.GetName() != name {
		t.Errorf("decoded %q, want %q", crd.GetName(), name)
	}
}

func TestApplyCRDLeavesTheClusterAloneWhenTheManifestIsWrong(t *testing.T) {
	root, _ := fakeRawGitHub(t)
	serve(t, root, "example/repo/main/widgets.yaml", crdManifestYAML("gadgets.example.com", "x"))
	client := fakeCRDClient()

	err := applyCRD(context.Background(), client, discardLogger(), rawGitHub+"example/repo/main/widgets.yaml", "widgets.example.com")
	if err == nil || !strings.Contains(err.Error(), "gadgets.example.com") || !strings.Contains(err.Error(), "widgets.example.com") {
		t.Fatalf("err = %v, want both names", err)
	}
	if actions := client.Actions(); len(actions) != 0 {
		t.Errorf("cluster actions = %v, want none", actions)
	}
}

func TestWaitForCRDsEstablished(t *testing.T) {
	client := fakeCRDClient(
		crdObject("ready.example.com", "x", true),
		crdObject("pending.example.com", "x", false),
	)
	ctx := context.Background()

	if err := waitForCRDsEstablished(ctx, client, []string{"ready.example.com"}, time.Second, time.Millisecond); err != nil {
		t.Fatalf("established CRD: %v", err)
	}
	err := waitForCRDsEstablished(ctx, client, []string{"ready.example.com", "pending.example.com"}, 20*time.Millisecond, time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "pending.example.com") {
		t.Errorf("err = %v, want the pending CRD named", err)
	}
	err = waitForCRDsEstablished(ctx, client, []string{"absent.example.com"}, 20*time.Millisecond, time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "absent.example.com") || !strings.Contains(err.Error(), "not found") {
		t.Errorf("err = %v, want the absent CRD named with the cause", err)
	}
}
