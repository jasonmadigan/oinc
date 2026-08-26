package oinc

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"sort"
	"strings"
	"time"

	"github.com/jasonmadigan/oinc/pkg/kubeconfig"
	"github.com/jasonmadigan/oinc/pkg/runtime"
	"github.com/jasonmadigan/oinc/pkg/version"

	authv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
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
)

var consolePluginGVR = schema.GroupVersionResource{
	Group: "console.openshift.io", Version: "v1", Resource: "consoleplugins",
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

func setupConsole(rt *runtime.Runtime, kubeconfig []byte, ver version.OCPVersion, consolePort int, consolePlugin string, logger *slog.Logger) error {
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
	if err := applyConsolePluginCRD(dynClient, ver); err != nil {
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
	if err := startConsoleContainer(rt, ver, token, consolePort, consolePlugin, nil); err != nil {
		return fmt.Errorf("starting console: %w", err)
	}

	// wait for console to be reachable
	logger.Info("waiting for console", "url", fmt.Sprintf("http://localhost:%d", consolePort))
	if err := waitForConsole(consolePort); err != nil {
		return fmt.Errorf("console not reachable: %w", err)
	}

	logger.Info("console ready", "url", fmt.Sprintf("http://localhost:%d", consolePort))
	return nil
}

func applyConsolePluginCRD(dynClient dynamic.Interface, ver version.OCPVersion) error {
	crdURL := ver.ConsolePluginCRDURL()

	if _, err := exec.LookPath("curl"); err != nil {
		return fmt.Errorf("curl is required but not found in PATH")
	}
	body, err := exec.Command("curl", "-sSL", "--retry", "3", "--max-time", "30", crdURL).Output()
	if err != nil {
		return fmt.Errorf("fetching CRD from %s: %w", crdURL, err)
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

func startConsoleContainer(rt *runtime.Runtime, ver version.OCPVersion, token string, consolePort int, consolePlugin string, proxyOptions *consoleProxyOptions) error {
	// remove old console container if present
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

	if consolePlugin != "" {
		resolved := resolvePluginURL(consolePlugin, rt.ContainerHostAddress())
		parts := strings.SplitN(resolved, "=", 2)
		if len(parts) == 2 {
			env["BRIDGE_PLUGINS"] = resolved
			env["BRIDGE_I18N_NAMESPACES"] = fmt.Sprintf("plugin__%s", parts[0])
		}
	}
	if proxyOptions != nil {
		env["BRIDGE_PLUGIN_PROXY"] = proxyOptions.config
		if proxyOptions.caFile != "" {
			env["BRIDGE_SERVICE_CA_FILE"] = consoleProxyCAPath
		}
	}

	opts := runtime.ContainerOpts{
		Name:  consoleContainer,
		Image: ver.ConsoleImageRef(),
		Env:   env,
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
			{Host: consolePort, Container: 9000},
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
func SyncConsolePluginProxy(ctx context.Context, runtimeOverride, pluginName, consolePlugin string, consolePort int, logger *slog.Logger) error {
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
	if err := startConsoleContainer(rt, ver, token, consolePort, consolePlugin, proxyOptions); err != nil {
		return fmt.Errorf("restarting Console with plugin proxies: %w", err)
	}
	if err := waitForConsole(consolePort); err != nil {
		return err
	}
	logger.Info("console plugin proxies configured", "plugin", pluginName, "url", fmt.Sprintf("http://localhost:%d", consolePort))
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
	proxyEntries, found, err := unstructured.NestedSlice(plugin.Object, "spec", "proxy")
	if err != nil {
		return nil, fmt.Errorf("reading ConsolePlugin proxies: %w", err)
	}
	if !found {
		proxyEntries = []any{}
	}

	result := &consoleProxyOptions{extraHosts: map[string]string{}}
	proxyConfig := bridgePluginProxy{Services: []bridgePluginProxyService{}}
	caBundles := map[string]struct{}{}
	for _, rawEntry := range proxyEntries {
		entry, ok := rawEntry.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("ConsolePlugin %s has an invalid proxy entry", pluginName)
		}
		alias, _, _ := unstructured.NestedString(entry, "alias")
		authorization, _, _ := unstructured.NestedString(entry, "authorization")
		endpointType, _, _ := unstructured.NestedString(entry, "endpoint", "type")
		serviceName, _, _ := unstructured.NestedString(entry, "endpoint", "service", "name")
		namespace, _, _ := unstructured.NestedString(entry, "endpoint", "service", "namespace")
		port, _, _ := unstructured.NestedInt64(entry, "endpoint", "service", "port")
		if endpointType != "Service" || alias == "" || serviceName == "" || namespace == "" || port < 1 || port > 65535 {
			return nil, fmt.Errorf("ConsolePlugin %s has an incomplete service proxy entry", pluginName)
		}

		externalIP, err := ensureConsoleProxyLoadBalancer(ctx, client, pluginName, alias, namespace, serviceName, int32(port), logger)
		if err != nil {
			return nil, err
		}
		serviceHost := fmt.Sprintf("%s.%s.svc", serviceName, namespace)
		result.extraHosts[serviceHost] = externalIP
		proxyService := bridgePluginProxyService{
			ConsoleAPIPath: fmt.Sprintf("/api/proxy/plugin/%s/%s/", pluginName, alias),
			Endpoint:       fmt.Sprintf("https://%s:%d/", serviceHost, port),
			Authorize:      authorization == "UserToken",
		}

		if customCA, _, _ := unstructured.NestedString(entry, "caCertificate"); customCA != "" {
			proxyService.CACertificate = customCA
		} else {
			caConfigMap, err := client.CoreV1().ConfigMaps(namespace).Get(ctx, "openshift-service-ca.crt", metav1.GetOptions{})
			if err != nil {
				return nil, fmt.Errorf("getting service CA in namespace %s: %w", namespace, err)
			}
			if ca := caConfigMap.Data["service-ca.crt"]; ca != "" {
				caBundles[ca] = struct{}{}
			}
		}
		proxyConfig.Services = append(proxyConfig.Services, proxyService)
	}

	encodedConfig, err := json.Marshal(proxyConfig)
	if err != nil {
		return nil, fmt.Errorf("encoding Bridge plugin proxy configuration: %w", err)
	}
	result.config = string(encodedConfig)
	if len(caBundles) > 0 {
		caFile, err := writeCA(caBundles)
		if err != nil {
			return nil, err
		}
		result.caFile = caFile
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
