package addons

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/yaml"
)

const (
	defaultGatewayAPIVersion = "1.2.1"
	gatewayNamespace         = "gateway-system"
	gatewayName              = "kuadrant-ingressgateway"
	gatewayInfraParams       = "kuadrant-ingressgateway-params"
)

var gatewayGVR = schema.GroupVersionResource{
	Group: "gateway.networking.k8s.io", Version: "v1", Resource: "gateways",
}

func init() { Register(&gatewayAPI{}) }

type gatewayAPI struct {
	version string
	gateway bool
}

func (g *gatewayAPI) Name() string { return "gateway-api" }

// the gateway instance needs istiod to deploy it and metallb to give it an
// address, so the option pulls both in ahead of this addon.
func (g *gatewayAPI) Dependencies() []string {
	if g.gateway {
		return []string{"istio", "metallb"}
	}
	return nil
}

func (g *gatewayAPI) SetOptions(opts map[string]string) {
	if v, ok := opts["version"]; ok {
		g.version = v
	}
	if v, ok := opts["gateway"]; ok {
		g.gateway = v == "true"
	}
}

func (g *gatewayAPI) resolveVersion() string {
	if g.version != "" {
		return g.version
	}
	return defaultGatewayAPIVersion
}

func (g *gatewayAPI) Install(ctx context.Context, cfg *Config) error {
	v := g.resolveVersion()
	url := fmt.Sprintf("https://github.com/kubernetes-sigs/gateway-api/releases/download/v%s/standard-install.yaml", v)
	cfg.Logger.Info("fetching gateway-api CRDs", "url", url)

	if _, err := exec.LookPath("curl"); err != nil {
		return fmt.Errorf("curl is required but not found in PATH")
	}
	data, err := exec.CommandContext(ctx, "curl", "-sSL", "--retry", "3", "--max-time", "30", url).Output()
	if err != nil {
		return fmt.Errorf("downloading gateway-api manifests: %w", err)
	}

	crdGVR := schema.GroupVersionResource{
		Group:    "apiextensions.k8s.io",
		Version:  "v1",
		Resource: "customresourcedefinitions",
	}

	decoder := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(data), 4096)
	for {
		obj := &unstructured.Unstructured{}
		if err := decoder.Decode(obj); err != nil {
			if err == io.EOF {
				break
			}
			return fmt.Errorf("decoding gateway-api manifest: %w", err)
		}

		if obj.Object == nil {
			continue
		}

		cfg.Logger.Info("applying", "kind", obj.GetKind(), "name", obj.GetName())

		existing, err := cfg.DynamicClient.Resource(crdGVR).Get(ctx, obj.GetName(), metav1.GetOptions{})
		if err != nil {
			if errors.IsNotFound(err) {
				if _, err := cfg.DynamicClient.Resource(crdGVR).Create(ctx, obj, metav1.CreateOptions{}); err != nil {
					return fmt.Errorf("creating %s: %w", obj.GetName(), err)
				}
				continue
			}
			return fmt.Errorf("getting %s: %w", obj.GetName(), err)
		}

		obj.SetResourceVersion(existing.GetResourceVersion())
		if _, err := cfg.DynamicClient.Resource(crdGVR).Update(ctx, obj, metav1.UpdateOptions{}); err != nil {
			return fmt.Errorf("updating %s: %w", obj.GetName(), err)
		}
	}

	return nil
}

func (g *gatewayAPI) Ready(ctx context.Context, cfg *Config) error {
	crdGVR := schema.GroupVersionResource{
		Group:    "apiextensions.k8s.io",
		Version:  "v1",
		Resource: "customresourcedefinitions",
	}

	expected := []string{
		"gatewayclasses.gateway.networking.k8s.io",
		"gateways.gateway.networking.k8s.io",
		"httproutes.gateway.networking.k8s.io",
	}

	for range 30 {
		allReady := true
		for _, name := range expected {
			crd, err := cfg.DynamicClient.Resource(crdGVR).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				allReady = false
				break
			}

			conditions, found, _ := unstructured.NestedSlice(crd.Object, "status", "conditions")
			if !found {
				allReady = false
				break
			}

			established := false
			for _, c := range conditions {
				cm, ok := c.(map[string]any)
				if !ok {
					continue
				}
				if cm["type"] == "Established" && cm["status"] == "True" {
					established = true
					break
				}
			}
			if !established {
				allReady = false
				break
			}
		}

		if allReady {
			return g.readyInstances(ctx, cfg)
		}
		time.Sleep(2 * time.Second)
	}

	return fmt.Errorf("gateway-api CRDs not established after 60s")
}

func (g *gatewayAPI) readyInstances(ctx context.Context, cfg *Config) error {
	if !g.gateway {
		return nil
	}
	cfg.Logger.Info("creating default gateway", "namespace", gatewayNamespace, "name", gatewayName)
	if err := g.ensureDefaultGateway(ctx, cfg); err != nil {
		return err
	}
	return waitForGatewayProgrammed(ctx, cfg, 5*time.Minute, 5*time.Second)
}

// ensureDefaultGateway creates the kuadrant-ingressgateway Gateway (istio
// class) plus the infrastructure parameters ConfigMap istio's deployment
// controller merges into the resources it generates. The service overlay sets
// spec.loadBalancerClass to the oinc metallb class: the field is immutable
// after creation, so the generated service must be rendered with it, and
// without it the class-scoped metallb never assigns the gateway an address.
func (g *gatewayAPI) ensureDefaultGateway(ctx context.Context, cfg *Config) error {
	// a pre-existing gateway without the parametersRef (e.g. created by a
	// consumer script before this option existed) has a class-less service
	// already; the class cannot be added after creation, so fail fast rather
	// than burn the programmed wait with a misleading pool hint
	existing, err := cfg.DynamicClient.Resource(gatewayGVR).Namespace(gatewayNamespace).Get(ctx, gatewayName, metav1.GetOptions{})
	if err == nil {
		if ref, _, _ := unstructured.NestedString(existing.Object, "spec", "infrastructure", "parametersRef", "name"); ref == "" {
			return fmt.Errorf("gateway %s/%s already exists without infrastructure.parametersRef, so its service cannot adopt the %s class (immutable after creation); delete the gateway and re-run so oinc can recreate it",
				gatewayNamespace, gatewayName, metalLBClass)
		}
	} else if !errors.IsNotFound(err) {
		return fmt.Errorf("checking for existing gateway: %w", err)
	}

	if err := ensureNamespace(ctx, cfg, gatewayNamespace); err != nil {
		return fmt.Errorf("creating namespace %s: %w", gatewayNamespace, err)
	}

	params := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "v1",
			"kind":       "ConfigMap",
			"metadata": map[string]any{
				"name":      gatewayInfraParams,
				"namespace": gatewayNamespace,
			},
			"data": map[string]any{
				"service": "spec:\n  loadBalancerClass: " + metalLBClass + "\n",
			},
		},
	}
	if err := ensureResource(ctx, cfg, configMapGVR, params); err != nil {
		return err
	}

	gw := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "gateway.networking.k8s.io/v1",
			"kind":       "Gateway",
			"metadata": map[string]any{
				"name":      gatewayName,
				"namespace": gatewayNamespace,
			},
			"spec": map[string]any{
				"gatewayClassName": "istio",
				"infrastructure": map[string]any{
					"parametersRef": map[string]any{
						"group": "",
						"kind":  "ConfigMap",
						"name":  gatewayInfraParams,
					},
				},
				"listeners": []any{
					map[string]any{
						"name":     "http",
						"port":     int64(80),
						"protocol": "HTTP",
						"allowedRoutes": map[string]any{
							"namespaces": map[string]any{"from": "All"},
						},
					},
				},
			},
		},
	}
	return ensureResource(ctx, cfg, gatewayGVR, gw)
}

func waitForGatewayProgrammed(ctx context.Context, cfg *Config, timeout, interval time.Duration) error {
	deadline := time.Now().Add(timeout)
	why := "not yet observed"
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		obj, err := cfg.DynamicClient.Resource(gatewayGVR).Namespace(gatewayNamespace).Get(ctx, gatewayName, metav1.GetOptions{})
		if err != nil {
			why = fmt.Sprintf("cannot get gateway: %v", err)
		} else {
			var ready bool
			ready, why = gatewayProgrammedState(obj)
			if ready {
				cfg.Logger.Info("gateway programmed", "namespace", gatewayNamespace, "name", gatewayName)
				return nil
			}
		}
		cfg.Logger.Debug("waiting for gateway to be programmed", "why", why)
		time.Sleep(interval)
	}
	return fmt.Errorf("gateway %s/%s not programmed with an address after %s (%s); an address needs a metallb pool for class %s (--metallb-address-pool)",
		gatewayNamespace, gatewayName, timeout, why, metalLBClass)
}

// gatewayProgrammedState reports whether the Gateway's Programmed condition is
// True with an address assigned, plus a short explanation when it is not.
func gatewayProgrammedState(obj *unstructured.Unstructured) (bool, string) {
	conditions, found, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
	if !found || len(conditions) == 0 {
		return false, "status empty"
	}
	for _, c := range conditions {
		cm, ok := c.(map[string]any)
		if !ok || cm["type"] != "Programmed" {
			continue
		}
		if cm["status"] != "True" {
			return false, fmt.Sprintf("reason=%v message=%v", cm["reason"], cm["message"])
		}
		addresses, _, _ := unstructured.NestedSlice(obj.Object, "status", "addresses")
		if len(addresses) == 0 {
			return false, "programmed but no address assigned"
		}
		return true, ""
	}
	return false, "no Programmed condition"
}
