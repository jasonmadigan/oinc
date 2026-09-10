package addons

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/yaml"
)

const (
	defaultMCPGatewayChartVersion       = "0.8.0"
	mcpGatewayChartOCI                  = "oci://ghcr.io/kuadrant/charts/mcp-gateway"
	mcpGatewayNamespace                 = "mcp-gateway-system"
	mcpGatewayControllerDeploy          = "mcp-gateway-controller"
	kuadrantNamespace                   = "kuadrant-system"
	gatewaySystemNamespace              = "gateway-system"
	mcpGatewayListenerPort        int64 = 80
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
	args = append(args, m.chartOptions()...)
	return append(args, "--wait", "--timeout", "5m")
}

func (m *mcpGateway) chartOptions() []string {
	var args []string
	if m.valuesFile != "" {
		args = append(args, "--values", m.valuesFile)
	}
	args = append(args, m.chartVersionArgs()...)
	args = append(args, "--set", fmt.Sprintf("gateway.port=%d", mcpGatewayListenerPort))
	return args
}

func (m *mcpGateway) templateArgs(chart string) []string {
	args := []string{"template", "mcp-gateway", chart, "-n", mcpGatewayNamespace}
	args = append(args, m.chartOptions()...)
	// Apply after user values so an overlay cannot deploy a second controller.
	return append(args, "--set", "controller.enabled=false", "--skip-tests")
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

	managedVersion, err := kuadrantMCPVersion(ctx, cfg)
	if err != nil {
		return err
	}
	if managedVersion != "" {
		return m.installManagedInstance(ctx, cfg, managedVersion)
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

// kuadrantMCPVersion detects ownership before considering a standalone install.
// Checking the CRD also covers the interval before the child Deployment exists.
func kuadrantMCPVersion(ctx context.Context, cfg *Config) (string, error) {
	crdGVR := schema.GroupVersionResource{Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions"}
	crd, err := cfg.DynamicClient.Resource(crdGVR).Get(ctx, "mcpgatewayextensions.mcp.kuadrant.io", metav1.GetOptions{})
	if errors.IsNotFound(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("detecting Kuadrant MCP Gateway ownership: %w", err)
	}
	if crd.GetLabels()["app.kubernetes.io/managed-by"] != "kuadrant-operator" {
		return "", nil
	}
	versions, _, _ := unstructured.NestedSlice(crd.Object, "spec", "versions")
	var served string
	for _, entry := range versions {
		v, ok := entry.(map[string]any)
		if !ok || v["served"] != true {
			continue
		}
		name, _, _ := unstructured.NestedString(v, "name")
		if v["storage"] == true && name != "" {
			return name, nil
		}
		if served == "" {
			served = name
		}
	}
	if served == "" {
		return "", fmt.Errorf("Kuadrant-managed MCPGatewayExtension CRD has no served API version")
	}
	return served, nil
}

func (m *mcpGateway) installManagedInstance(ctx context.Context, cfg *Config, apiVersion string) error {
	cfg.Logger.Info("configuring mcp-gateway with Kuadrant-managed controller", "chartVersion", m.resolveVersion(), "apiVersion", apiVersion)
	chartDir, err := os.MkdirTemp("", "oinc-mcp-chart-")
	if err != nil {
		return fmt.Errorf("creating mcp-gateway chart directory: %w", err)
	}
	defer os.RemoveAll(chartDir)

	// Some Helm versions write OCI pull messages to stdout during template.
	// Pull separately so the decoder receives only the local chart's manifest.
	pullArgs := []string{"pull", mcpGatewayChartOCI, "--destination", chartDir}
	pullArgs = append(pullArgs, m.chartVersionArgs()...)
	if out, err := exec.CommandContext(ctx, "helm", pullArgs...).CombinedOutput(); err != nil {
		return fmt.Errorf("pulling mcp-gateway chart: %s: %w", out, err)
	}
	charts, err := os.ReadDir(chartDir)
	if err != nil {
		return fmt.Errorf("reading mcp-gateway chart directory: %w", err)
	}
	if len(charts) != 1 || charts[0].IsDir() || filepath.Ext(charts[0].Name()) != ".tgz" {
		return fmt.Errorf("pulling mcp-gateway chart: expected one chart archive in %s", chartDir)
	}
	chart := filepath.Join(chartDir, charts[0].Name())
	cmd := exec.CommandContext(ctx, "helm", m.templateArgs(chart)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	manifest, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("rendering mcp-gateway instance: %s: %w", stderr.String(), err)
	}

	// Only instance resources belong to oinc. Reject unexpected chart output
	// before writing anything, even when a custom chart version is selected.
	resources, err := mcpInstanceResources(manifest, apiVersion)
	if err != nil {
		return err
	}
	if err := m.ensureGatewayPrereqs(ctx, cfg); err != nil {
		return err
	}
	for _, resource := range resources {
		obj := resource.object
		if err := ensureNamespace(ctx, cfg, obj.GetNamespace()); err != nil {
			return err
		}
		data, err := json.Marshal(obj.Object)
		if err != nil {
			return err
		}
		_, err = cfg.DynamicClient.Resource(resource.gvr).Namespace(obj.GetNamespace()).Patch(ctx, obj.GetName(), types.ApplyPatchType, data, metav1.PatchOptions{FieldManager: "oinc-mcp-gateway"})
		if err != nil {
			return fmt.Errorf("applying %s %s/%s: %w", obj.GetKind(), obj.GetNamespace(), obj.GetName(), err)
		}
	}
	return nil
}

type mcpInstanceResource struct {
	object *unstructured.Unstructured
	gvr    schema.GroupVersionResource
}

func mcpInstanceResources(manifest []byte, apiVersion string) ([]mcpInstanceResource, error) {
	decoder := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(manifest), 4096)
	var resources []mcpInstanceResource
	for {
		obj := &unstructured.Unstructured{}
		if err := decoder.Decode(obj); err != nil {
			if err == io.EOF {
				return resources, nil
			}
			return nil, fmt.Errorf("decoding mcp-gateway instance: %w", err)
		}
		if len(obj.Object) == 0 {
			continue
		}
		var gvr schema.GroupVersionResource
		switch obj.GroupVersionKind().GroupKind() {
		case schema.GroupKind{Group: "mcp.kuadrant.io", Kind: "MCPGatewayExtension"}:
			// Published charts can still render v1alpha1 after the installed
			// operator has stopped serving it. The instance fields are shared.
			obj.SetAPIVersion("mcp.kuadrant.io/" + apiVersion)
			gvr = schema.GroupVersionResource{Group: "mcp.kuadrant.io", Version: apiVersion, Resource: "mcpgatewayextensions"}
		case schema.GroupKind{Group: "gateway.networking.k8s.io", Kind: "ReferenceGrant"}:
			gvr = referenceGrantGVR
		case schema.GroupKind{Group: "gateway.networking.k8s.io", Kind: "Gateway"}:
			gvr = gatewayGVR
		case schema.GroupKind{Kind: "Service"}:
			gvr = schema.GroupVersionResource{Version: "v1", Resource: "services"}
		default:
			return nil, fmt.Errorf("unexpected %s in mcp-gateway instance chart; Kuadrant owns the controller and CRDs", obj.GroupVersionKind())
		}
		if obj.GetNamespace() == "" || obj.GetName() == "" {
			return nil, fmt.Errorf("mcp-gateway instance %s requires a name and namespace", obj.GetKind())
		}
		labels := obj.GetLabels()
		if labels == nil {
			labels = map[string]string{}
		}
		labels["app.kubernetes.io/managed-by"] = "oinc"
		obj.SetLabels(labels)
		resources = append(resources, mcpInstanceResource{object: obj, gvr: gvr})
	}
}

// ensureGatewayPrereqs creates the gateway-system namespace, an Istio Gateway,
// and a ReferenceGrant the chart expects to exist before helm install.
func (m *mcpGateway) ensureGatewayPrereqs(ctx context.Context, cfg *Config) error {
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
						"port":     mcpGatewayListenerPort,
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
	if err := ensureMetalLBGateway(ctx, cfg, gw, "mcp-gateway-params"); err != nil {
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
	managedVersion, err := kuadrantMCPVersion(ctx, cfg)
	if err != nil {
		return err
	}
	namespace := mcpGatewayNamespace
	if managedVersion != "" {
		namespace = kuadrantNamespace
	}
	if err := waitForDeployment(ctx, cfg, namespace, mcpGatewayControllerDeploy, 5*time.Minute); err != nil {
		return err
	}

	// ensureGatewayPrereqs adds an "mcp" listener in gateway-system;
	// MCPGatewayExtension references that listener and fails if the gateway
	// controller hasn't accepted it yet. Programming and an address are also
	// required before the addon is ready.
	return waitForMCPListener(ctx, cfg, 5*time.Minute, 5*time.Second)
}

// waitForMCPListener polls until the mcp-gateway Gateway in gateway-system has
// Programmed=True, an assigned address, and a listener named "mcp" with
// Accepted=True. ensureGatewayPrereqs creates the
// listener, but the gateway controller needs time to reconcile it; the
// MCPGatewayExtension resource cannot reference the listener until then.
func waitForMCPListener(ctx context.Context, cfg *Config, timeout, interval time.Duration) error {
	cfg.Logger.Info("waiting for mcp gateway and listener", "namespace", gatewaySystemNamespace, "gateway", "mcp-gateway")
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
			ready, why = gatewayProgrammedState(obj)
			if !ready {
				why = "Gateway Programmed: " + why
			} else {
				ready, why = mcpListenerAccepted(obj)
			}
			if ready {
				cfg.Logger.Info("mcp gateway and listener ready", "namespace", gatewaySystemNamespace, "gateway", "mcp-gateway")
				return nil
			}
		}
		cfg.Logger.Debug("waiting for mcp gateway and listener", "why", why)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
	return fmt.Errorf("gateway %s/mcp-gateway not ready after %s (%s); requires Programmed=True with an address and an accepted mcp listener; check the metallb pool for class %s (--metallb-address-pool)", gatewaySystemNamespace, timeout, why, metalLBClass)
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
			if cm["type"] == "Accepted" {
				if cm["status"] == "True" {
					return true, ""
				}
				return false, fmt.Sprintf("mcp listener Accepted=%v reason=%v message=%v", cm["status"], cm["reason"], cm["message"])
			}
		}
		return false, "mcp listener not accepted"
	}
	return false, "mcp listener not found in status"
}
