package addons

import (
	"context"
	"fmt"
	"os/exec"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const defaultKuadrantVersion = "1.4.1"

func init() { Register(&kuadrant{}) }

type kuadrant struct {
	version string
}

func (k *kuadrant) Name() string {
	return "kuadrant"
}

func (k *kuadrant) Dependencies() []string {
	return []string{"gateway-api", "cert-manager", "metallb", "istio"}
}

func (k *kuadrant) SetOptions(opts map[string]string) {
	if v, ok := opts["version"]; ok {
		k.version = v
	}
}

func (k *kuadrant) resolveVersion() string {
	if k.version != "" {
		return k.version
	}
	return defaultKuadrantVersion
}

func (k *kuadrant) Install(ctx context.Context, cfg *Config) error {
	if _, err := exec.LookPath("helm"); err != nil {
		return fmt.Errorf("kuadrant addon requires helm: %w", err)
	}

	v := k.resolveVersion()
	cfg.Logger.Info("installing kuadrant operator via helm", "version", v)

	// ensure helm repo
	out, err := exec.CommandContext(ctx, "helm", "repo", "add", "kuadrant",
		"https://kuadrant.io/helm-charts/", "--force-update",
	).CombinedOutput()
	if err != nil {
		return fmt.Errorf("helm repo add: %s: %w", string(out), err)
	}

	out, err = exec.CommandContext(ctx, "helm", "repo", "update", "kuadrant").CombinedOutput()
	if err != nil {
		return fmt.Errorf("helm repo update: %s: %w", string(out), err)
	}

	out, err = exec.CommandContext(ctx, "helm", "upgrade", "--install",
		"kuadrant-operator", "kuadrant/kuadrant-operator",
		"--version", v,
		"--create-namespace",
		"-n", "kuadrant-system",
		"--wait",
		"--timeout", "5m",
	).CombinedOutput()
	if err != nil {
		return fmt.Errorf("helm install kuadrant-operator: %s: %w", string(out), err)
	}

	cfg.Logger.Info("kuadrant operator installed")
	return nil
}

func (k *kuadrant) Ready(ctx context.Context, cfg *Config) error {
	if err := waitForDeployment(ctx, cfg, "kuadrant-system", "kuadrant-operator-controller-manager", 5*time.Minute); err != nil {
		return err
	}

	// create the Kuadrant CR to deploy operand components
	kuadrantGVR := schema.GroupVersionResource{
		Group: "kuadrant.io", Version: "v1beta1", Resource: "kuadrants",
	}

	cr := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "kuadrant.io/v1beta1",
			"kind":       "Kuadrant",
			"metadata": map[string]any{
				"name":      "kuadrant",
				"namespace": "kuadrant-system",
			},
		},
	}

	if err := ensureResource(ctx, cfg, kuadrantGVR, cr); err != nil {
		return err
	}

	// wait for the Kuadrant CR to become ready
	return waitForKuadrantReady(ctx, cfg, 5*time.Minute)
}

func waitForKuadrantReady(ctx context.Context, cfg *Config, timeout time.Duration) error {
	kuadrantGVR := schema.GroupVersionResource{
		Group: "kuadrant.io", Version: "v1beta1", Resource: "kuadrants",
	}

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		obj, err := cfg.DynamicClient.Resource(kuadrantGVR).Namespace("kuadrant-system").Get(ctx, "kuadrant", metav1.GetOptions{})
		if err != nil {
			cfg.Logger.Debug("waiting for kuadrant CR", "err", err)
			time.Sleep(5 * time.Second)
			continue
		}

		conditions, found, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
		if found {
			for _, c := range conditions {
				cm, ok := c.(map[string]any)
				if !ok {
					continue
				}
				if cm["type"] == "Ready" && cm["status"] == "True" {
					cfg.Logger.Info("kuadrant ready")
					return nil
				}
			}
		}

		cfg.Logger.Debug("waiting for kuadrant to become ready")
		time.Sleep(5 * time.Second)
	}

	return fmt.Errorf("kuadrant not ready after %s", timeout)
}
