package oinc

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jasonmadigan/oinc/pkg/runtime"
	"github.com/jasonmadigan/oinc/pkg/version"
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
			pluginName: "example-plugin",
			alias:      "MCP_Backend",
			want:       "oinc-example-plugin-mcp-backend-4a6d22b3",
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
	tests := []struct {
		name  string
		left  string
		right string
	}{
		{name: "normalization", left: "mcp_backend", right: "mcp-backend"},
		{name: "case", left: "Backend", right: "backend"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			left := consoleProxyServiceName("example-plugin", tt.left)
			right := consoleProxyServiceName("example-plugin", tt.right)
			if left == right {
				t.Fatalf("distinct aliases %q and %q produced the same Service name %q", tt.left, tt.right, left)
			}
		})
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

func TestBuildConsoleProxyOptionsClearsRemovedProxies(t *testing.T) {
	const pluginName = "kuadrant-console-plugin"
	plugin := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "console.openshift.io/v1",
		"kind":       "ConsolePlugin",
		"metadata": map[string]any{
			"name": pluginName,
		},
		"spec": map[string]any{},
	}}

	options, err := buildConsoleProxyOptionsWithCAWriter(
		context.Background(),
		kubefake.NewClientset(),
		dynamicfake.NewSimpleDynamicClient(k8sruntime.NewScheme(), plugin),
		pluginName,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		func(map[string]struct{}) (string, error) {
			t.Fatal("CA writer called with no proxy entries")
			return "", nil
		},
	)
	if err != nil {
		t.Fatalf("buildConsoleProxyOptionsWithCAWriter() error = %v", err)
	}
	if options.caFile != "" || len(options.extraHosts) != 0 {
		t.Errorf("empty proxy options = %#v, want no CA file or extra hosts", options)
	}

	var config bridgePluginProxy
	if err := json.Unmarshal([]byte(options.config), &config); err != nil {
		t.Fatalf("decoding generated Bridge config: %v", err)
	}
	if config.Services == nil || len(config.Services) != 0 {
		t.Errorf("generated proxy services = %#v, want an explicit empty list", config.Services)
	}
}

// The launch must use the catalogue's image and platform: 4.x payload
// consoles request amd64 even on ARM hosts, while 5.0 runs natively.
func TestStartConsoleRequestsCatalogueImage(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "docker")
	capture := filepath.Join(dir, "args")
	script := `#!/bin/sh
case "$1" in
  info) printf '{"CgroupVersion":"2"}';;
  create) printf '%s\n' "$@" > "$OINC_TEST_ARGS";;
esac
`
	if err := os.WriteFile(binary, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OINC_TEST_ARGS", capture)
	rt, err := runtime.Detect(binary)
	if err != nil {
		t.Fatal(err)
	}
	for _, selector := range []string{"4.22", "5.0.0-okd-scos.1"} {
		ver, err := version.Resolve(selector)
		if err != nil {
			t.Fatal(err)
		}
		if err := startConsoleContainer(rt, ver, "test-token", ConsoleOpts{Port: 9000}, nil); err != nil {
			t.Fatal(err)
		}
		args, err := os.ReadFile(capture)
		if err != nil {
			t.Fatal(err)
		}
		image, platform := ver.ConsoleImageRef()
		if selector == "4.22" && (!strings.Contains(image, "@sha256:") || platform != "linux/amd64") {
			t.Fatalf("4.22 console = %s (%s), want the payload digest on linux/amd64", image, platform)
		}
		if !strings.Contains(string(args), "--platform\n"+platform+"\n") || !strings.HasSuffix(string(args), "\n"+image+"\n") {
			t.Fatalf("%s console launch did not request %s on %s: %s", selector, image, platform, args)
		}
	}
}

// fakeConsoleRuntime records the subcommand of every runtime call and the full
// argument list of the last create.
func fakeConsoleRuntime(t *testing.T) (rt *runtime.Runtime, createArgs, calls string) {
	t.Helper()
	dir := t.TempDir()
	binary := filepath.Join(dir, "docker")
	script := `#!/bin/sh
printf '%s\n' "$1" >> "$OINC_TEST_CALLS"
case "$1" in
  info) printf '{"CgroupVersion":"2"}';;
  create) printf '%s\n' "$@" > "$OINC_TEST_ARGS";;
esac
`
	if err := os.WriteFile(binary, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	createArgs = filepath.Join(dir, "args")
	calls = filepath.Join(dir, "calls")
	t.Setenv("OINC_TEST_ARGS", createArgs)
	t.Setenv("OINC_TEST_CALLS", calls)
	rt, err := runtime.Detect(binary)
	if err != nil {
		t.Fatal(err)
	}
	return rt, createArgs, calls
}

func createdEnv(t *testing.T, createArgs string) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(createArgs)
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	env := map[string]string{}
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-e" {
			key, value, _ := strings.Cut(args[i+1], "=")
			env[key] = value
			i++
		}
	}
	return env
}

func TestStartConsoleBranding(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("OINC_CONFIG_DIR", configDir)
	if err := os.WriteFile(filepath.Join(configDir, "pull-secret.json"), []byte(`{"auths":{"quay.io":{"auth":"dummy"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	const redHatDocs = "https://docs.redhat.com/en/documentation/openshift_container_platform/latest/"
	for _, tc := range []struct {
		name, version, branding, want, wantDocs string
	}{
		{"OKD defaults to OCP branding", "4.22", "", "ocp", redHatDocs},
		{"OKD keeps an explicit choice", "4.22", "okd", "okd", ""},
		{"any Red Hat brand gets Red Hat docs", "5.0", "rosa", "rosa", redHatDocs},
		{"Red Hat preview keeps an explicit choice", "ocp-4.23", "okd", "okd", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, createArgs, _ := fakeConsoleRuntime(t)
			ver, err := version.Resolve(tc.version)
			if err != nil {
				t.Fatal(err)
			}
			if err := startConsoleContainer(rt, ver, "test-token", ConsoleOpts{Port: 9000, Branding: tc.branding}, nil); err != nil {
				t.Fatal(err)
			}
			env := createdEnv(t, createArgs)
			if got := env["BRIDGE_BRANDING"]; got != tc.want {
				t.Fatalf("BRIDGE_BRANDING = %q, want %q", got, tc.want)
			}
			if got := env["BRIDGE_DOCUMENTATION_BASE_URL"]; got != tc.wantDocs {
				t.Fatalf("BRIDGE_DOCUMENTATION_BASE_URL = %q, want %q", got, tc.wantDocs)
			}
		})
	}
}

func TestStartConsoleRejectsUnknownBrandingBeforeReplacingConsole(t *testing.T) {
	rt, _, calls := fakeConsoleRuntime(t)
	ver, err := version.Resolve("4.22")
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(calls)
	if err := startConsoleContainer(rt, ver, "test-token", ConsoleOpts{Port: 9000, Branding: "redhat"}, nil); err == nil {
		t.Fatal("unknown branding was accepted")
	}
	after, _ := os.ReadFile(calls)
	if string(after) != string(before) {
		t.Fatalf("runtime was called before the branding was rejected: %q", strings.TrimPrefix(string(after), string(before)))
	}
}

func TestBridgePluginEnv(t *testing.T) {
	tests := []struct {
		name        string
		plugins     string
		wantPlugins string
		wantI18n    string
	}{
		{
			name:        "one plugin on the host",
			plugins:     "mine=http://host.docker.internal:9001",
			wantPlugins: "mine=http://host.containers.internal:9001",
			wantI18n:    "plugin__mine",
		},
		{
			name:        "several plugins",
			plugins:     "mine=http://localhost:9001,kuadrant-console-plugin=http://host.containers.internal:9005",
			wantPlugins: "mine=http://localhost:9001,kuadrant-console-plugin=http://host.containers.internal:9005",
			wantI18n:    "plugin__mine,plugin__kuadrant-console-plugin",
		},
		{name: "none"},
		{name: "entry without a url is dropped", plugins: "broken"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plugins, i18n := bridgePluginEnv(tt.plugins, "host.containers.internal")
			if plugins != tt.wantPlugins || i18n != tt.wantI18n {
				t.Errorf("bridgePluginEnv(%q) = %q, %q; want %q, %q", tt.plugins, plugins, i18n, tt.wantPlugins, tt.wantI18n)
			}
		})
	}
}

func TestWithConsolePlugin(t *testing.T) {
	const url = "http://host.docker.internal:9005"

	tests := []struct {
		name        string
		current     string
		want        string
		wantChanged bool
		wantErr     string
	}{
		{name: "first plugin", current: "", want: "kuadrant-console-plugin=" + url, wantChanged: true},
		{
			name:        "beside the user's plugin",
			current:     "mine=http://host.docker.internal:9001",
			want:        "mine=http://host.docker.internal:9001,kuadrant-console-plugin=" + url,
			wantChanged: true,
		},
		{
			name:    "already loaded from the same place",
			current: "mine=http://host.docker.internal:9001,kuadrant-console-plugin=" + url,
			want:    "mine=http://host.docker.internal:9001,kuadrant-console-plugin=" + url,
		},
		{
			name:    "already loaded from somewhere else",
			current: "kuadrant-console-plugin=http://host.docker.internal:9001",
			wantErr: "http://host.docker.internal:9001",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, changed, err := withConsolePlugin(tt.current, "kuadrant-console-plugin", url)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want it to name %s", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want || changed != tt.wantChanged {
				t.Errorf("got %q changed=%v, want %q changed=%v", got, changed, tt.want, tt.wantChanged)
			}
		})
	}
}

// reloading the Console for one more plugin must keep what it was started
// with, none of which is stored anywhere but the container.
func TestConsoleSettingsReadBackFromItsContainer(t *testing.T) {
	info := &runtime.ContainerInfo{
		Ports: map[int]int{9000: 9100},
		Env: map[string]string{
			"BRIDGE_PLUGINS":         "mine=http://host.docker.internal:9001",
			"BRIDGE_BRANDING":        "okd",
			"BRIDGE_PLUGIN_PROXY":    `{"services":[]}`,
			"BRIDGE_SERVICE_CA_FILE": "/var/run/oinc/service-ca.crt",
		},
		Binds:      []string{"/home/me/.cache/oinc/service-ca.crt:/var/run/oinc/service-ca.crt:ro"},
		ExtraHosts: map[string]string{"plugin.ns.svc": "192.168.97.200"},
	}

	console, proxy := consoleSettings(info)

	if want := (ConsoleOpts{Port: 9100, Plugin: "mine=http://host.docker.internal:9001", Branding: "okd"}); console != want {
		t.Errorf("console = %+v, want %+v", console, want)
	}
	if proxy == nil {
		t.Fatal("proxy options were dropped")
	}
	if proxy.config != `{"services":[]}` || proxy.caFile != "/home/me/.cache/oinc/service-ca.crt" || proxy.extraHosts["plugin.ns.svc"] != "192.168.97.200" {
		t.Errorf("proxy = %+v, want the config, CA file and hosts kept", proxy)
	}
}

func TestConsoleSettingsOfAPlainConsole(t *testing.T) {
	// host networking on linux publishes no ports
	console, proxy := consoleSettings(&runtime.ContainerInfo{Env: map[string]string{"BRIDGE_BRANDING": "ocp"}})

	if want := (ConsoleOpts{Port: 9000, Branding: "ocp"}); console != want {
		t.Errorf("console = %+v, want %+v", console, want)
	}
	if proxy != nil {
		t.Errorf("proxy = %+v, want none", proxy)
	}
}

// inClusterPluginFixture is a ConsolePlugin as the Kuadrant operator creates
// it, its Service, and the shadow LoadBalancer already holding an address so
// the reconcile does not wait.
func inClusterPluginFixture(proxied bool) (*unstructured.Unstructured, []k8sruntime.Object) {
	const namespace = "kuadrant-system"
	spec := map[string]any{
		"backend": map[string]any{
			"type": "Service",
			"service": map[string]any{
				"name":      "kuadrant-console-plugin",
				"namespace": namespace,
				"port":      int64(9443),
				"basePath":  "/",
			},
		},
	}
	shadowAlias := ""
	if proxied {
		shadowAlias = "backend"
		spec["proxy"] = []any{
			map[string]any{
				"alias":         "backend",
				"authorization": "UserToken",
				"endpoint": map[string]any{
					"type": "Service",
					"service": map[string]any{
						"name":      "kuadrant-console-plugin",
						"namespace": namespace,
						"port":      int64(9443),
					},
				},
			},
		}
	}
	plugin := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "console.openshift.io/v1",
		"kind":       "ConsolePlugin",
		"metadata":   map[string]any{"name": "kuadrant-console-plugin"},
		"spec":       spec,
	}}

	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "kuadrant-console-plugin", Namespace: namespace},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app": "kuadrant-console-plugin"},
			Ports:    []corev1.ServicePort{{Name: "9443-tcp", Port: 9443, TargetPort: intstr.FromInt32(9443), Protocol: corev1.ProtocolTCP}},
		},
	}
	shadow := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      consoleProxyServiceName("kuadrant-console-plugin", shadowAlias),
			Namespace: namespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "oinc",
				"oinc.io/console-plugin":       "kuadrant-console-plugin",
			},
		},
		Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer},
		Status: corev1.ServiceStatus{
			LoadBalancer: corev1.LoadBalancerStatus{Ingress: []corev1.LoadBalancerIngress{{IP: "192.168.215.3"}}},
		},
	}
	serviceCA := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "openshift-service-ca.crt", Namespace: namespace},
		Data:       map[string]string{"service-ca.crt": "SERVICE CA"},
	}
	return plugin, []k8sruntime.Object{service, shadow, serviceCA}
}

func discardCA(map[string]struct{}) (string, error) { return "/tmp/service-ca.crt", nil }

// the plugin's Service serves both its assets and its proxied backend. the
// node offers one LoadBalancer address, so the two must share one shadow
// Service or the second would never get the port.
func TestInClusterConsolePluginSharesOneLoadBalancerForAssetsAndProxy(t *testing.T) {
	plugin, objects := inClusterPluginFixture(true)
	client := kubefake.NewClientset(objects...)
	dynClient := dynamicfake.NewSimpleDynamicClient(k8sruntime.NewScheme(), plugin)

	assets, options, err := inClusterConsolePluginWithCAWriter(context.Background(), client, dynClient,
		"kuadrant-console-plugin", slog.New(slog.NewTextHandler(io.Discard, nil)), discardCA)
	if err != nil {
		t.Fatal(err)
	}

	if want := "https://kuadrant-console-plugin.kuadrant-system.svc:9443/"; assets != want {
		t.Errorf("assets url = %q, want %q", assets, want)
	}
	if got := options.extraHosts["kuadrant-console-plugin.kuadrant-system.svc"]; got != "192.168.215.3" {
		t.Errorf("host address = %q, want the LoadBalancer's", got)
	}
	if options.caFile != "/tmp/service-ca.crt" {
		t.Errorf("CA file = %q, want the service CA written for the Console", options.caFile)
	}
	var config bridgePluginProxy
	if err := json.Unmarshal([]byte(options.config), &config); err != nil {
		t.Fatal(err)
	}
	if len(config.Services) != 1 || config.Services[0].ConsoleAPIPath != "/api/proxy/plugin/kuadrant-console-plugin/backend/" || !config.Services[0].Authorize {
		t.Errorf("proxy config = %+v, want the backend alias with user authorization", config.Services)
	}

	services, err := client.CoreV1().Services("kuadrant-system").List(context.Background(), metav1.ListOptions{LabelSelector: "app.kubernetes.io/managed-by=oinc"})
	if err != nil {
		t.Fatal(err)
	}
	if len(services.Items) != 1 {
		t.Errorf("%d shadow LoadBalancers, want 1 shared by assets and proxy", len(services.Items))
	}
}

// before plugins had a backend the ConsolePlugin listed no proxies; the
// assets still have to be reachable.
func TestInClusterConsolePluginExposesAssetsWithoutProxies(t *testing.T) {
	plugin, objects := inClusterPluginFixture(false)
	client := kubefake.NewClientset(objects...)
	dynClient := dynamicfake.NewSimpleDynamicClient(k8sruntime.NewScheme(), plugin)

	assets, options, err := inClusterConsolePluginWithCAWriter(context.Background(), client, dynClient,
		"kuadrant-console-plugin", slog.New(slog.NewTextHandler(io.Discard, nil)), discardCA)
	if err != nil {
		t.Fatal(err)
	}

	if want := "https://kuadrant-console-plugin.kuadrant-system.svc:9443/"; assets != want {
		t.Errorf("assets url = %q, want %q", assets, want)
	}
	if got := options.extraHosts["kuadrant-console-plugin.kuadrant-system.svc"]; got != "192.168.215.3" {
		t.Errorf("host address = %q, want the LoadBalancer's", got)
	}
	if options.caFile == "" {
		t.Error("the service CA must be trusted for the plugin's serving certificate")
	}
}

// operators restrict who may reach their plugin to the in-cluster Console.
// oinc's Console is outside the cluster, so each exposed Service needs an
// allowance or its LoadBalancer times out.
func TestConsoleLoadBalancerComesWithAnIngressAllowance(t *testing.T) {
	plugin, objects := inClusterPluginFixture(true)
	client := kubefake.NewClientset(objects...)
	dynClient := dynamicfake.NewSimpleDynamicClient(k8sruntime.NewScheme(), plugin)

	if _, _, err := inClusterConsolePluginWithCAWriter(context.Background(), client, dynClient,
		"kuadrant-console-plugin", slog.New(slog.NewTextHandler(io.Discard, nil)), discardCA); err != nil {
		t.Fatal(err)
	}

	policy, err := client.NetworkingV1().NetworkPolicies("kuadrant-system").Get(context.Background(),
		consoleProxyServiceName("kuadrant-console-plugin", "backend"), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("no ingress allowance beside the shadow LoadBalancer: %v", err)
	}
	if got := policy.Spec.PodSelector.MatchLabels; len(got) != 1 || got["app"] != "kuadrant-console-plugin" {
		t.Errorf("pod selector = %v, want the plugin Service's selector", got)
	}
	if len(policy.Spec.Ingress) != 1 || len(policy.Spec.Ingress[0].From) != 0 {
		t.Fatalf("ingress = %+v, want one rule open to any source", policy.Spec.Ingress)
	}
	ports := policy.Spec.Ingress[0].Ports
	if len(ports) != 1 || ports[0].Port == nil || ports[0].Port.IntValue() != 9443 {
		t.Errorf("ports = %+v, want only the Service's target port 9443", ports)
	}
	if policy.Labels["app.kubernetes.io/managed-by"] != "oinc" {
		t.Errorf("labels = %v, want it marked as oinc's", policy.Labels)
	}
}

func TestMergeProxyOptionsKeepsOtherPluginsProxies(t *testing.T) {
	current := &consoleProxyOptions{
		config:     `{"services":[{"consoleAPIPath":"/api/proxy/plugin/other-plugin/api/","endpoint":"https://other.ns.svc:8443/"},{"consoleAPIPath":"/api/proxy/plugin/kuadrant-console-plugin/stale/","endpoint":"https://stale.kuadrant-system.svc:1/"}]}`,
		caFile:     "/cache/service-ca.crt",
		extraHosts: map[string]string{"other.ns.svc": "192.168.215.3", "stale.kuadrant-system.svc": "192.168.215.3"},
	}
	added := &consoleProxyOptions{
		config:     `{"services":[{"consoleAPIPath":"/api/proxy/plugin/kuadrant-console-plugin/backend/","endpoint":"https://kuadrant-console-plugin.kuadrant-system.svc:9443/","authorize":true}]}`,
		caFile:     "/cache/service-ca.crt",
		extraHosts: map[string]string{"kuadrant-console-plugin.kuadrant-system.svc": "192.168.215.3"},
	}

	merged, err := mergeProxyOptions(current, added, "kuadrant-console-plugin")
	if err != nil {
		t.Fatal(err)
	}

	var config bridgePluginProxy
	if err := json.Unmarshal([]byte(merged.config), &config); err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, s := range config.Services {
		paths = append(paths, s.ConsoleAPIPath)
	}
	want := []string{"/api/proxy/plugin/other-plugin/api/", "/api/proxy/plugin/kuadrant-console-plugin/backend/"}
	if strings.Join(paths, " ") != strings.Join(want, " ") {
		t.Errorf("proxied paths = %v, want %v", paths, want)
	}
	if merged.extraHosts["other.ns.svc"] == "" || merged.extraHosts["kuadrant-console-plugin.kuadrant-system.svc"] == "" {
		t.Errorf("hosts = %v, want both plugins' Services", merged.extraHosts)
	}
	if merged.caFile != "/cache/service-ca.crt" {
		t.Errorf("CA file = %q, want it kept", merged.caFile)
	}

	alone, err := mergeProxyOptions(nil, added, "kuadrant-console-plugin")
	if err != nil {
		t.Fatal(err)
	}
	if alone.config != added.config {
		t.Errorf("config without earlier proxies = %s, want %s", alone.config, added.config)
	}
}

// a Console that restarted cleanly may still be unable to reach the plugin
// (no route, an untrusted certificate); that has to fail the install rather
// than show up as an empty navigation in the browser.
func TestWaitForConsolePlugin(t *testing.T) {
	serving := false
	console := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/plugins/kuadrant-console-plugin/plugin-manifest.json" {
			http.NotFound(w, r)
			return
		}
		if !serving {
			http.Error(w, "upstream timeout", http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(`{"name":"kuadrant-console-plugin"}`))
	}))
	t.Cleanup(console.Close)

	err := waitForConsolePlugin(context.Background(), console.URL, "kuadrant-console-plugin", 60*time.Millisecond, 10*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "kuadrant-console-plugin") || !strings.Contains(err.Error(), "502") {
		t.Errorf("err = %v, want it to name the plugin and the Console's 502", err)
	}

	serving = true
	if err := waitForConsolePlugin(context.Background(), console.URL, "kuadrant-console-plugin", time.Second, 10*time.Millisecond); err != nil {
		t.Errorf("err = %v, want nil once the Console serves the plugin", err)
	}
}
