package oinc

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jasonmadigan/oinc/pkg/kubeconfig"
	"github.com/jasonmadigan/oinc/pkg/runtime"
	"github.com/jasonmadigan/oinc/pkg/version"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/clientcmd"
)

type AddonInfo struct {
	Name  string `json:"name"`
	Ready bool   `json:"ready"`
}

type Status struct {
	State        string      `json:"state"`
	Runtime      string      `json:"runtime"`
	Version      string      `json:"version,omitempty"`
	APIServer    string      `json:"apiServer,omitempty"`
	ConsoleURL   string      `json:"consoleUrl,omitempty"`
	IngressHTTP  string      `json:"ingressHttp,omitempty"`
	IngressHTTPS string      `json:"ingressHttps,omitempty"`
	Uptime       string      `json:"uptime,omitempty"`
	Addons       []AddonInfo `json:"addons,omitempty"`
	Error        string      `json:"error,omitempty"`
}

func GetStatus(runtimeOverride string) Status {
	rt, err := runtime.Detect(runtimeOverride)
	if err != nil {
		return Status{State: "error", Error: err.Error()}
	}

	s := Status{Runtime: rt.Name()}

	info, err := rt.InspectContainer(containerName)
	if err != nil {
		s.State = "not found"
		return s
	}

	if !info.Running {
		s.State = "stopped"
		return s
	}

	s.State = "running"

	if v, ok := version.ResolveFromImage(info.Image); ok {
		s.Version = v.Version
	}

	if !info.StartedAt.IsZero() {
		s.Uptime = formatUptime(time.Since(info.StartedAt))
	}

	if port, ok := info.Ports[6443]; ok {
		s.APIServer = fmt.Sprintf("https://127.0.0.1:%d", port)
	}
	if port, ok := info.Ports[80]; ok {
		s.IngressHTTP = fmt.Sprintf("http://localhost:%d", port)
	}
	if port, ok := info.Ports[443]; ok {
		s.IngressHTTPS = fmt.Sprintf("https://localhost:%d", port)
	}

	consoleInfo, err := rt.InspectContainer(consoleContainer)
	if err == nil && consoleInfo.Running {
		if port, ok := consoleInfo.Ports[9000]; ok {
			s.ConsoleURL = fmt.Sprintf("http://localhost:%d", port)
		}
	}

	s.Addons = detectAddons()

	return s
}

var (
	deploymentGVR = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
	nsGVR         = schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}
	crdGVR        = schema.GroupVersionResource{Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions"}
)

type addonCheck struct {
	name       string
	namespace  string
	deployment string
}

var addonChecks = []addonCheck{
	{"cert-manager", "cert-manager", "cert-manager-webhook"},
	{"metallb", "metallb-system", "controller"},
	{"istio", "istio-system", "istiod"},
	{"kuadrant", "kuadrant-system", "kuadrant-operator-controller-manager"},
	{"rhdh", "rhdh", "rhdh-developer-hub"},
}

func detectAddons() []AddonInfo {
	kc, err := kubeconfig.Read()
	if err != nil {
		return nil
	}
	config, err := clientcmd.RESTConfigFromKubeConfig(kc)
	if err != nil {
		return nil
	}
	config.Timeout = 3 * time.Second

	dyn, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil
	}

	ctx := context.Background()
	var addons []AddonInfo

	for _, check := range addonChecks {
		_, err := dyn.Resource(nsGVR).Get(ctx, check.namespace, metav1.GetOptions{})
		if err != nil {
			continue
		}
		ready := deploymentReady(ctx, dyn, check.namespace, check.deployment)
		addons = append(addons, AddonInfo{Name: check.name, Ready: ready})
	}

	// gateway-api: CRD-only, ready if CRD is established
	_, err = dyn.Resource(crdGVR).Get(ctx, "gatewayclasses.gateway.networking.k8s.io", metav1.GetOptions{})
	if err == nil {
		addons = append(addons, AddonInfo{Name: "gateway-api", Ready: true})
	}

	sort.Slice(addons, func(i, j int) bool { return addons[i].Name < addons[j].Name })
	return addons
}

func deploymentReady(ctx context.Context, dyn dynamic.Interface, ns, name string) bool {
	dep, err := dyn.Resource(deploymentGVR).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return false
	}
	avail, _, _ := unstructured.NestedInt64(dep.Object, "status", "availableReplicas")
	return avail > 0
}

// PodInfo holds summary info for a single pod.
type PodInfo struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Ready     string `json:"ready"`
	Status    string `json:"status"`
}

// GetPods returns pods in openshift-* namespaces.
func GetPods() []PodInfo {
	kc, err := kubeconfig.Read()
	if err != nil {
		return nil
	}
	config, err := clientcmd.RESTConfigFromKubeConfig(kc)
	if err != nil {
		return nil
	}
	config.Timeout = 5 * time.Second

	dyn, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil
	}

	podGVR := schema.GroupVersionResource{Version: "v1", Resource: "pods"}
	list, err := dyn.Resource(podGVR).Namespace("").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		return nil
	}

	var pods []PodInfo
	for _, item := range list.Items {
		ns := item.GetNamespace()
		if !strings.HasPrefix(ns, "openshift-") && ns != "kube-system" {
			continue
		}

		phase, _, _ := unstructured.NestedString(item.Object, "status", "phase")

		// count ready/total containers
		containers, _, _ := unstructured.NestedSlice(item.Object, "status", "containerStatuses")
		total := len(containers)
		readyCount := 0
		for _, c := range containers {
			if cs, ok := c.(map[string]any); ok {
				if r, ok := cs["ready"].(bool); ok && r {
					readyCount++
				}
			}
		}

		pods = append(pods, PodInfo{
			Name:      item.GetName(),
			Namespace: ns,
			Ready:     fmt.Sprintf("%d/%d", readyCount, total),
			Status:    phase,
		})
	}

	sort.Slice(pods, func(i, j int) bool {
		if pods[i].Namespace != pods[j].Namespace {
			return pods[i].Namespace < pods[j].Namespace
		}
		return pods[i].Name < pods[j].Name
	})

	return pods
}

func formatUptime(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		h := int(d.Hours())
		m := int(d.Minutes()) % 60
		if m == 0 {
			return fmt.Sprintf("%dh", h)
		}
		return fmt.Sprintf("%dh %dm", h, m)
	}
	days := int(d.Hours()) / 24
	h := int(d.Hours()) % 24
	if h == 0 {
		return fmt.Sprintf("%dd", days)
	}
	return fmt.Sprintf("%dd %dh", days, h)
}
