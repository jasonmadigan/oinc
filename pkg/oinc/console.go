package oinc

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os/exec"
	goruntime "runtime"
	"strings"
	"time"

	"github.com/jasonmadigan/oinc/pkg/runtime"
	"github.com/jasonmadigan/oinc/pkg/version"

	authv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	consoleSA        = "openshift-console"
	consoleSANS      = "kube-system"
	consoleContainer = "oinc-console"
)

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
	if err := startConsoleContainer(rt, ver, token, consolePort, consolePlugin); err != nil {
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

func startConsoleContainer(rt *runtime.Runtime, ver version.OCPVersion, token string, consolePort int, consolePlugin string) error {
	// remove old console container if present
	_ = rt.RemoveContainer(consoleContainer)

	var apiEndpoint string
	if goruntime.GOOS == "linux" {
		apiEndpoint = "https://localhost:6443"
	} else {
		apiEndpoint = fmt.Sprintf("https://%s:6443", rt.ContainerHostAddress())
	}

	env := map[string]string{
		"BRIDGE_USER_AUTH":                             "disabled",
		"BRIDGE_K8S_MODE":                              "off-cluster",
		"BRIDGE_K8S_AUTH":                              "bearer-token",
		"BRIDGE_K8S_AUTH_BEARER_TOKEN":                 token,
		"BRIDGE_K8S_MODE_OFF_CLUSTER_ENDPOINT":         apiEndpoint,
		"BRIDGE_K8S_MODE_OFF_CLUSTER_SKIP_VERIFY_TLS":  "true",
		"BRIDGE_USER_SETTINGS_LOCATION":                "localstorage",
	}

	if consolePlugin != "" {
		parts := strings.SplitN(consolePlugin, "=", 2)
		if len(parts) == 2 {
			env["BRIDGE_PLUGINS"] = consolePlugin
			env["BRIDGE_I18N_NAMESPACES"] = fmt.Sprintf("plugin__%s", parts[0])
		}
	}

	opts := runtime.ContainerOpts{
		Name:  consoleContainer,
		Image: ver.ConsoleImageRef(),
		Env:   env,
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

	// origin-console is amd64 only
	if goruntime.GOARCH == "arm64" {
		opts.Platform = "linux/amd64"
	}

	if err := rt.CreateContainer(opts); err != nil {
		return err
	}
	return rt.StartContainer(consoleContainer)
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

