package addons

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/yaml"
)

func fakeWorkload(kind, name string, containers ...map[string]any) *unstructured.Unstructured {
	specs := make([]any, 0, len(containers))
	for _, c := range containers {
		specs = append(specs, c)
	}
	return &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "apps/v1",
			"kind":       kind,
			"metadata": map[string]any{
				"name":      name,
				"namespace": "metallb-system",
			},
			"spec": map[string]any{
				"template": map[string]any{
					"spec": map[string]any{
						"containers": specs,
					},
				},
			},
		},
	}
}

func workloadArgs(t *testing.T, obj *unstructured.Unstructured, container string) []string {
	t.Helper()
	containers, _, _ := unstructured.NestedSlice(obj.Object, "spec", "template", "spec", "containers")
	for _, c := range containers {
		cm, ok := c.(map[string]any)
		if !ok || cm["name"] != container {
			continue
		}
		var args []string
		raw, _ := cm["args"].([]any)
		for _, a := range raw {
			args = append(args, a.(string))
		}
		return args
	}
	t.Fatalf("container %q not found", container)
	return nil
}

// metallb must not claim class-less LoadBalancer services: with no address
// pools it clears the status microshift's built-in service-lb wrote for
// openshift-ingress/router-default, tearing down the host port bindings that
// Routes rely on.
func TestEnsureContainerArg(t *testing.T) {
	dep := fakeWorkload("Deployment", "controller",
		map[string]any{"name": "controller", "args": []any{"--port=7472"}})
	client := dynamicfake.NewSimpleDynamicClient(kscheme.Scheme, dep)
	cfg := &Config{
		DynamicClient: client,
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	arg := "--lb-class=" + metalLBClass
	for range 2 { // second call must be a no-op
		if err := ensureContainerArg(context.Background(), cfg, deploymentGVR, "metallb-system", "controller", "controller", arg); err != nil {
			t.Fatalf("ensureContainerArg: %v", err)
		}
	}

	got, err := client.Resource(deploymentGVR).Namespace("metallb-system").Get(context.Background(), "controller", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	args := workloadArgs(t, got, "controller")
	count := 0
	for _, a := range args {
		if a == arg {
			count++
		}
	}
	if count != 1 {
		t.Errorf("args = %v, want exactly one %q", args, arg)
	}
	if args[0] != "--port=7472" {
		t.Errorf("args = %v, existing args must be preserved", args)
	}
}

func TestEnsureContainerArgTargetsNamedContainer(t *testing.T) {
	ds := fakeWorkload("DaemonSet", "speaker",
		map[string]any{"name": "frr", "args": []any{}},
		map[string]any{"name": "speaker", "args": []any{"--port=7472"}})
	client := dynamicfake.NewSimpleDynamicClient(kscheme.Scheme, ds)
	cfg := &Config{
		DynamicClient: client,
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	arg := "--lb-class=" + metalLBClass
	if err := ensureContainerArg(context.Background(), cfg, daemonSetGVR, "metallb-system", "speaker", "speaker", arg); err != nil {
		t.Fatalf("ensureContainerArg: %v", err)
	}

	got, err := client.Resource(daemonSetGVR).Namespace("metallb-system").Get(context.Background(), "speaker", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if args := workloadArgs(t, got, "frr"); len(args) != 0 {
		t.Errorf("frr args = %v, other containers must be untouched", args)
	}
	found := false
	for _, a := range workloadArgs(t, got, "speaker") {
		if a == arg {
			found = true
		}
	}
	if !found {
		t.Error("speaker container missing the lb-class arg")
	}
}

const lbClassFixture = `# metallb-native.yaml fixture
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: ipaddresspools.metallb.io
spec:
  group: metallb.io
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: controller
  namespace: metallb-system
spec:
  template:
    spec:
      containers:
      - name: controller
        args:
        - --port=7472
        - --log-level=info
---
apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: speaker
  namespace: metallb-system
spec:
  template:
    spec:
      containers:
      - name: frr
        args: []
      - name: speaker
        args:
        - --port=7472
`

func manifestArgs(t *testing.T, manifest []byte, kind, name, container string) []string {
	t.Helper()
	for _, doc := range strings.Split(string(manifest), "\n---\n") {
		var obj map[string]any
		if err := yaml.Unmarshal([]byte(doc), &obj); err != nil || obj == nil {
			continue
		}
		if obj["kind"] != kind {
			continue
		}
		u := &unstructured.Unstructured{Object: obj}
		if u.GetName() != name {
			continue
		}
		return workloadArgs(t, u, container)
	}
	t.Fatalf("%s %s not found in manifest", kind, name)
	return nil
}

// the scoping arg must be present in the manifest bytes before apply: the
// first pod ever created has to be scoped already, and server-side apply
// ownership of the args list must include it so re-installs do not strip it.
func TestInjectLBClassArg(t *testing.T) {
	out, err := injectLBClassArg([]byte(lbClassFixture))
	if err != nil {
		t.Fatalf("injectLBClassArg: %v", err)
	}

	arg := "--lb-class=" + metalLBClass
	for _, tt := range []struct {
		kind, name, container string
		want                  bool
	}{
		{"Deployment", "controller", "controller", true},
		{"DaemonSet", "speaker", "speaker", true},
		{"DaemonSet", "speaker", "frr", false},
	} {
		count := 0
		for _, a := range manifestArgs(t, out, tt.kind, tt.name, tt.container) {
			if a == arg {
				count++
			}
		}
		if tt.want && count != 1 {
			t.Errorf("%s/%s container %s: arg count = %d, want 1", tt.kind, tt.name, tt.container, count)
		}
		if !tt.want && count != 0 {
			t.Errorf("%s/%s container %s: arg count = %d, want 0", tt.kind, tt.name, tt.container, count)
		}
	}

	// existing args preserved
	args := manifestArgs(t, out, "Deployment", "controller", "controller")
	if args[0] != "--port=7472" || args[1] != "--log-level=info" {
		t.Errorf("controller args = %v, existing args must be preserved", args)
	}

	// untouched docs pass through verbatim
	if !strings.Contains(string(out), "# metallb-native.yaml fixture") {
		t.Error("unmodified docs must pass through without re-marshalling")
	}
	if !strings.Contains(string(out), "name: ipaddresspools.metallb.io") {
		t.Error("CRD doc lost")
	}
}

func TestInjectLBClassArgIdempotent(t *testing.T) {
	once, err := injectLBClassArg([]byte(lbClassFixture))
	if err != nil {
		t.Fatal(err)
	}
	twice, err := injectLBClassArg(once)
	if err != nil {
		t.Fatal(err)
	}
	arg := "--lb-class=" + metalLBClass
	count := 0
	for _, a := range manifestArgs(t, twice, "Deployment", "controller", "controller") {
		if a == arg {
			count++
		}
	}
	if count != 1 {
		t.Errorf("arg count after double inject = %d, want 1", count)
	}
}

func TestInjectLBClassArgMissingTarget(t *testing.T) {
	manifest := []byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: x\n")
	if _, err := injectLBClassArg(manifest); err == nil {
		t.Error("expected error when controller/speaker are missing from the manifest")
	}
}

func TestEnsureContainerArgMissingContainer(t *testing.T) {
	dep := fakeWorkload("Deployment", "controller",
		map[string]any{"name": "other", "args": []any{}})
	client := dynamicfake.NewSimpleDynamicClient(kscheme.Scheme, dep)
	cfg := &Config{
		DynamicClient: client,
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	if err := ensureContainerArg(context.Background(), cfg, deploymentGVR, "metallb-system", "controller", "controller", "--x"); err == nil {
		t.Error("expected error for missing container")
	}
}
