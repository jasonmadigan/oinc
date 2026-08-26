package oinc

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"
)

func TestResolvePluginURL(t *testing.T) {
	tests := []struct {
		name          string
		spec          string
		containerHost string
		want          string
	}{
		{
			name:          "rewrite podman host to docker",
			spec:          "my-plugin=http://host.containers.internal:9001",
			containerHost: "host.docker.internal",
			want:          "my-plugin=http://host.docker.internal:9001",
		},
		{
			name:          "rewrite docker host to podman",
			spec:          "my-plugin=http://host.docker.internal:9001",
			containerHost: "host.containers.internal",
			want:          "my-plugin=http://host.containers.internal:9001",
		},
		{
			name:          "already correct docker host",
			spec:          "my-plugin=http://host.docker.internal:9001",
			containerHost: "host.docker.internal",
			want:          "my-plugin=http://host.docker.internal:9001",
		},
		{
			name:          "already correct podman host",
			spec:          "my-plugin=http://host.containers.internal:9001",
			containerHost: "host.containers.internal",
			want:          "my-plugin=http://host.containers.internal:9001",
		},
		{
			name:          "localhost left alone",
			spec:          "my-plugin=http://localhost:9001",
			containerHost: "host.docker.internal",
			want:          "my-plugin=http://localhost:9001",
		},
		{
			name:          "linux host rewrite",
			spec:          "my-plugin=http://host.containers.internal:9001",
			containerHost: "localhost",
			want:          "my-plugin=http://localhost:9001",
		},
		{
			name:          "no url part",
			spec:          "my-plugin",
			containerHost: "host.docker.internal",
			want:          "my-plugin",
		},
		{
			name:          "empty spec",
			spec:          "",
			containerHost: "host.docker.internal",
			want:          "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolvePluginURL(tt.spec, tt.containerHost)
			if got != tt.want {
				t.Errorf("resolvePluginURL(%q, %q) = %q, want %q", tt.spec, tt.containerHost, got, tt.want)
			}
		})
	}
}

func TestConsoleProxyServiceName(t *testing.T) {
	tests := []struct {
		name       string
		pluginName string
		alias      string
		want       string
	}{
		{
			name:       "ordinary plugin and alias",
			pluginName: "kuadrant-console-plugin",
			alias:      "backend",
			want:       "oinc-kuadrant-console-plugin-backend",
		},
		{
			name:       "normalizes unsupported characters",
			pluginName: "Example_Plugin",
			alias:      "MCP.Backend",
			want:       "oinc-example-plugin-mcp-backend-825aa9fc",
		},
		{
			name:       "long names have a stable collision-resistant suffix",
			pluginName: "a-console-plugin-name-that-is-deliberately-much-too-long-for-dns",
			alias:      "a-long-backend-alias",
			want:       "oinc-a-console-plugin-name-that-is-deliberately-much-t-1f5cd6b5",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := consoleProxyServiceName(tt.pluginName, tt.alias)
			if got != tt.want {
				t.Fatalf("consoleProxyServiceName(%q, %q) = %q, want %q", tt.pluginName, tt.alias, got, tt.want)
			}
			if len(got) > 63 {
				t.Fatalf("service name is %d characters, want at most 63", len(got))
			}
		})
	}
}

func TestConsoleProxyServiceNameAvoidsNormalizationCollisions(t *testing.T) {
	withUnderscore := consoleProxyServiceName("example-plugin", "mcp_backend")
	withDash := consoleProxyServiceName("example-plugin", "mcp-backend")
	if withUnderscore == withDash {
		t.Fatalf("distinct aliases produced the same Service name %q", withUnderscore)
	}
}

func TestBuildConsoleProxyOptions(t *testing.T) {
	const (
		pluginName = "kuadrant-console-plugin"
		namespace  = "kuadrant-system"
		address    = "192.168.122.10"
	)

	plugin := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "console.openshift.io/v1",
		"kind":       "ConsolePlugin",
		"metadata": map[string]any{
			"name": pluginName,
		},
		"spec": map[string]any{
			"proxy": []any{
				map[string]any{
					"alias":         "backend",
					"authorization": "UserToken",
					"endpoint": map[string]any{
						"type": "Service",
						"service": map[string]any{
							"name":      "backend",
							"namespace": namespace,
							"port":      int64(8443),
						},
					},
				},
				map[string]any{
					"alias":         "custom",
					"authorization": "None",
					"caCertificate": "CUSTOM CA",
					"endpoint": map[string]any{
						"type": "Service",
						"service": map[string]any{
							"name":      "custom-backend",
							"namespace": namespace,
							"port":      int64(9443),
						},
					},
				},
			},
		},
	}}
	dynClient := dynamicfake.NewSimpleDynamicClient(k8sruntime.NewScheme(), plugin)

	backend := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "backend", Namespace: namespace},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app": "backend-v2"},
			Ports: []corev1.ServicePort{{
				Name:       "https",
				Port:       8443,
				TargetPort: intstr.FromInt32(9443),
				Protocol:   corev1.ProtocolTCP,
			}},
		},
	}
	customBackend := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "custom-backend", Namespace: namespace},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app": "custom-backend"},
			Ports:    []corev1.ServicePort{{Port: 9443, TargetPort: intstr.FromInt32(10443)}},
		},
	}
	shadow := func(alias string) *corev1.Service {
		return &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:      consoleProxyServiceName(pluginName, alias),
				Namespace: namespace,
				Labels: map[string]string{
					"app.kubernetes.io/managed-by": "oinc",
					"oinc.io/console-plugin":       pluginName,
				},
			},
			Spec: corev1.ServiceSpec{
				Type:     corev1.ServiceTypeLoadBalancer,
				Selector: map[string]string{"app": "stale"},
				Ports:    []corev1.ServicePort{{Port: 443}},
			},
			Status: corev1.ServiceStatus{
				LoadBalancer: corev1.LoadBalancerStatus{
					Ingress: []corev1.LoadBalancerIngress{{IP: address}},
				},
			},
		}
	}
	serviceCA := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "openshift-service-ca.crt", Namespace: namespace},
		Data:       map[string]string{"service-ca.crt": "SERVICE CA"},
	}
	client := kubefake.NewClientset(backend, customBackend, shadow("backend"), shadow("custom"), serviceCA)

	var writtenBundles map[string]struct{}
	options, err := buildConsoleProxyOptionsWithCAWriter(
		context.Background(),
		client,
		dynClient,
		pluginName,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		func(bundles map[string]struct{}) (string, error) {
			writtenBundles = bundles
			return "/tmp/service-ca.crt", nil
		},
	)
	if err != nil {
		t.Fatalf("buildConsoleProxyOptionsWithCAWriter() error = %v", err)
	}

	if got := options.extraHosts["backend.kuadrant-system.svc"]; got != address {
		t.Errorf("backend host address = %q, want %q", got, address)
	}
	if got := options.extraHosts["custom-backend.kuadrant-system.svc"]; got != address {
		t.Errorf("custom backend host address = %q, want %q", got, address)
	}
	if options.caFile != "/tmp/service-ca.crt" {
		t.Errorf("CA file = %q, want test writer path", options.caFile)
	}
	if _, ok := writtenBundles["SERVICE CA"]; !ok || len(writtenBundles) != 1 {
		t.Errorf("written CA bundles = %#v, want only the default service CA", writtenBundles)
	}

	var config bridgePluginProxy
	if err := json.Unmarshal([]byte(options.config), &config); err != nil {
		t.Fatalf("decoding generated Bridge config: %v", err)
	}
	if len(config.Services) != 2 {
		t.Fatalf("generated %d proxy services, want 2", len(config.Services))
	}
	if got := config.Services[0]; got.ConsoleAPIPath != "/api/proxy/plugin/kuadrant-console-plugin/backend/" || got.Endpoint != "https://backend.kuadrant-system.svc:8443/" || !got.Authorize || got.CACertificate != "" {
		t.Errorf("default proxy config = %#v, want default service CA and user authorization", got)
	}
	if got := config.Services[1]; got.CACertificate != "CUSTOM CA" || got.Authorize {
		t.Errorf("custom proxy config = %#v, want isolated custom CA and no user authorization", got)
	}

	updated, err := client.CoreV1().Services(namespace).Get(context.Background(), consoleProxyServiceName(pluginName, "backend"), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("getting reconciled shadow Service: %v", err)
	}
	if got := updated.Spec.Selector["app"]; got != "backend-v2" {
		t.Errorf("reconciled selector = %q, want backend-v2", got)
	}
	if len(updated.Spec.Ports) != 1 || updated.Spec.Ports[0].Port != 8443 || updated.Spec.Ports[0].TargetPort != intstr.FromInt32(9443) {
		t.Errorf("reconciled ports = %#v, want current backend Service port", updated.Spec.Ports)
	}
}
