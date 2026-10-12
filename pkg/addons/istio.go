package addons

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	defaultSailVersion  = "1.29.0"
	defaultIstioVersion = "v1.29.0"
)

func init() { Register(&istio{}) }

type istio struct {
	version string
}

func (i *istio) Name() string           { return "istio" }
func (i *istio) Dependencies() []string { return nil }

func (i *istio) SetOptions(opts map[string]string) {
	if v, ok := opts["version"]; ok {
		i.version = v
	}
}

func (i *istio) resolveSailVersion() string {
	if i.version != "" {
		return i.version
	}
	return defaultSailVersion
}

func (i *istio) Install(ctx context.Context, cfg *Config) error {
	if _, err := exec.LookPath("helm"); err != nil {
		return fmt.Errorf("istio addon requires helm: %w", err)
	}

	v := i.resolveSailVersion()
	chartURL := fmt.Sprintf("https://github.com/istio-ecosystem/sail-operator/releases/download/%s/sail-operator-%s.tgz", v, v)
	cfg.Logger.Info("installing sail operator via helm", "version", v)

	dir, chart, err := pullChart(ctx, cfg, chartURL)
	if err != nil {
		return fmt.Errorf("pulling sail-operator chart: %w", err)
	}
	defer os.RemoveAll(dir)

	out, err := exec.CommandContext(ctx, "helm", "upgrade", "--install",
		"sail-operator", chart,
		"--create-namespace",
		"-n", "sail-operator",
		"--wait",
		"--timeout", "5m",
	).CombinedOutput()
	if err != nil {
		return fmt.Errorf("helm install sail-operator: %s: %w", string(out), err)
	}

	cfg.Logger.Info("sail operator installed")
	return nil
}

func (i *istio) Ready(ctx context.Context, cfg *Config) error {
	if err := waitForDeployment(ctx, cfg, "sail-operator", "sail-operator", 5*time.Minute); err != nil {
		return err
	}

	if err := ensureNamespace(ctx, cfg, "istio-system"); err != nil {
		return err
	}

	istioGVR := schema.GroupVersionResource{
		Group: "sailoperator.io", Version: "v1", Resource: "istios",
	}

	// use the istio version that matches the sail operator version
	istioVersion := defaultIstioVersion
	if i.version != "" {
		istioVersion = "v" + i.version
	}

	cr := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "sailoperator.io/v1",
			"kind":       "Istio",
			"metadata": map[string]any{
				"name": "default",
			},
			"spec": map[string]any{
				"namespace": "istio-system",
				"version":   istioVersion,
			},
		},
	}

	return ensureResource(ctx, cfg, istioGVR, cr)
}
