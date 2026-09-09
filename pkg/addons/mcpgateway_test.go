package addons

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	kruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kscheme "k8s.io/client-go/kubernetes/scheme"
	k8stesting "k8s.io/client-go/testing"
)

func TestMCPGatewayInstallReusesKuadrant(t *testing.T) {
	for _, version := range []string{"", "0.9.0", "latest"} {
		t.Run("version="+version, func(t *testing.T) {
			testMCPGatewayInstallReusesKuadrant(t, version)
		})
	}
}

func testMCPGatewayInstallReusesKuadrant(t *testing.T, version string) {
	bin := t.TempDir()
	// Helm 4.2.4 writes OCI registry messages to stdout. A separate pull
	// may emit those messages, but rendering the local archive must not.
	script := `#!/bin/sh
set -eu
case "$1" in
  pull)
    printf '%s\n' "$@" > "$MCP_TEST_PULL_ARGS"
    while [ "$1" != --destination ]; do shift; done
    mkdir -p "$2"
    touch "$2/mcp-gateway-0.8.0.tgz"
    printf 'Pulled: ghcr.io/kuadrant/charts/mcp-gateway:0.8.0\nDigest: sha256:1234\n'
    exit 0
    ;;
  template)
    printf '%s\n' "$@" > "$MCP_TEST_ARGS"
    case "$3" in
      oci://*) printf 'Pulled: ghcr.io/kuadrant/charts/mcp-gateway:0.8.0\nDigest: sha256:1234\n---\n' ;;
      *) test -f "$3" ;;
    esac
    ;;
  *)
    echo 'conflict with "kuadrant-operator": .spec.versions' >&2
    exit 1
    ;;
esac
cat <<'EOF'
apiVersion: mcp.kuadrant.io/v1alpha1
kind: MCPGatewayExtension
metadata:
  name: mcp-gateway-extension
  namespace: mcp-gateway-system
spec:
  targetRef:
    group: gateway.networking.k8s.io
    kind: Gateway
    name: mcp-gateway
    namespace: gateway-system
    sectionName: mcp
  privateHost: mcp-gateway-istio.gateway-system.svc.cluster.local:80
---
apiVersion: gateway.networking.k8s.io/v1beta1
kind: ReferenceGrant
metadata:
  name: instance-grant
  namespace: mcp-gateway-system
spec:
  from:
    - group: gateway.networking.k8s.io
      kind: Gateway
      namespace: gateway-system
  to:
    - group: ""
      kind: Service
EOF
`
	if err := os.WriteFile(filepath.Join(bin, "helm"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	argsFile := filepath.Join(bin, "args")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("MCP_TEST_ARGS", argsFile)
	t.Setenv("MCP_TEST_PULL_ARGS", filepath.Join(bin, "pull-args"))
	crd := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apiextensions.k8s.io/v1", "kind": "CustomResourceDefinition",
		"metadata": map[string]any{
			"name":   "mcpgatewayextensions.mcp.kuadrant.io",
			"labels": map[string]any{"app.kubernetes.io/managed-by": "kuadrant-operator"},
		},
		"spec": map[string]any{"versions": []any{
			map[string]any{"name": "v1", "served": true, "storage": true},
			map[string]any{"name": "v1alpha1", "served": false, "storage": false},
		}},
	}}
	client := dynamicfake.NewSimpleDynamicClient(kscheme.Scheme, crd)
	applied := map[string]bool{}
	client.PrependReactor("patch", "*", func(action k8stesting.Action) (bool, kruntime.Object, error) {
		if action.GetResource().Resource == "mcpgatewayextensions" && action.GetResource().Version != "v1" {
			t.Errorf("extension uses unserved API %s", action.GetResource().Version)
		}
		obj := &unstructured.Unstructured{}
		if err := json.Unmarshal(action.(k8stesting.PatchAction).GetPatch(), &obj.Object); err != nil {
			t.Fatal(err)
		}
		if obj.GetLabels()["app.kubernetes.io/managed-by"] != "oinc" {
			t.Errorf("instance ownership label missing: %v", obj.GetLabels())
		}
		applied[action.GetResource().Resource+"/"+obj.GetName()] = true
		return true, obj, nil
	})
	cfg := &Config{DynamicClient: client, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	valuesFile := filepath.Join(bin, "custom values.yaml")
	if err := os.WriteFile(valuesFile, []byte("controller:\n  enabled: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := &mcpGateway{version: version, valuesFile: valuesFile}
	if err := m.Install(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if len(applied) != 2 || !applied["mcpgatewayextensions/mcp-gateway-extension"] || !applied["referencegrants/instance-grant"] {
		t.Fatalf("expected both instance resources to be applied, got %v", applied)
	}
	pullArgs, err := os.ReadFile(filepath.Join(bin, "pull-args"))
	if err != nil {
		t.Fatal(err)
	}
	pull := strings.Split(strings.TrimSpace(string(pullArgs)), "\n")
	if len(pull) < 4 || pull[0] != "pull" || pull[1] != mcpGatewayChartOCI || pull[2] != "--destination" {
		t.Fatalf("unexpected pull command: %q", pull)
	}
	if version == "latest" {
		if strings.Contains(string(pullArgs), "--version") {
			t.Fatalf("latest must not be passed as a literal version: %s", pullArgs)
		}
	} else if !strings.Contains(string(pullArgs), "--version\n"+m.resolveVersion()+"\n") {
		t.Fatalf("pull lost the selected version: %s", pullArgs)
	}
	if _, err := os.Stat(pull[3]); !os.IsNotExist(err) {
		t.Fatalf("chart directory was not removed: %v", err)
	}
	args, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(args), "controller.enabled=false") || !strings.Contains(string(args), "--skip-tests") {
		t.Fatalf("instance render must disable the controller and test pods: %s", args)
	}
	if !strings.HasPrefix(string(args), "template\nmcp-gateway\n"+filepath.Join(pull[3], "mcp-gateway-0.8.0.tgz")+"\n") {
		t.Fatalf("render must use the downloaded archive: %s", args)
	}
	valuesAt := strings.Index(string(args), "--values\n"+valuesFile+"\n")
	if valuesAt < 0 || strings.Index(string(args), "controller.enabled=false") < valuesAt || strings.Index(string(args), "gateway.port=80") < valuesAt {
		t.Fatalf("managed overrides must follow user values: %s", args)
	}
	for _, action := range client.Actions() {
		if action.GetVerb() == "get" {
			continue
		}
		if action.GetResource() == (schema.GroupVersionResource{Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions"}) || action.GetResource() == deploymentGVR {
			t.Fatalf("mutated an operator-owned resource: %v", action)
		}
	}
}

func TestMCPGatewayInstallStandaloneAndDetectionErrors(t *testing.T) {
	for _, failedProbe := range []bool{false, true} {
		t.Run(fmt.Sprintf("failed ownership probe=%t", failedProbe), func(t *testing.T) {
			bin := t.TempDir()
			argsFile := filepath.Join(bin, "args")
			if err := os.WriteFile(filepath.Join(bin, "helm"), []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$MCP_TEST_ARGS\"\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("MCP_TEST_ARGS", argsFile)
			client := dynamicfake.NewSimpleDynamicClient(kscheme.Scheme)
			if failedProbe {
				client.PrependReactor("get", "customresourcedefinitions", func(k8stesting.Action) (bool, kruntime.Object, error) {
					return true, nil, fmt.Errorf("access denied")
				})
			}
			cfg := &Config{DynamicClient: client, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
			err := (&mcpGateway{version: "0.8.0"}).Install(context.Background(), cfg)
			if failedProbe {
				if err == nil || !strings.Contains(err.Error(), "access denied") {
					t.Fatalf("expected ownership probe error, got %v", err)
				}
				if _, err := os.Stat(argsFile); !os.IsNotExist(err) {
					t.Fatal("must not fall back to Helm install after a failed ownership probe")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			args, err := os.ReadFile(argsFile)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(string(args), "upgrade\n--install\n") || !strings.Contains(string(args), "--version\n0.8.0\n") {
				t.Fatalf("standalone install changed: %s", args)
			}
		})
	}
}

func TestMCPInstanceResourcesRejectControllerAndCRDs(t *testing.T) {
	for _, manifest := range []string{
		"apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: controller\n  namespace: mcp-gateway-system\n",
		"apiVersion: apiextensions.k8s.io/v1\nkind: CustomResourceDefinition\nmetadata:\n  name: mcpgatewayextensions.mcp.kuadrant.io\n",
	} {
		if _, err := mcpInstanceResources([]byte(manifest), "v1"); err == nil {
			t.Fatalf("accepted controller or CRD in instance render: %s", manifest)
		}
	}
}

func TestMCPGatewayTemplateOptions(t *testing.T) {
	args := (&mcpGateway{version: "0.9.0", valuesFile: "/tmp/values.yaml"}).templateArgs("/tmp/mcp-gateway.tgz")
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--version 0.9.0") || !strings.Contains(joined, "--values /tmp/values.yaml") {
		t.Fatalf("missing chart options: %s", joined)
	}
	if strings.Index(joined, "controller.enabled=false") < strings.Index(joined, "--values") {
		t.Fatalf("overlay can override controller ownership: %s", joined)
	}
}

func fakeMCPGateway(listeners []map[string]any) *unstructured.Unstructured {
	obj := map[string]any{
		"apiVersion": "gateway.networking.k8s.io/v1",
		"kind":       "Gateway",
		"metadata": map[string]any{
			"name":      "mcp-gateway",
			"namespace": gatewaySystemNamespace,
		},
	}
	if listeners != nil {
		obj["status"] = map[string]any{
			"listeners": func() []any {
				out := make([]any, len(listeners))
				for i, l := range listeners {
					out[i] = l
				}
				return out
			}(),
		}
	}
	return &unstructured.Unstructured{Object: obj}
}

func mcpGatewayReadyConfig(t *testing.T, gw *unstructured.Unstructured) *Config {
	t.Helper()
	client := dynamicfake.NewSimpleDynamicClient(kscheme.Scheme)
	if _, err := client.Resource(gatewayGVR).Namespace(gatewaySystemNamespace).Create(
		context.Background(), gw, metav1.CreateOptions{},
	); err != nil {
		t.Fatalf("seeding gateway: %v", err)
	}
	return &Config{
		DynamicClient: client,
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func TestWaitForMCPListenerReady(t *testing.T) {
	t.Run("mcp listener accepted", func(t *testing.T) {
		cfg := mcpGatewayReadyConfig(t, fakeMCPGateway([]map[string]any{
			{
				"name": "http",
				"conditions": []any{
					map[string]any{"type": "Accepted", "status": "True"},
				},
			},
			{
				"name": "mcp",
				"conditions": []any{
					map[string]any{"type": "Accepted", "status": "True"},
				},
			},
		}))
		err := waitForMCPListener(context.Background(), cfg, 50*time.Millisecond, 5*time.Millisecond)
		if err != nil {
			t.Fatalf("expected success, got: %v", err)
		}
	})

	t.Run("mcp listener not yet present", func(t *testing.T) {
		cfg := mcpGatewayReadyConfig(t, fakeMCPGateway([]map[string]any{
			{
				"name": "http",
				"conditions": []any{
					map[string]any{"type": "Accepted", "status": "True"},
				},
			},
		}))
		err := waitForMCPListener(context.Background(), cfg, 30*time.Millisecond, 5*time.Millisecond)
		if err == nil {
			t.Fatal("expected timeout when mcp listener is missing")
		}
		if !strings.Contains(err.Error(), "mcp") {
			t.Errorf("error %q should mention the mcp listener", err)
		}
	})

	t.Run("mcp listener not accepted", func(t *testing.T) {
		cfg := mcpGatewayReadyConfig(t, fakeMCPGateway([]map[string]any{
			{
				"name": "mcp",
				"conditions": []any{
					map[string]any{"type": "Accepted", "status": "False", "reason": "Pending"},
				},
			},
		}))
		err := waitForMCPListener(context.Background(), cfg, 30*time.Millisecond, 5*time.Millisecond)
		if err == nil {
			t.Fatal("expected timeout when mcp listener is not accepted")
		}
	})

	t.Run("no status yet", func(t *testing.T) {
		cfg := mcpGatewayReadyConfig(t, fakeMCPGateway(nil))
		err := waitForMCPListener(context.Background(), cfg, 30*time.Millisecond, 5*time.Millisecond)
		if err == nil {
			t.Fatal("expected timeout when gateway has no status")
		}
	})

	t.Run("context cancelled", func(t *testing.T) {
		cfg := mcpGatewayReadyConfig(t, fakeMCPGateway(nil))
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err := waitForMCPListener(ctx, cfg, time.Second, 5*time.Millisecond)
		if err == nil {
			t.Fatal("expected error on cancelled context")
		}
	})
}

func TestMCPGatewayHelmArgsUsePrerequisiteGatewayPort(t *testing.T) {
	args := (&mcpGateway{valuesFile: "/tmp/custom-values.yaml"}).helmArgs()
	joined := strings.Join(args, " ")

	valuesAt := strings.Index(joined, "--values /tmp/custom-values.yaml")
	portAt := strings.Index(joined, "--set gateway.port=80")
	if valuesAt == -1 {
		t.Fatalf("helm args %q do not include the values overlay", joined)
	}
	if portAt == -1 {
		t.Fatalf("helm args %q do not set the chart port to the prerequisite Gateway port", joined)
	}
	if portAt < valuesAt {
		t.Fatalf("gateway port override must follow the values overlay so the generated privateHost matches the Gateway: %q", joined)
	}
}

func TestMCPGatewayManagedFailuresCleanUpBeforeReturning(t *testing.T) {
	valid := "apiVersion: mcp.kuadrant.io/v1alpha1\nkind: MCPGatewayExtension\nmetadata:\n  name: instance\n  namespace: mcp-gateway-system\n"
	for _, tc := range []struct {
		name      string
		pullMode  string
		renderErr bool
		manifest  string
		applyErr  bool
		want      []string
	}{
		{name: "pull", pullMode: "fail", want: []string{"pulling mcp-gateway chart", "pull stdout detail", "pull stderr detail"}},
		{name: "render", renderErr: true, want: []string{"rendering mcp-gateway instance", "render stderr detail"}},
		{name: "missing archive", pullMode: "empty", want: []string{"expected one chart archive"}},
		{name: "multiple archives", pullMode: "multiple", want: []string{"expected one chart archive"}},
		{name: "unexpected file", pullMode: "wrong-file", want: []string{"expected one chart archive"}},
		{name: "missing directory", pullMode: "remove-dir", want: []string{"reading mcp-gateway chart directory"}},
		{name: "malformed YAML", manifest: valid + "---\napiVersion: [\n", want: []string{"decoding mcp-gateway instance"}},
		{name: "missing kind", manifest: valid + "---\nPulled: unexpected output\nDigest: sha256:1234\n", want: []string{"Kind", "missing"}},
		{name: "controller", manifest: valid + "---\napiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: controller\n  namespace: mcp-gateway-system\n", want: []string{"unexpected", "Deployment"}},
		{name: "CRD", manifest: valid + "---\napiVersion: apiextensions.k8s.io/v1\nkind: CustomResourceDefinition\nmetadata:\n  name: mcpgatewayextensions.mcp.kuadrant.io\n", want: []string{"unexpected", "CustomResourceDefinition"}},
		{name: "apply", manifest: valid, applyErr: true, want: []string{"applying MCPGatewayExtension", "apply denied"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bin := t.TempDir()
			chartRoot := t.TempDir()
			t.Setenv("TMPDIR", chartRoot)
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("MCP_PULL_MODE", tc.pullMode)
			t.Setenv("MCP_RENDER_ERROR", fmt.Sprint(tc.renderErr))
			manifestFile := filepath.Join(bin, "manifest.yaml")
			t.Setenv("MCP_MANIFEST", manifestFile)
			if err := os.WriteFile(manifestFile, []byte(tc.manifest), 0o600); err != nil {
				t.Fatal(err)
			}
			script := `#!/bin/sh
set -eu
case "$1" in
  pull)
    while [ "$1" != --destination ]; do shift; done
    destination=$2
    case "$MCP_PULL_MODE" in
      empty) exit 0 ;;
      remove-dir) rmdir "$destination"; exit 0 ;;
      wrong-file) touch "$destination/not-a-chart.txt"; exit 0 ;;
      multiple) touch "$destination/extra.tgz" ;;
    esac
    touch "$destination/chart.tgz"
    echo 'pull stdout detail'
    echo 'pull stderr detail' >&2
    if [ "$MCP_PULL_MODE" = fail ]; then exit 1; fi
    ;;
  template)
    test -f "$3"
    if [ "$MCP_RENDER_ERROR" = true ]; then
      echo 'render stderr detail' >&2
      exit 1
    fi
    cat "$MCP_MANIFEST"
    ;;
  *) exit 1 ;;
esac
`
			if err := os.WriteFile(filepath.Join(bin, "helm"), []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			client := dynamicfake.NewSimpleDynamicClient(kscheme.Scheme)
			if tc.applyErr {
				client.PrependReactor("patch", "*", func(k8stesting.Action) (bool, kruntime.Object, error) {
					return true, nil, fmt.Errorf("apply denied")
				})
			}
			cfg := &Config{DynamicClient: client, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
			err := (&mcpGateway{}).installManagedInstance(context.Background(), cfg, "v1")
			if err == nil {
				t.Fatal("expected install error")
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not contain %q", err, want)
				}
			}
			entries, err := os.ReadDir(chartRoot)
			if err != nil || len(entries) != 0 {
				t.Errorf("temporary chart files leaked: %v, %v", entries, err)
			}
			if !tc.applyErr {
				for _, action := range client.Actions() {
					if action.GetVerb() != "get" && action.GetVerb() != "list" {
						t.Errorf("wrote cluster resource before validating all chart output: %v", action)
					}
				}
			}
		})
	}
}
