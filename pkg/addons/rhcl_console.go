package addons

import (
	"context"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/uuid"
)

const (
	// ConsolePlugin and Deployment the operator creates for its plugin
	rhclConsolePlugin = "kuadrant-console-plugin"

	openShiftAPIManifests = "https://raw.githubusercontent.com/openshift/api/%s/config/v1/zz_generated.crd-manifests/%s"
)

// CRDs the operator's console plugin needs on a cluster that is not
// OpenShift. The operator deploys its plugin only where ClusterVersion is
// served. ClusterOperator comes with it because OLM starts reporting there
// as soon as the config.openshift.io group exists, and logs errors if it
// cannot.
var openShiftConfigCRDs = []string{
	"0000_00_cluster-version-operator_01_clusterversions-Default.crd.yaml",
	"0000_00_cluster-version-operator_01_clusteroperators.crd.yaml",
}

var (
	clusterVersionGVR = schema.GroupVersionResource{Group: "config.openshift.io", Version: "v1", Resource: "clusterversions"}
	consolePluginGVR  = schema.GroupVersionResource{Group: "console.openshift.io", Version: "v1", Resource: "consoleplugins"}
)

// ensureClusterVersion gives the cluster the ClusterVersion the operator
// looks for before deploying its console plugin, reporting the OpenShift
// version the cluster corresponds to. The operator picks the plugin build
// from that version, and only checks that the type is served when it starts.
func (r *rhcl) ensureClusterVersion(ctx context.Context, cfg *Config) error {
	if cfg.OpenShiftVersion == "" || cfg.OpenShiftAPIBranch == "" {
		return fmt.Errorf("cannot tell which OpenShift version the cluster corresponds to, which the console plugin needs")
	}
	for _, manifest := range openShiftConfigCRDs {
		url := fmt.Sprintf(openShiftAPIManifests, cfg.OpenShiftAPIBranch, manifest)
		if err := r.applyURL(ctx, cfg, url); err != nil {
			return fmt.Errorf("installing OpenShift config CRDs: %w", err)
		}
	}

	version := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "config.openshift.io/v1",
		"kind":       "ClusterVersion",
		"metadata":   map[string]any{"name": "version"},
		"spec":       map[string]any{"clusterID": string(uuid.NewUUID())},
	}}
	if err := ensureResource(ctx, cfg, clusterVersionGVR, version); err != nil {
		return err
	}

	client := cfg.DynamicClient.Resource(clusterVersionGVR)
	current, err := client.Get(ctx, "version", metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("getting ClusterVersion: %w", err)
	}
	want := cfg.OpenShiftVersion + ".0"
	if desired, _, _ := unstructured.NestedString(current.Object, "status", "desired", "version"); desired == want {
		return nil
	}
	// the fields the API requires; there is no release payload behind them
	current.Object["status"] = map[string]any{
		"desired":            map[string]any{"version": want, "image": "oinc"},
		"observedGeneration": current.GetGeneration(),
		"versionHash":        "oinc",
		"availableUpdates":   []any{},
	}
	if _, err := client.UpdateStatus(ctx, current, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("setting ClusterVersion status: %w", err)
	}
	cfg.Logger.Info("created ClusterVersion for the console plugin", "version", want)
	return nil
}

// loadConsolePlugin waits for the operator to deploy its console plugin and
// has the Console load it. An operator that started before the ClusterVersion
// existed is restarted once after restartAfter, as it never looks again.
func (r *rhcl) loadConsolePlugin(ctx context.Context, cfg *Config, timeout, restartAfter, interval time.Duration) error {
	if cfg.LoadConsolePlugin == nil {
		return fmt.Errorf("no Console to load the rhcl console plugin into")
	}

	cfg.Logger.Info("waiting for the operator to deploy its console plugin")
	start := time.Now()
	deadline := start.Add(timeout)
	restarted := false
	for {
		_, err := cfg.DynamicClient.Resource(consolePluginGVR).Get(ctx, rhclConsolePlugin, metav1.GetOptions{})
		if err == nil {
			break
		}
		if !errors.IsNotFound(err) {
			cfg.Logger.Debug("getting ConsolePlugin", "err", err)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the operator created no ConsolePlugin %s after %s", rhclConsolePlugin, timeout)
		}

		if !restarted && time.Since(start) >= restartAfter {
			restarted = true
			cfg.Logger.Info("restarting the operator so that it finds the ClusterVersion")
			selector, oldUIDs, err := restartOperatorPod(ctx, cfg, kuadrantNamespace, kuadrantOperatorDeployment)
			if err != nil {
				cfg.Logger.Warn("operator pod restart failed", "err", err)
			} else if err := waitForNewOperatorPod(ctx, cfg, kuadrantNamespace, selector, oldUIDs, time.Until(deadline), interval); err != nil {
				cfg.Logger.Warn("operator pod not back after restart", "err", err)
			}
			continue
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}

	if err := waitForDeployment(ctx, cfg, kuadrantNamespace, rhclConsolePlugin, 5*time.Minute); err != nil {
		return fmt.Errorf("console plugin deployed by the operator did not roll out: %w", err)
	}
	if err := cfg.LoadConsolePlugin(ctx, rhclConsolePlugin); err != nil {
		return err
	}
	cfg.Logger.Info("rhcl console plugin loaded", "plugin", rhclConsolePlugin)
	return nil
}
