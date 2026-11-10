package oinc

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	goruntime "runtime"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/jasonmadigan/oinc/pkg/download"
	"github.com/jasonmadigan/oinc/pkg/kubeconfig"
	"github.com/jasonmadigan/oinc/pkg/pullsecret"
	"github.com/jasonmadigan/oinc/pkg/runtime"
	"github.com/jasonmadigan/oinc/pkg/version"

	authv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	consoleSA          = "openshift-console"
	consoleSANS        = "kube-system"
	consoleContainer   = "oinc-console"
	consoleProxyCAPath = "/var/run/oinc/service-ca.crt"

	// DefaultConsoleBranding shows Red Hat OpenShift branding; Bridge itself defaults to okd.
	DefaultConsoleBranding = "ocp"

	// non-okd brandings build docs links from this with no fallback. latest,
	// matching OKD's own default; versioned urls 404 for unreleased minors
	redHatDocsBaseURL = "https://docs.redhat.com/en/documentation/openshift_container_platform/latest/"
)

// ConsoleBrandings lists the values Bridge accepts for --branding.
func ConsoleBrandings() []string {
	return []string{"okd", "openshift", "ocp", "online", "dedicated", "azure", "rosa"}
}

// ValidateConsoleBranding rejects values Bridge would refuse at start-up.
func ValidateConsoleBranding(branding string) error {
	if slices.Contains(ConsoleBrandings(), branding) {
		return nil
	}
	return fmt.Errorf("unknown console branding %q; use one of %s", branding, strings.Join(ConsoleBrandings(), ", "))
}

var consolePluginGVR = schema.GroupVersionResource{
	Group: "console.openshift.io", Version: "v1", Resource: "consoleplugins",
}

// ConsoleOpts configures the standalone development Console.
type ConsoleOpts struct {
	Port     int
	Plugin   string // name=url
	Branding string
}

type consoleProxyOptions struct {
	config     string
	caFile     string
	extraHosts map[string]string
}

type bridgePluginProxy struct {
	Services []bridgePluginProxyService `json:"services"`
}

type bridgePluginProxyService struct {
	ConsoleAPIPath string `json:"consoleAPIPath"`
	Endpoint       string `json:"endpoint"`
	Authorize      bool   `json:"authorize,omitempty"`
	CACertificate  string `json:"caCertificate,omitempty"`
}

func setupConsole(rt *runtime.Runtime, kubeconfig []byte, ver version.OCPVersion, console ConsoleOpts, logger *slog.Logger) error {
	config, err := clientcmd.RESTConfigFromKubeConfig(kubeconfig)
	if err != nil {
		return fmt.Errorf("building rest config: %w", err)
	}

	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("creating k8s client: %w", err)
	}

	dynClient, err := dynamic.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("creating dynamic client: %w", err)
	}

	// apply ConsolePlugin CRD
	logger.Info("installing ConsolePlugin CRD")
	if err := applyConsolePluginCRD(dynClient, ver, logger); err != nil {
		return fmt.Errorf("applying ConsolePlugin CRD: %w", err)
	}

	// create SA + RBAC
	logger.Info("creating console service account and RBAC")
	if err := createConsoleRBAC(client); err != nil {
		return fmt.Errorf("creating console RBAC: %w", err)
	}

	// generate bearer token
	logger.Info("generating bearer token")
	token, err := createBearerToken(client)
	if err != nil {
		return fmt.Errorf("creating bearer token: %w", err)
	}

	// start console container
	logger.Info("starting console container")
	if err := startConsoleContainer(rt, ver, token, console, nil); err != nil {
		return fmt.Errorf("starting console: %w", err)
	}

	// wait for console to be reachable
	logger.Info("waiting for console", "url", fmt.Sprintf("http://localhost:%d", console.Port))
	if err := waitForConsole(console.Port); err != nil {
		return fmt.Errorf("console not reachable: %w", err)
	}

	logger.Info("console ready", "url", fmt.Sprintf("http://localhost:%d", console.Port))
	return nil
}

func applyConsolePluginCRD(dynClient dynamic.Interface, ver version.OCPVersion, logger *slog.Logger) error {
	body, err := download.URL(context.TODO(), logger, ver.ConsolePluginCRDURL())
	if err != nil {
		return err
	}

	obj := &unstructured.Unstructured{}
	if err := yaml.NewYAMLOrJSONDecoder(strings.NewReader(string(body)), len(body)).Decode(obj); err != nil {
		return fmt.Errorf("decoding CRD YAML: %w", err)
	}

	crdGVR := schema.GroupVersionResource{
		Group:    "apiextensions.k8s.io",
		Version:  "v1",
		Resource: "customresourcedefinitions",
	}

	existing, err := dynClient.Resource(crdGVR).Get(context.TODO(), obj.GetName(), metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			_, err = dynClient.Resource(crdGVR).Create(context.TODO(), obj, metav1.CreateOptions{})
			return err
		}
		return err
	}

	obj.SetResourceVersion(existing.GetResourceVersion())
	_, err = dynClient.Resource(crdGVR).Update(context.TODO(), obj, metav1.UpdateOptions{})
	return err
}

func createConsoleRBAC(client kubernetes.Interface) error {
	ctx := context.TODO()

	// service account
	sa := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{Name: consoleSA, Namespace: consoleSANS},
	}
	if _, err := client.CoreV1().ServiceAccounts(consoleSANS).Get(ctx, consoleSA, metav1.GetOptions{}); err != nil {
		if errors.IsNotFound(err) {
			if _, err := client.CoreV1().ServiceAccounts(consoleSANS).Create(ctx, sa, metav1.CreateOptions{}); err != nil {
				return err
			}
		} else {
			return err
		}
	}

	// cluster-admin binding
	binding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "oinc-console-admin"},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     "cluster-admin",
		},
		Subjects: []rbacv1.Subject{{
			Kind:      "ServiceAccount",
			Name:      consoleSA,
			Namespace: consoleSANS,
		}},
	}
	if _, err := client.RbacV1().ClusterRoleBindings().Get(ctx, binding.Name, metav1.GetOptions{}); err != nil {
		if errors.IsNotFound(err) {
			if _, err := client.RbacV1().ClusterRoleBindings().Create(ctx, binding, metav1.CreateOptions{}); err != nil {
				return err
			}
		} else {
			return err
		}
	}

	// impersonation role + binding so the console can impersonate users
	impRole := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{Name: "oinc-console-impersonator"},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{""},
				Resources: []string{"users", "groups", "serviceaccounts"},
				Verbs:     []string{"impersonate"},
			},
		},
	}
	if _, err := client.RbacV1().ClusterRoles().Get(ctx, impRole.Name, metav1.GetOptions{}); err != nil {
		if errors.IsNotFound(err) {
			if _, err := client.RbacV1().ClusterRoles().Create(ctx, impRole, metav1.CreateOptions{}); err != nil {
				return err
			}
		} else {
			return err
		}
	}

	impBinding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "oinc-console-impersonator"},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     impRole.Name,
		},
		Subjects: []rbacv1.Subject{{
			Kind:      "ServiceAccount",
			Name:      consoleSA,
			Namespace: consoleSANS,
		}},
	}
	if _, err := client.RbacV1().ClusterRoleBindings().Get(ctx, impBinding.Name, metav1.GetOptions{}); err != nil {
		if errors.IsNotFound(err) {
			if _, err := client.RbacV1().ClusterRoleBindings().Create(ctx, impBinding, metav1.CreateOptions{}); err != nil {
				return err
			}
		} else {
			return err
		}
	}

	return nil
}

func createBearerToken(client kubernetes.Interface) (string, error) {
	expiry := int64(30 * 24 * 3600) // 30 days, regenerated on each create
	req := &authv1.TokenRequest{
		Spec: authv1.TokenRequestSpec{
			ExpirationSeconds: &expiry,
		},
	}
	resp, err := client.CoreV1().ServiceAccounts(consoleSANS).CreateToken(context.TODO(), consoleSA, req, metav1.CreateOptions{})
	if err != nil {
		return "", err
	}
	return resp.Status.Token, nil
}

func startConsoleContainer(rt *runtime.Runtime, ver version.OCPVersion, token string, console ConsoleOpts, proxyOptions *consoleProxyOptions) error {
	branding := console.Branding
	if branding == "" {
		branding = DefaultConsoleBranding
	}
	if err := ValidateConsoleBranding(branding); err != nil {
		return err
	}

	image, platform := ver.ConsoleImageRef()
	if ver.RequiresPullSecret {
		path, err := pullsecret.RequiredPath()
		if err != nil {
			return err
		}
		if err := rt.PullImageWithAuth(image, platform, path); err != nil {
			return err
		}
	}
	// Keep the existing Console until its replacement image is available.
	_ = rt.RemoveContainer(consoleContainer)

	apiEndpoint := fmt.Sprintf("https://%s:6443", rt.ContainerHostAddress())

	env := map[string]string{
		"BRIDGE_USER_AUTH":                            "disabled",
		"BRIDGE_K8S_MODE":                             "off-cluster",
		"BRIDGE_K8S_AUTH":                             "bearer-token",
		"BRIDGE_K8S_AUTH_BEARER_TOKEN":                token,
		"BRIDGE_K8S_MODE_OFF_CLUSTER_ENDPOINT":        apiEndpoint,
		"BRIDGE_K8S_MODE_OFF_CLUSTER_SKIP_VERIFY_TLS": "true",
		"BRIDGE_USER_SETTINGS_LOCATION":               "localstorage",
	}

	if plugins, namespaces := bridgePluginEnv(console.Plugin, rt.ContainerHostAddress()); plugins != "" {
		env["BRIDGE_PLUGINS"] = plugins
		env["BRIDGE_I18N_NAMESPACES"] = namespaces
	}
	if proxyOptions != nil {
		env["BRIDGE_PLUGIN_PROXY"] = proxyOptions.config
		if proxyOptions.caFile != "" {
			env["BRIDGE_SERVICE_CA_FILE"] = consoleProxyCAPath
		}
	}

	env["BRIDGE_BRANDING"] = branding
	if branding != "okd" {
		env["BRIDGE_DOCUMENTATION_BASE_URL"] = redHatDocsBaseURL
	}

	opts := runtime.ContainerOpts{
		Name:     consoleContainer,
		Image:    image,
		Platform: platform,
		Env:      env,
	}
	if proxyOptions != nil {
		opts.ExtraHosts = proxyOptions.extraHosts
		if proxyOptions.caFile != "" {
			opts.Volumes = append(opts.Volumes, proxyOptions.caFile+":"+consoleProxyCAPath+":ro")
		}
	}

	// linux: use host networking so the console can reach localhost services
	// (plugin dev servers, etc). macOS/windows: use port mapping.
	if goruntime.GOOS == "linux" {
		opts.Network = "host"
	} else {
		opts.Ports = []runtime.PortMapping{
			{Host: console.Port, Container: 9000},
		}
	}

	if err := rt.CreateContainer(opts); err != nil {
		return err
	}
	return rt.StartContainer(consoleContainer)
}

// SyncConsolePluginProxy makes the standalone development Console honour the
// proxy entries reconciled into a ConsolePlugin resource. Real OpenShift uses
// the Console operator for this translation; OINC deliberately runs Bridge on
// its own and therefore needs this explicit development adapter.
func SyncConsolePluginProxy(ctx context.Context, runtimeOverride, pluginName string, console ConsoleOpts, logger *slog.Logger) error {
	rt, err := runtime.DetectOwner(runtimeOverride, containerName)
	if err != nil {
		return err
	}
	rawKubeconfig, err := kubeconfig.Read()
	if err != nil {
		return fmt.Errorf("reading kubeconfig: %w", err)
	}
	config, err := clientcmd.RESTConfigFromKubeConfig(rawKubeconfig)
	if err != nil {
		return fmt.Errorf("building rest config: %w", err)
	}
	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("creating k8s client: %w", err)
	}
	dynClient, err := dynamic.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("creating dynamic client: %w", err)
	}

	proxyOptions, err := buildConsoleProxyOptions(ctx, client, dynClient, pluginName, logger)
	if err != nil {
		return err
	}
	status := GetStatus(rt.Name())
	if status.Version == "" {
		return fmt.Errorf("could not determine the running OCP version")
	}
	ver, err := version.Resolve(status.Version)
	if err != nil {
		return fmt.Errorf("resolving running OCP version: %w", err)
	}
	if err := createConsoleRBAC(client); err != nil {
		return fmt.Errorf("creating console RBAC: %w", err)
	}
	token, err := createBearerToken(client)
	if err != nil {
		return fmt.Errorf("creating console bearer token: %w", err)
	}
	if err := startConsoleContainer(rt, ver, token, console, proxyOptions); err != nil {
		return fmt.Errorf("restarting Console with plugin proxies: %w", err)
	}
	if err := waitForConsole(console.Port); err != nil {
		return err
	}
	logger.Info("console plugin proxies configured", "plugin", pluginName, "url", fmt.Sprintf("http://localhost:%d", console.Port))
	return nil
}

func buildConsoleProxyOptions(ctx context.Context, client kubernetes.Interface, dynClient dynamic.Interface, pluginName string, logger *slog.Logger) (*consoleProxyOptions, error) {
	return buildConsoleProxyOptionsWithCAWriter(ctx, client, dynClient, pluginName, logger, writeConsoleServiceCA)
}

func buildConsoleProxyOptionsWithCAWriter(ctx context.Context, client kubernetes.Interface, dynClient dynamic.Interface, pluginName string, logger *slog.Logger, writeCA func(map[string]struct{}) (string, error)) (*consoleProxyOptions, error) {
	plugin, err := dynClient.Resource(consolePluginGVR).Get(ctx, pluginName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("getting ConsolePlugin %s: %w", pluginName, err)
	}
	exposure := newConsoleExposure(ctx, client, pluginName, logger)
	if err := exposure.proxies(plugin); err != nil {
		return nil, err
	}
	return exposure.options(writeCA)
}

// inClusterConsolePluginWithCAWriter makes a plugin that an operator serves
// inside the cluster reachable from the standalone Console, and returns the
// URL of its assets with the proxy settings the Console needs.
func inClusterConsolePluginWithCAWriter(ctx context.Context, client kubernetes.Interface, dynClient dynamic.Interface, pluginName string, logger *slog.Logger, writeCA func(map[string]struct{}) (string, error)) (string, *consoleProxyOptions, error) {
	plugin, err := dynClient.Resource(consolePluginGVR).Get(ctx, pluginName, metav1.GetOptions{})
	if err != nil {
		return "", nil, fmt.Errorf("getting ConsolePlugin %s: %w", pluginName, err)
	}
	exposure := newConsoleExposure(ctx, client, pluginName, logger)
	// proxies first, so a Service that also serves the assets keeps the
	// shadow sync-plugin-proxy would give it
	if err := exposure.proxies(plugin); err != nil {
		return "", nil, err
	}
	assets, err := exposure.backend(plugin)
	if err != nil {
		return "", nil, err
	}
	options, err := exposure.options(writeCA)
	if err != nil {
		return "", nil, err
	}
	return assets, options, nil
}

// consoleExposure collects what the standalone Console needs to reach a
// plugin's Services inside the cluster.
type consoleExposure struct {
	ctx        context.Context
	client     kubernetes.Interface
	pluginName string
	logger     *slog.Logger

	services []bridgePluginProxyService
	// service host -> LoadBalancer address
	hosts map[string]string
	// namespace/service:port that already has a LoadBalancer. the node
	// offers one address, so two shadows cannot hold the same port
	exposed   map[string]bool
	caBundles map[string]struct{}
}

func newConsoleExposure(ctx context.Context, client kubernetes.Interface, pluginName string, logger *slog.Logger) *consoleExposure {
	return &consoleExposure{
		ctx:        ctx,
		client:     client,
		pluginName: pluginName,
		logger:     logger,
		services:   []bridgePluginProxyService{},
		hosts:      map[string]string{},
		exposed:    map[string]bool{},
		caBundles:  map[string]struct{}{},
	}
}

// expose gives a Service a shadow LoadBalancer and returns the host name the
// Console reaches it by.
func (e *consoleExposure) expose(alias, namespace, serviceName string, port int32) (string, error) {
	host := fmt.Sprintf("%s.%s.svc", serviceName, namespace)
	key := fmt.Sprintf("%s/%s:%d", namespace, serviceName, port)
	if e.exposed[key] {
		return host, nil
	}
	address, err := ensureConsoleProxyLoadBalancer(e.ctx, e.client, e.pluginName, alias, namespace, serviceName, port, e.logger)
	if err != nil {
		return "", err
	}
	e.exposed[key] = true
	e.hosts[host] = address
	return host, nil
}

func (e *consoleExposure) trustServiceCA(namespace string) error {
	caConfigMap, err := e.client.CoreV1().ConfigMaps(namespace).Get(e.ctx, "openshift-service-ca.crt", metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("getting service CA in namespace %s: %w", namespace, err)
	}
	if ca := caConfigMap.Data["service-ca.crt"]; ca != "" {
		e.caBundles[ca] = struct{}{}
	}
	return nil
}

// proxies exposes the Services behind the plugin's proxy entries.
func (e *consoleExposure) proxies(plugin *unstructured.Unstructured) error {
	proxyEntries, _, err := unstructured.NestedSlice(plugin.Object, "spec", "proxy")
	if err != nil {
		return fmt.Errorf("reading ConsolePlugin proxies: %w", err)
	}
	for _, rawEntry := range proxyEntries {
		entry, ok := rawEntry.(map[string]any)
		if !ok {
			return fmt.Errorf("ConsolePlugin %s has an invalid proxy entry", e.pluginName)
		}
		alias, _, _ := unstructured.NestedString(entry, "alias")
		authorization, _, _ := unstructured.NestedString(entry, "authorization")
		endpointType, _, _ := unstructured.NestedString(entry, "endpoint", "type")
		serviceName, _, _ := unstructured.NestedString(entry, "endpoint", "service", "name")
		namespace, _, _ := unstructured.NestedString(entry, "endpoint", "service", "namespace")
		port, _, _ := unstructured.NestedInt64(entry, "endpoint", "service", "port")
		if endpointType != "Service" || alias == "" || serviceName == "" || namespace == "" || port < 1 || port > 65535 {
			return fmt.Errorf("ConsolePlugin %s has an incomplete service proxy entry", e.pluginName)
		}

		serviceHost, err := e.expose(alias, namespace, serviceName, int32(port))
		if err != nil {
			return err
		}
		proxyService := bridgePluginProxyService{
			ConsoleAPIPath: fmt.Sprintf("/api/proxy/plugin/%s/%s/", e.pluginName, alias),
			Endpoint:       fmt.Sprintf("https://%s:%d/", serviceHost, port),
			Authorize:      authorization == "UserToken",
		}

		if customCA, _, _ := unstructured.NestedString(entry, "caCertificate"); customCA != "" {
			proxyService.CACertificate = customCA
		} else if err := e.trustServiceCA(namespace); err != nil {
			return err
		}
		e.services = append(e.services, proxyService)
	}
	return nil
}

// backend exposes the Service the plugin's assets are served from and
// returns their URL.
func (e *consoleExposure) backend(plugin *unstructured.Unstructured) (string, error) {
	backendType, _, _ := unstructured.NestedString(plugin.Object, "spec", "backend", "type")
	serviceName, _, _ := unstructured.NestedString(plugin.Object, "spec", "backend", "service", "name")
	namespace, _, _ := unstructured.NestedString(plugin.Object, "spec", "backend", "service", "namespace")
	port, _, _ := unstructured.NestedInt64(plugin.Object, "spec", "backend", "service", "port")
	basePath, _, _ := unstructured.NestedString(plugin.Object, "spec", "backend", "service", "basePath")
	if backendType != "Service" || serviceName == "" || namespace == "" || port < 1 || port > 65535 {
		return "", fmt.Errorf("ConsolePlugin %s has no service backend to load its assets from", e.pluginName)
	}
	if basePath == "" {
		basePath = "/"
	}

	host, err := e.expose("", namespace, serviceName, int32(port))
	if err != nil {
		return "", err
	}
	if err := e.trustServiceCA(namespace); err != nil {
		return "", err
	}
	return fmt.Sprintf("https://%s:%d%s", host, port, basePath), nil
}

func (e *consoleExposure) options(writeCA func(map[string]struct{}) (string, error)) (*consoleProxyOptions, error) {
	encodedConfig, err := json.Marshal(bridgePluginProxy{Services: e.services})
	if err != nil {
		return nil, fmt.Errorf("encoding Bridge plugin proxy configuration: %w", err)
	}
	result := &consoleProxyOptions{config: string(encodedConfig), extraHosts: e.hosts}
	if len(e.caBundles) > 0 {
		caFile, err := writeCA(e.caBundles)
		if err != nil {
			return nil, err
		}
		result.caFile = caFile
	}
	return result, nil
}

// mergeProxyOptions replaces one plugin's proxies in the Console's settings
// and keeps every other plugin's.
func mergeProxyOptions(current, added *consoleProxyOptions, pluginName string) (*consoleProxyOptions, error) {
	if current == nil {
		return added, nil
	}
	var currentConfig, addedConfig bridgePluginProxy
	if current.config != "" {
		if err := json.Unmarshal([]byte(current.config), &currentConfig); err != nil {
			return nil, fmt.Errorf("decoding the Console's plugin proxy configuration: %w", err)
		}
	}
	if err := json.Unmarshal([]byte(added.config), &addedConfig); err != nil {
		return nil, fmt.Errorf("decoding plugin proxy configuration: %w", err)
	}

	merged := bridgePluginProxy{Services: []bridgePluginProxyService{}}
	prefix := fmt.Sprintf("/api/proxy/plugin/%s/", pluginName)
	for _, service := range currentConfig.Services {
		if !strings.HasPrefix(service.ConsoleAPIPath, prefix) {
			merged.Services = append(merged.Services, service)
		}
	}
	merged.Services = append(merged.Services, addedConfig.Services...)
	encodedConfig, err := json.Marshal(merged)
	if err != nil {
		return nil, fmt.Errorf("encoding Bridge plugin proxy configuration: %w", err)
	}

	result := &consoleProxyOptions{config: string(encodedConfig), caFile: added.caFile, extraHosts: map[string]string{}}
	if result.caFile == "" {
		result.caFile = current.caFile
	}
	for host, address := range current.extraHosts {
		result.extraHosts[host] = address
	}
	for host, address := range added.extraHosts {
		result.extraHosts[host] = address
	}
	return result, nil
}

func ensureConsoleProxyLoadBalancer(ctx context.Context, client kubernetes.Interface, pluginName, alias, namespace, serviceName string, port int32, logger *slog.Logger) (string, error) {
	original, err := client.CoreV1().Services(namespace).Get(ctx, serviceName, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("getting proxy Service %s/%s: %w", namespace, serviceName, err)
	}
	var servicePort *corev1.ServicePort
	for i := range original.Spec.Ports {
		if original.Spec.Ports[i].Port == port {
			copy := original.Spec.Ports[i]
			servicePort = &copy
			break
		}
	}
	if servicePort == nil {
		return "", fmt.Errorf("proxy Service %s/%s has no port %d", namespace, serviceName, port)
	}
	if servicePort.TargetPort == (intstr.IntOrString{}) {
		servicePort.TargetPort = intstr.FromInt32(port)
	}
	servicePort.NodePort = 0
	servicePort.Name = "proxy"

	shadowName := consoleProxyServiceName(pluginName, alias)
	services := client.CoreV1().Services(namespace)
	shadow, err := services.Get(ctx, shadowName, metav1.GetOptions{})
	if errors.IsNotFound(err) {
		shadow = &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name: shadowName,
				Labels: map[string]string{
					"app.kubernetes.io/managed-by": "oinc",
					"oinc.io/console-plugin":       pluginName,
				},
			},
			Spec: corev1.ServiceSpec{
				Type:     corev1.ServiceTypeLoadBalancer,
				Selector: original.Spec.Selector,
				Ports:    []corev1.ServicePort{*servicePort},
			},
		}
		shadow, err = services.Create(ctx, shadow, metav1.CreateOptions{})
	} else if err == nil {
		if shadow.Labels["app.kubernetes.io/managed-by"] != "oinc" || shadow.Labels["oinc.io/console-plugin"] != pluginName {
			return "", fmt.Errorf("development proxy LoadBalancer %s/%s already exists and is not managed by OINC for ConsolePlugin %s", namespace, shadowName, pluginName)
		}
		shadow.Spec.Type = corev1.ServiceTypeLoadBalancer
		shadow.Spec.Selector = original.Spec.Selector
		shadow.Spec.Ports = []corev1.ServicePort{*servicePort}
		shadow, err = services.Update(ctx, shadow, metav1.UpdateOptions{})
	}
	if err != nil {
		return "", fmt.Errorf("ensuring development proxy LoadBalancer %s/%s: %w", namespace, shadowName, err)
	}
	if err := allowConsoleIngress(ctx, client, shadowName, pluginName, namespace, original.Spec.Selector, *servicePort); err != nil {
		return "", err
	}
	logger.Info("waiting for Console proxy LoadBalancer", "service", namespace+"/"+shadowName)

	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		current, getErr := services.Get(ctx, shadow.Name, metav1.GetOptions{})
		if getErr != nil {
			return "", fmt.Errorf("checking development proxy LoadBalancer: %w", getErr)
		}
		if len(current.Status.LoadBalancer.Ingress) > 0 {
			address := current.Status.LoadBalancer.Ingress[0].IP
			if address == "" {
				address = current.Status.LoadBalancer.Ingress[0].Hostname
			}
			if address != "" {
				return address, nil
			}
		}
		time.Sleep(time.Second)
	}
	return "", fmt.Errorf("development proxy LoadBalancer %s/%s has no address after 2m; install the metallb addon with an address pool", namespace, shadowName)
}

// allowConsoleIngress admits traffic from outside the cluster to the pods
// behind a shadow LoadBalancer. Operators let only the in-cluster Console
// reach their plugin, and the standalone Console runs outside the cluster.
func allowConsoleIngress(ctx context.Context, client kubernetes.Interface, name, pluginName, namespace string, selector map[string]string, port corev1.ServicePort) error {
	// an empty selector would open the port on every pod in the namespace
	if len(selector) == 0 {
		return nil
	}
	protocol := port.Protocol
	if protocol == "" {
		protocol = corev1.ProtocolTCP
	}
	spec := networkingv1.NetworkPolicySpec{
		PodSelector: metav1.LabelSelector{MatchLabels: selector},
		PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
		Ingress: []networkingv1.NetworkPolicyIngressRule{{
			Ports: []networkingv1.NetworkPolicyPort{{Protocol: &protocol, Port: &port.TargetPort}},
		}},
	}

	policies := client.NetworkingV1().NetworkPolicies(namespace)
	existing, err := policies.Get(ctx, name, metav1.GetOptions{})
	if errors.IsNotFound(err) {
		_, err = policies.Create(ctx, &networkingv1.NetworkPolicy{
			ObjectMeta: metav1.ObjectMeta{
				Name: name,
				Labels: map[string]string{
					"app.kubernetes.io/managed-by": "oinc",
					"oinc.io/console-plugin":       pluginName,
				},
			},
			Spec: spec,
		}, metav1.CreateOptions{})
	} else if err == nil {
		if existing.Labels["app.kubernetes.io/managed-by"] != "oinc" {
			return fmt.Errorf("NetworkPolicy %s/%s already exists and is not managed by OINC", namespace, name)
		}
		existing.Spec = spec
		_, err = policies.Update(ctx, existing, metav1.UpdateOptions{})
	}
	if err != nil {
		return fmt.Errorf("allowing the Console to reach %s/%s: %w", namespace, name, err)
	}
	return nil
}

func consoleProxyServiceName(pluginName, alias string) string {
	identity := "oinc-" + pluginName + "-" + alias
	name := strings.ToLower(identity)
	name = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' {
			return r
		}
		return '-'
	}, name)
	name = strings.Trim(name, "-")
	if name != identity || len(name) > 63 {
		digest := sha256.Sum256([]byte(identity))
		if len(name) > 54 {
			name = name[:54]
		}
		name = strings.TrimRight(name, "-") + fmt.Sprintf("-%x", digest[:4])
	}
	return name
}

func writeConsoleServiceCA(bundles map[string]struct{}) (string, error) {
	if len(bundles) == 0 {
		return "", fmt.Errorf("no service CA was found for Console plugin proxies")
	}
	ordered := make([]string, 0, len(bundles))
	for bundle := range bundles {
		ordered = append(ordered, strings.TrimSpace(bundle))
	}
	sort.Strings(ordered)
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("finding cache directory: %w", err)
	}
	directory := filepath.Join(cacheDir, "oinc")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", fmt.Errorf("creating Console CA directory: %w", err)
	}
	path := filepath.Join(directory, "service-ca.crt")
	if err := os.WriteFile(path, []byte(strings.Join(ordered, "\n")+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("writing Console service CA: %w", err)
	}
	return path, nil
}

// resolvePluginURL rewrites the host in a console plugin spec to match the
// detected container runtime. Accepts:
//   - name=http://host.containers.internal:9001 → rewritten to correct host
//   - name=http://host.docker.internal:9001     → rewritten to correct host
//   - name=http://localhost:9001                 → left as-is (explicit)
func resolvePluginURL(spec, containerHost string) string {
	parts := strings.SplitN(spec, "=", 2)
	if len(parts) != 2 {
		return spec
	}

	url := parts[1]
	// rewrite known container hostnames to match the detected runtime
	for _, placeholder := range []string{"host.containers.internal", "host.docker.internal"} {
		if strings.Contains(url, placeholder) {
			url = strings.Replace(url, placeholder, containerHost, 1)
			return parts[0] + "=" + url
		}
	}

	return spec
}

func waitForConsole(port int) error {
	url := fmt.Sprintf("http://localhost:%d", port)
	httpClient := &http.Client{Timeout: 2 * time.Second}

	for range 30 {
		resp, err := httpClient.Get(url)
		if err == nil {
			resp.Body.Close()
			return nil
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("console not reachable at %s after 60s", url)
}

// bridgePluginEnv renders plugin wiring (name=url, comma-separated) as
// Bridge's plugin and i18n namespace settings, pointing container host
// placeholders at the runtime's host address.
func bridgePluginEnv(plugins, containerHost string) (bridgePlugins, i18nNamespaces string) {
	var specs, namespaces []string
	for _, spec := range strings.Split(plugins, ",") {
		resolved := resolvePluginURL(strings.TrimSpace(spec), containerHost)
		name, _, ok := strings.Cut(resolved, "=")
		if !ok || name == "" {
			continue
		}
		specs = append(specs, resolved)
		namespaces = append(namespaces, "plugin__"+name)
	}
	return strings.Join(specs, ","), strings.Join(namespaces, ",")
}

// withConsolePlugin adds name=url to plugin wiring. changed is false when the
// plugin is already loaded from that url.
func withConsolePlugin(plugins, name, url string) (result string, changed bool, err error) {
	var specs []string
	for _, spec := range strings.Split(plugins, ",") {
		spec = strings.TrimSpace(spec)
		if spec == "" {
			continue
		}
		if existing, existingURL, _ := strings.Cut(spec, "="); existing == name {
			if existingURL == url {
				return plugins, false, nil
			}
			return "", false, fmt.Errorf("the Console already loads %s from %s; remove that --console-plugin to load it from %s", name, existingURL, url)
		}
		specs = append(specs, spec)
	}
	return strings.Join(append(specs, name+"="+url), ","), true, nil
}

// consoleSettings reads back what the Console container was started with.
func consoleSettings(info *runtime.ContainerInfo) (ConsoleOpts, *consoleProxyOptions) {
	console := ConsoleOpts{
		Port:     9000,
		Plugin:   info.Env["BRIDGE_PLUGINS"],
		Branding: info.Env["BRIDGE_BRANDING"],
	}
	if port, ok := info.Ports[9000]; ok {
		console.Port = port
	}

	if info.Env["BRIDGE_PLUGIN_PROXY"] == "" {
		return console, nil
	}
	proxy := &consoleProxyOptions{
		config:     info.Env["BRIDGE_PLUGIN_PROXY"],
		extraHosts: info.ExtraHosts,
	}
	for _, bind := range info.Binds {
		if source, rest, ok := strings.Cut(bind, ":"); ok && strings.HasPrefix(rest, consoleProxyCAPath) {
			proxy.caFile = source
		}
	}
	return console, proxy
}

// loadConsolePlugin restarts the Console so that it also loads a plugin its
// operator serves inside the cluster, keeping the rest of its settings. The
// Console starts before addons do, so one that brings a plugin has to reload it.
func loadConsolePlugin(ctx context.Context, rt *runtime.Runtime, rawKubeconfig []byte, name string, logger *slog.Logger) error {
	config, err := clientcmd.RESTConfigFromKubeConfig(rawKubeconfig)
	if err != nil {
		return fmt.Errorf("building rest config: %w", err)
	}
	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("creating k8s client: %w", err)
	}
	dynClient, err := dynamic.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("creating dynamic client: %w", err)
	}

	assets, added, err := inClusterConsolePluginWithCAWriter(ctx, client, dynClient, name, logger, writeConsoleServiceCA)
	if err != nil {
		return err
	}

	current, err := rt.InspectContainer(consoleContainer)
	if err != nil {
		return fmt.Errorf("finding the Console to load %s into: %w", name, err)
	}
	console, currentProxy := consoleSettings(current)
	plugins, changed, err := withConsolePlugin(console.Plugin, name, assets)
	if err != nil {
		return err
	}
	proxyOptions, err := mergeProxyOptions(currentProxy, added, name)
	if err != nil {
		return err
	}
	consoleURL := fmt.Sprintf("http://localhost:%d", console.Port)
	if !changed && current.Running && sameProxyOptions(currentProxy, proxyOptions) {
		return waitForConsolePlugin(ctx, consoleURL, name, time.Minute, 2*time.Second)
	}
	console.Plugin = plugins

	cluster, err := rt.InspectContainer(containerName)
	if err != nil {
		return fmt.Errorf("inspecting %s container: %w", containerName, err)
	}
	ver, ok := version.ResolveFromImage(cluster.Image)
	if !ok {
		return fmt.Errorf("could not determine the running OCP version from %s", cluster.Image)
	}
	token, err := createBearerToken(client)
	if err != nil {
		return fmt.Errorf("creating console bearer token: %w", err)
	}

	logger.Info("restarting console to load plugin", "plugin", name, "url", assets)
	if err := startConsoleContainer(rt, ver, token, console, proxyOptions); err != nil {
		return fmt.Errorf("restarting Console with %s: %w", name, err)
	}
	if err := waitForConsole(console.Port); err != nil {
		return err
	}
	return waitForConsolePlugin(ctx, consoleURL, name, 2*time.Minute, 2*time.Second)
}

func sameProxyOptions(a, b *consoleProxyOptions) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.config == b.config && a.caFile == b.caFile && maps.Equal(a.extraHosts, b.extraHosts)
}

// waitForConsolePlugin polls until the Console serves a plugin's manifest,
// which proves it can reach and trust wherever the plugin is served from.
func waitForConsolePlugin(ctx context.Context, consoleURL, name string, timeout, interval time.Duration) error {
	url := fmt.Sprintf("%s/api/plugins/%s/plugin-manifest.json", consoleURL, name)
	// Bridge can take a while to give up on a plugin it cannot reach
	httpClient := &http.Client{Timeout: 20 * time.Second}
	deadline := time.Now().Add(timeout)
	last := "no response"
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		resp, err := httpClient.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
			last = resp.Status
		} else {
			last = err.Error()
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the Console cannot load plugin %s (%s): check that it can reach and trust the plugin's Service", name, last)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}
