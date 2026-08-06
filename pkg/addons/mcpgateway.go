package addons

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	defaultMCPGatewayChartVersion = "0.8.0"
	mcpGatewayChartOCI            = "oci://ghcr.io/kuadrant/charts/mcp-gateway"
	mcpGatewayNamespace           = "mcp-gateway-system"
	mcpGatewayControllerDeploy    = "mcp-gateway-controller"
	gatewaySystemNamespace        = "gateway-system"
)

var referenceGrantGVR = schema.GroupVersionResource{
	Group: "gateway.networking.k8s.io", Version: "v1beta1", Resource: "referencegrants",
}

func init() { Register(&mcpGateway{}) }

type mcpGateway struct {
	version    string
	valuesFile string
}

func (m *mcpGateway) Name() string           { return "mcp-gateway" }
func (m *mcpGateway) Dependencies() []string { return []string{"kuadrant"} }

func (m *mcpGateway) SetOptions(opts map[string]string) {
	if v, ok := opts["version"]; ok {
		m.version = v
	}
	if v, ok := opts["values"]; ok {
		m.valuesFile = v
	}
}

func (m *mcpGateway) Validate() error {
	if m.valuesFile != "" {
		if _, err := os.Stat(m.valuesFile); err != nil {
			return fmt.Errorf("values overlay: %w", err)
		}
	}
	return nil
}

func (m *mcpGateway) resolveVersion() string {
	if m.version != "" {
		return m.version
	}
	return defaultMCPGatewayChartVersion
}

func (m *mcpGateway) chartVersionArgs() []string {
	v := m.resolveVersion()
	if v == "latest" {
		return nil
	}
	return []string{"--version", v}
}

func (m *mcpGateway) helmArgs() []string {
	args := []string{"upgrade", "--install", "mcp-gateway", mcpGatewayChartOCI,
		"--create-namespace",
		"-n", mcpGatewayNamespace,
	}
	if m.valuesFile != "" {
		args = append(args, "--values", m.valuesFile)
	}
	args = append(args, m.chartVersionArgs()...)
	return append(args, "--wait", "--timeout", "5m")
}

func (m *mcpGateway) Install(ctx context.Context, cfg *Config) error {
	if _, err := exec.LookPath("helm"); err != nil {
		return fmt.Errorf("mcp-gateway addon requires helm: %w", err)
	}

	if m.valuesFile != "" {
		if _, err := os.Stat(m.valuesFile); err != nil {
			return fmt.Errorf("mcp-gateway values overlay: %w", err)
		}
	}

	if err := m.ensureGatewayPrereqs(ctx, cfg); err != nil {
		return err
	}

	cfg.Logger.Info("installing mcp-gateway via helm", "version", m.resolveVersion())

	out, err := exec.CommandContext(ctx, "helm", m.helmArgs()...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("helm install mcp-gateway: %s: %w", string(out), err)
	}

	cfg.Logger.Info("mcp-gateway installed")
	return nil
}

// ensureGatewayPrereqs creates the gateway-system namespace, an Istio Gateway,
// and a ReferenceGrant the chart expects to exist before helm install.
func (m *mcpGateway) ensureGatewayPrereqs(ctx context.Context, cfg *Config) error {
	if err := ensureNamespace(ctx, cfg, gatewaySystemNamespace); err != nil {
		return fmt.Errorf("create %s namespace: %w", gatewaySystemNamespace, err)
	}

	gw := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "gateway.networking.k8s.io/v1",
			"kind":       "Gateway",
			"metadata": map[string]any{
				"name":      "mcp-gateway",
				"namespace": gatewaySystemNamespace,
			},
			"spec": map[string]any{
				"gatewayClassName": "istio",
				"listeners": []any{
					map[string]any{
						"name":     "mcp",
						"port":     int64(80),
						"protocol": "HTTP",
						"allowedRoutes": map[string]any{
							"namespaces": map[string]any{
								"from": "All",
							},
						},
					},
				},
			},
		},
	}
	if err := ensureResource(ctx, cfg, gatewayGVR, gw); err != nil {
		return fmt.Errorf("create gateway: %w", err)
	}

	rg := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "gateway.networking.k8s.io/v1beta1",
			"kind":       "ReferenceGrant",
			"metadata": map[string]any{
				"name":      "mcp-gateway-system-to-gateway-system",
				"namespace": gatewaySystemNamespace,
			},
			"spec": map[string]any{
				"from": []any{
					map[string]any{
						"group":     "gateway.networking.k8s.io",
						"kind":      "HTTPRoute",
						"namespace": mcpGatewayNamespace,
					},
				},
				"to": []any{
					map[string]any{
						"group": "",
						"kind":  "Service",
					},
				},
			},
		},
	}
	if err := ensureResource(ctx, cfg, referenceGrantGVR, rg); err != nil {
		return fmt.Errorf("create referencegrant: %w", err)
	}

	return nil
}

func (m *mcpGateway) Ready(ctx context.Context, cfg *Config) error {
	if err := waitForDeployment(ctx, cfg, mcpGatewayNamespace, mcpGatewayControllerDeploy, 5*time.Minute); err != nil {
		return err
	}

	// the helm chart adds an "mcp" listener to the gateway in gateway-system;
	// MCPGatewayExtension references that listener and fails if the gateway
	// controller hasn't accepted it yet
	return waitForMCPListener(ctx, cfg, 5*time.Minute, 5*time.Second)
}

// waitForMCPListener polls until the mcp-gateway Gateway in gateway-system has
// a listener named "mcp" with Accepted=True. the helm chart creates the
// listener, but the gateway controller needs time to reconcile it; the
// MCPGatewayExtension resource cannot reference the listener until then.
func waitForMCPListener(ctx context.Context, cfg *Config, timeout, interval time.Duration) error {
	cfg.Logger.Info("waiting for mcp listener on gateway", "namespace", gatewaySystemNamespace, "gateway", "mcp-gateway")
	deadline := time.Now().Add(timeout)
	why := "not yet observed"

	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		obj, err := cfg.DynamicClient.Resource(gatewayGVR).Namespace(gatewaySystemNamespace).Get(ctx, "mcp-gateway", metav1.GetOptions{})
		if err != nil {
			why = fmt.Sprintf("cannot get gateway: %v", err)
		} else {
			var ready bool
			ready, why = mcpListenerAccepted(obj)
			if ready {
				cfg.Logger.Info("mcp listener ready", "namespace", gatewaySystemNamespace, "gateway", "mcp-gateway")
				return nil
			}
		}
		cfg.Logger.Debug("waiting for mcp listener", "why", why)
		time.Sleep(interval)
	}
	return fmt.Errorf("mcp listener on gateway %s/mcp-gateway not accepted after %s (%s)", gatewaySystemNamespace, timeout, why)
}

// mcpListenerAccepted checks whether the gateway has a listener named "mcp"
// with Accepted condition set to True.
func mcpListenerAccepted(obj *unstructured.Unstructured) (bool, string) {
	listeners, found, _ := unstructured.NestedSlice(obj.Object, "status", "listeners")
	if !found || len(listeners) == 0 {
		return false, "no listener status"
	}
	for _, l := range listeners {
		lm, ok := l.(map[string]any)
		if !ok {
			continue
		}
		name, _, _ := unstructured.NestedString(lm, "name")
		if name != "mcp" {
			continue
		}
		conditions, found, _ := unstructured.NestedSlice(lm, "conditions")
		if !found {
			return false, "mcp listener has no conditions"
		}
		for _, c := range conditions {
			cm, ok := c.(map[string]any)
			if !ok {
				continue
			}
			if cm["type"] == "Accepted" && cm["status"] == "True" {
				return true, ""
			}
		}
		return false, "mcp listener not accepted"
	}
	return false, "mcp listener not found in status"
}
