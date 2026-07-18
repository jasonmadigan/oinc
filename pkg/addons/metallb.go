package addons

import (
	"context"
	"fmt"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/yaml"
)

const (
	defaultMetalLBVersion = "0.14.9"
	// services opt in to metallb by setting spec.loadBalancerClass to this
	metalLBClass = "oinc.io/metallb"
)

func init() { Register(&metalLB{}) }

type metalLB struct {
	version string
}

func (m *metalLB) Name() string           { return "metallb" }
func (m *metalLB) Dependencies() []string { return nil }

func (m *metalLB) SetOptions(opts map[string]string) {
	if v, ok := opts["version"]; ok {
		m.version = v
	}
}

func (m *metalLB) resolveVersion() string {
	if m.version != "" {
		return m.version
	}
	return defaultMetalLBVersion
}

func (m *metalLB) Install(ctx context.Context, cfg *Config) error {
	v := m.resolveVersion()
	url := fmt.Sprintf("https://raw.githubusercontent.com/metallb/metallb/v%s/config/manifests/metallb-native.yaml", v)

	cfg.Logger.Info("fetching manifests", "url", url)
	manifest, err := fetchURL(ctx, url)
	if err != nil {
		return err
	}

	// restrict metallb to services that opt in via spec.loadBalancerClass.
	// without this its controller claims every class-less LoadBalancer
	// service and, having no address pools, clears the status microshift's
	// built-in service-lb wrote for openshift-ingress/router-default, which
	// tears down the host port 80/443 bindings Routes rely on. injected into
	// the manifest before apply so the first pod ever created is already
	// scoped and server-side apply ownership of the args list keeps the arg
	// across re-installs.
	manifest, err = injectLBClassArg(manifest)
	if err != nil {
		return fmt.Errorf("scoping metallb to %s: %w", metalLBClass, err)
	}

	if err := applyManifests(ctx, cfg, manifest); err != nil {
		return err
	}

	// microshift has SCCs but doesn't auto-grant them like full OCP.
	// metallb pods need the privileged SCC to schedule.
	grantSCC(ctx, cfg, "privileged", "metallb-system", []string{"controller", "speaker"})

	// backstop for clusters deployed before the arg was injected pre-apply
	arg := "--lb-class=" + metalLBClass
	if err := ensureContainerArg(ctx, cfg, deploymentGVR, "metallb-system", "controller", "controller", arg); err != nil {
		return fmt.Errorf("scoping metallb controller to %s: %w", metalLBClass, err)
	}
	if err := ensureContainerArg(ctx, cfg, daemonSetGVR, "metallb-system", "speaker", "speaker", arg); err != nil {
		return fmt.Errorf("scoping metallb speaker to %s: %w", metalLBClass, err)
	}
	return nil
}

// injectLBClassArg appends --lb-class to the controller Deployment and
// speaker DaemonSet container args in the raw manifest. errors if either
// target is missing so upstream manifest drift is caught. idempotent;
// untouched documents pass through verbatim.
func injectLBClassArg(manifest []byte) ([]byte, error) {
	arg := "--lb-class=" + metalLBClass
	targets := map[string]string{ // kind/name -> container
		"Deployment/controller": "controller",
		"DaemonSet/speaker":     "speaker",
	}

	docs := strings.Split(string(manifest), "\n---\n")
	injected := map[string]bool{}
	for i, doc := range docs {
		var meta struct {
			Kind     string `json:"kind"`
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
		}
		if err := yaml.Unmarshal([]byte(doc), &meta); err != nil {
			continue
		}
		container, ok := targets[meta.Kind+"/"+meta.Metadata.Name]
		if !ok {
			continue
		}

		var obj map[string]any
		if err := yaml.Unmarshal([]byte(doc), &obj); err != nil {
			return nil, fmt.Errorf("parsing %s/%s: %w", meta.Kind, meta.Metadata.Name, err)
		}
		u := &unstructured.Unstructured{Object: obj}
		if _, err := appendContainerArg(u, container, arg); err != nil {
			return nil, fmt.Errorf("%s/%s: %w", meta.Kind, meta.Metadata.Name, err)
		}
		out, err := yaml.Marshal(u.Object)
		if err != nil {
			return nil, fmt.Errorf("marshalling %s/%s: %w", meta.Kind, meta.Metadata.Name, err)
		}
		docs[i] = string(out)
		injected[meta.Kind+"/"+meta.Metadata.Name] = true
	}

	for target := range targets {
		if !injected[target] {
			return nil, fmt.Errorf("%s not found in manifest", target)
		}
	}

	return []byte(strings.Join(docs, "\n---\n")), nil
}

// appendContainerArg adds arg to the named container of a workload's pod
// template unless already present. reports whether the object was changed.
func appendContainerArg(obj *unstructured.Unstructured, container, arg string) (bool, error) {
	containers, found, err := unstructured.NestedSlice(obj.Object, "spec", "template", "spec", "containers")
	if err != nil {
		return false, fmt.Errorf("reading pod template containers: %w", err)
	}
	if !found {
		return false, fmt.Errorf("no pod template containers")
	}

	for i, c := range containers {
		cm, ok := c.(map[string]any)
		if !ok || cm["name"] != container {
			continue
		}
		args, _, _ := unstructured.NestedStringSlice(cm, "args")
		for _, a := range args {
			if a == arg {
				return false, nil
			}
		}
		args = append(args, arg)
		if err := unstructured.SetNestedStringSlice(cm, args, "args"); err != nil {
			return false, err
		}
		containers[i] = cm
		if err := unstructured.SetNestedSlice(obj.Object, containers, "spec", "template", "spec", "containers"); err != nil {
			return false, err
		}
		return true, nil
	}
	return false, fmt.Errorf("container %q not found", container)
}

func (m *metalLB) Ready(ctx context.Context, cfg *Config) error {
	return waitForDeployment(ctx, cfg, "metallb-system", "controller", 5*time.Minute)
}

var sccGVR = schema.GroupVersionResource{
	Group: "security.openshift.io", Version: "v1", Resource: "securitycontextconstraints",
}

var daemonSetGVR = schema.GroupVersionResource{
	Group: "apps", Version: "v1", Resource: "daemonsets",
}

// ensureContainerArg appends an argument to a named container of a workload's
// pod template if not already present. read-modify-write with a conflict
// retry, idempotent.
func ensureContainerArg(ctx context.Context, cfg *Config, gvr schema.GroupVersionResource, namespace, name, container, arg string) error {
	var lastErr error
	for range 3 {
		obj, err := cfg.DynamicClient.Resource(gvr).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return err
		}

		changed, err := appendContainerArg(obj, container, arg)
		if err != nil {
			return fmt.Errorf("%s %s/%s: %w", gvr.Resource, namespace, name, err)
		}
		if !changed {
			return nil
		}

		_, lastErr = cfg.DynamicClient.Resource(gvr).Namespace(namespace).Update(ctx, obj, metav1.UpdateOptions{})
		if lastErr == nil {
			cfg.Logger.Info("added container arg", "resource", gvr.Resource, "name", name, "container", container, "arg", arg)
			return nil
		}
		if !errors.IsConflict(lastErr) {
			return lastErr
		}
	}
	return lastErr
}

// grantSCC adds service accounts to an existing SCC's users list.
// best-effort: silently skips if the SCC API is not available.
func grantSCC(ctx context.Context, cfg *Config, sccName, namespace string, serviceAccounts []string) {
	scc, err := cfg.DynamicClient.Resource(sccGVR).Get(ctx, sccName, metav1.GetOptions{})
	if err != nil {
		cfg.Logger.Info("SCC API not available or SCC not found, skipping", "scc", sccName)
		return
	}

	users, _, _ := unstructured.NestedStringSlice(scc.Object, "users")
	existing := map[string]bool{}
	for _, u := range users {
		existing[u] = true
	}

	changed := false
	for _, sa := range serviceAccounts {
		fqn := fmt.Sprintf("system:serviceaccount:%s:%s", namespace, sa)
		if !existing[fqn] {
			users = append(users, fqn)
			changed = true
		}
	}

	if !changed {
		return
	}

	if err := unstructured.SetNestedStringSlice(scc.Object, users, "users"); err != nil {
		cfg.Logger.Info("failed to set SCC users, skipping", "err", err)
		return
	}

	cfg.Logger.Info("granting SCC", "scc", sccName, "serviceAccounts", serviceAccounts)
	if _, err := cfg.DynamicClient.Resource(sccGVR).Update(ctx, scc, metav1.UpdateOptions{}); err != nil {
		cfg.Logger.Info("failed to update SCC, skipping", "err", err)
	}
}
