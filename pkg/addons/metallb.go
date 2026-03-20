package addons

import (
	"context"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const defaultMetalLBVersion = "0.14.9"

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
	if err := applyManifestURL(ctx, cfg, url); err != nil {
		return err
	}
	// microshift has SCCs but doesn't auto-grant them like full OCP.
	// metallb pods need the privileged SCC to schedule.
	grantSCC(ctx, cfg, "privileged", "metallb-system", []string{"controller", "speaker"})
	return nil
}

func (m *metalLB) Ready(ctx context.Context, cfg *Config) error {
	return waitForDeployment(ctx, cfg, "metallb-system", "controller", 5*time.Minute)
}

var sccGVR = schema.GroupVersionResource{
	Group: "security.openshift.io", Version: "v1", Resource: "securitycontextconstraints",
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
