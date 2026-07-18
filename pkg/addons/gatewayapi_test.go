package addons

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/yaml"
)

// the gateway instance cannot be programmed before istio exists or get an
// address before metallb does, so the option must pull both in ahead of it.
func TestGatewayAPIDependencies(t *testing.T) {
	g := &gatewayAPI{}
	if deps := g.Dependencies(); len(deps) != 0 {
		t.Errorf("default deps = %v, want none", deps)
	}

	g.SetOptions(map[string]string{"gateway": "true"})
	deps := g.Dependencies()
	want := map[string]bool{"istio": true, "metallb": true}
	if len(deps) != len(want) {
		t.Fatalf("deps with gateway option = %v, want istio and metallb", deps)
	}
	for _, d := range deps {
		if !want[d] {
			t.Errorf("unexpected dependency %q", d)
		}
	}

	g.SetOptions(map[string]string{"gateway": "false"})
	if deps := g.Dependencies(); len(deps) != 0 {
		t.Errorf("deps after disabling = %v, want none", deps)
	}
}

func TestEnsureDefaultGateway(t *testing.T) {
	client := dynamicfake.NewSimpleDynamicClient(kscheme.Scheme)
	cfg := &Config{
		DynamicClient: client,
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	g := &gatewayAPI{gateway: true}
	for range 2 { // second call must be a no-op
		if err := g.ensureDefaultGateway(context.Background(), cfg); err != nil {
			t.Fatalf("ensureDefaultGateway: %v", err)
		}
	}

	if _, err := client.Resource(namespaceGVR).Get(context.Background(), gatewayNamespace, metav1.GetOptions{}); err != nil {
		t.Fatalf("namespace not created: %v", err)
	}

	// the params configmap must exist and its service overlay must scope the
	// generated service to the oinc metallb class: loadBalancerClass is
	// immutable after creation, so istio has to render the service with it
	cm, err := client.Resource(configMapGVR).Namespace(gatewayNamespace).Get(context.Background(), gatewayInfraParams, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("params configmap not created: %v", err)
	}
	overlay, found, _ := unstructured.NestedString(cm.Object, "data", "service")
	if !found {
		t.Fatal("params configmap has no service overlay")
	}
	var svc struct {
		Spec struct {
			LoadBalancerClass string `json:"loadBalancerClass"`
		} `json:"spec"`
	}
	if err := yaml.Unmarshal([]byte(overlay), &svc); err != nil {
		t.Fatalf("service overlay is not valid yaml: %v", err)
	}
	if svc.Spec.LoadBalancerClass != metalLBClass {
		t.Errorf("overlay loadBalancerClass = %q, want %q", svc.Spec.LoadBalancerClass, metalLBClass)
	}

	gw, err := client.Resource(gatewayGVR).Namespace(gatewayNamespace).Get(context.Background(), gatewayName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("gateway not created: %v", err)
	}
	if class, _, _ := unstructured.NestedString(gw.Object, "spec", "gatewayClassName"); class != "istio" {
		t.Errorf("gatewayClassName = %q, want istio", class)
	}
	if ref, _, _ := unstructured.NestedString(gw.Object, "spec", "infrastructure", "parametersRef", "name"); ref != gatewayInfraParams {
		t.Errorf("parametersRef.name = %q, want %q", ref, gatewayInfraParams)
	}
	if kind, _, _ := unstructured.NestedString(gw.Object, "spec", "infrastructure", "parametersRef", "kind"); kind != "ConfigMap" {
		t.Errorf("parametersRef.kind = %q, want ConfigMap", kind)
	}
	listeners, _, _ := unstructured.NestedSlice(gw.Object, "spec", "listeners")
	if len(listeners) != 1 {
		t.Fatalf("listeners = %v, want one http listener", listeners)
	}
	l := listeners[0].(map[string]any)
	if l["port"] != int64(80) || l["protocol"] != "HTTP" {
		t.Errorf("listener = %v, want http/80", l)
	}
	from, _, _ := unstructured.NestedString(l, "allowedRoutes", "namespaces", "from")
	if from != "All" {
		t.Errorf("allowedRoutes from = %q, want All", from)
	}
}

// a gateway that predates the option has a class-less service already and
// the class is immutable, so ensure must fail fast with a recreate hint
// instead of timing out on the programmed wait.
func TestEnsureDefaultGatewayRejectsExistingWithoutParams(t *testing.T) {
	client := dynamicfake.NewSimpleDynamicClient(kscheme.Scheme)
	pre := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "gateway.networking.k8s.io/v1",
			"kind":       "Gateway",
			"metadata": map[string]any{
				"name":      gatewayName,
				"namespace": gatewayNamespace,
			},
			"spec": map[string]any{"gatewayClassName": "istio"},
		},
	}
	if _, err := client.Resource(gatewayGVR).Namespace(gatewayNamespace).Create(context.Background(), pre, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seeding gateway: %v", err)
	}
	cfg := &Config{DynamicClient: client, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}

	err := (&gatewayAPI{gateway: true}).ensureDefaultGateway(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected error for a pre-existing gateway without parametersRef")
	}
	if !strings.Contains(err.Error(), "parametersRef") || !strings.Contains(err.Error(), "delete the gateway") {
		t.Errorf("error %q should explain the parametersRef gap and the recreate remedy", err)
	}
}

func fakeGateway(programmed bool, reason, message string, addresses ...string) *unstructured.Unstructured {
	status := "False"
	if programmed {
		status = "True"
	}
	addrs := make([]any, 0, len(addresses))
	for _, a := range addresses {
		addrs = append(addrs, map[string]any{"type": "IPAddress", "value": a})
	}
	return &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "gateway.networking.k8s.io/v1",
			"kind":       "Gateway",
			"metadata": map[string]any{
				"name":      gatewayName,
				"namespace": gatewayNamespace,
			},
			"status": map[string]any{
				"addresses": addrs,
				"conditions": []any{
					map[string]any{"type": "Programmed", "status": status, "reason": reason, "message": message},
				},
			},
		},
	}
}

// gatewayWaitConfig seeds a fake cluster with the gateway via the explicit
// GVR: the fake tracker's kind-to-resource guess would file "Gateway" under
// "gatewaies" if seeded through the constructor.
func gatewayWaitConfig(t *testing.T, gw *unstructured.Unstructured) *Config {
	t.Helper()
	client := dynamicfake.NewSimpleDynamicClient(kscheme.Scheme)
	if _, err := client.Resource(gatewayGVR).Namespace(gatewayNamespace).Create(context.Background(), gw, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seeding gateway: %v", err)
	}
	return &Config{DynamicClient: client, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func TestWaitForGatewayProgrammed(t *testing.T) {
	t.Run("programmed with address", func(t *testing.T) {
		cfg := gatewayWaitConfig(t, fakeGateway(true, "Programmed", "", "172.17.0.200"))
		if err := waitForGatewayProgrammed(context.Background(), cfg, time.Second, time.Millisecond); err != nil {
			t.Fatalf("waitForGatewayProgrammed: %v", err)
		}
	})

	t.Run("not programmed times out with reason", func(t *testing.T) {
		cfg := gatewayWaitConfig(t, fakeGateway(false, "AddressNotAssigned", "address pending"))
		err := waitForGatewayProgrammed(context.Background(), cfg, 20*time.Millisecond, time.Millisecond)
		if err == nil {
			t.Fatal("expected timeout error")
		}
		if !strings.Contains(err.Error(), "AddressNotAssigned") {
			t.Errorf("error %q should carry the last condition reason", err)
		}
	})

	t.Run("programmed without address keeps waiting", func(t *testing.T) {
		cfg := gatewayWaitConfig(t, fakeGateway(true, "Programmed", ""))
		err := waitForGatewayProgrammed(context.Background(), cfg, 20*time.Millisecond, time.Millisecond)
		if err == nil {
			t.Fatal("expected timeout when no address is assigned")
		}
		if !strings.Contains(err.Error(), "no address assigned") {
			t.Errorf("error %q should say the address is missing", err)
		}
	})
}
