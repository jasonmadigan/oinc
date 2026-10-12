package addons

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/jasonmadigan/oinc/pkg/download"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// pullChart downloads a chart archive into a new temp directory, retrying
// failed downloads as helm never does, so installs and renders never fetch.
// the caller removes dir; nothing is left behind on error.
func pullChart(ctx context.Context, cfg *Config, chart string, args ...string) (dir, archive string, err error) {
	tmp, err := os.MkdirTemp("", "oinc-chart-")
	if err != nil {
		return "", "", fmt.Errorf("creating chart directory: %w", err)
	}
	defer func() {
		if err != nil {
			os.RemoveAll(tmp)
		}
	}()

	pullArgs := append([]string{"pull", chart, "--destination", tmp}, args...)
	err = download.Retry(ctx, cfg.Logger, chart, func() error {
		out, err := exec.CommandContext(ctx, "helm", pullArgs...).CombinedOutput()
		if err != nil {
			return fmt.Errorf("%s: %w", strings.TrimSpace(string(out)), err)
		}
		return nil
	})
	if err != nil {
		return "", "", err
	}

	entries, err := os.ReadDir(tmp)
	if err != nil {
		return "", "", fmt.Errorf("reading chart directory: %w", err)
	}
	if len(entries) != 1 || entries[0].IsDir() || filepath.Ext(entries[0].Name()) != ".tgz" {
		return "", "", fmt.Errorf("expected one chart archive in %s", tmp)
	}
	return tmp, filepath.Join(tmp, entries[0].Name()), nil
}

// applyManifests applies a multi-document YAML manifest using kubectl server-side apply.
func applyManifests(ctx context.Context, cfg *Config, manifest []byte) error {
	if _, err := exec.LookPath("kubectl"); err != nil {
		return fmt.Errorf("kubectl is required but not found in PATH")
	}
	kubeconfigFile, err := os.CreateTemp("", "oinc-kubeconfig-*.yaml")
	if err != nil {
		return fmt.Errorf("creating temp kubeconfig: %w", err)
	}
	defer os.Remove(kubeconfigFile.Name())

	if _, err := kubeconfigFile.Write(cfg.Kubeconfig); err != nil {
		kubeconfigFile.Close()
		return fmt.Errorf("writing temp kubeconfig: %w", err)
	}
	kubeconfigFile.Close()

	cmd := exec.CommandContext(ctx, "kubectl", "apply",
		"--kubeconfig", kubeconfigFile.Name(),
		"--server-side",
		"--force-conflicts",
		"-f", "-",
	)
	cmd.Stdin = bytes.NewReader(manifest)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("kubectl apply: %s: %w", string(out), err)
	}
	return nil
}

// applyManifestURL fetches a URL and applies all objects.
func applyManifestURL(ctx context.Context, cfg *Config, url string) error {
	cfg.Logger.Info("fetching manifests", "url", url)
	data, err := download.URL(ctx, cfg.Logger, url)
	if err != nil {
		return err
	}
	return applyManifests(ctx, cfg, data)
}

var deploymentGVR = schema.GroupVersionResource{
	Group: "apps", Version: "v1", Resource: "deployments",
}

// waitForDeployment polls until a deployment has available replicas.
func waitForDeployment(ctx context.Context, cfg *Config, namespace, name string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		dep, err := cfg.DynamicClient.Resource(deploymentGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
		if err == nil {
			avail, _, _ := unstructured.NestedInt64(dep.Object, "status", "availableReplicas")
			if avail > 0 {
				cfg.Logger.Info("deployment ready", "namespace", namespace, "name", name)
				return nil
			}
		}
		cfg.Logger.Debug("waiting for deployment", "namespace", namespace, "name", name)
		time.Sleep(5 * time.Second)
	}
	return fmt.Errorf("deployment %s/%s not ready after %s", namespace, name, timeout)
}

