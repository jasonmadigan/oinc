package addons

import (
	"context"
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
	bin := t.TempDir()
	// Replay the ownership conflict if Install attempts a second Helm release.
	script := `#!/bin/sh
if [ "$1" != template ]; then
  echo 'conflict with "kuadrant-operator": .spec.versions' >&2
  exit 1
fi
printf '%s\n' "$@" > "$MCP_TEST_ARGS"
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
EOF
`
	if err := os.WriteFile(filepath.Join(bin, "helm"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	argsFile := filepath.Join(bin, "args")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("MCP_TEST_ARGS", argsFile)
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
	var applied bool
	client.PrependReactor("patch", "mcpgatewayextensions", func(action k8stesting.Action) (bool, kruntime.Object, error) {
		if action.GetResource().Version != "v1" {
			t.Errorf("extension uses unserved API %s", action.GetResource().Version)
		}
		applied = true
		return true, &unstructured.Unstructured{}, nil
	})
	cfg := &Config{DynamicClient: client, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err := (&mcpGateway{}).Install(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if !applied {
		t.Fatal("MCPGatewayExtension was not applied")
	}
	args, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(args), "controller.enabled=false") || !strings.Contains(string(args), "--skip-tests") {
		t.Fatalf("instance render must disable the controller and test pods: %s", args)
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
	args := (&mcpGateway{version: "0.9.0", valuesFile: "/tmp/values.yaml"}).templateArgs()
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
