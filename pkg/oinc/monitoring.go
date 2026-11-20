package oinc

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jasonmadigan/oinc/pkg/addons"
	"github.com/jasonmadigan/oinc/pkg/version"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	monitoringCRDManifests = "https://raw.githubusercontent.com/openshift/cluster-monitoring-operator/%s/manifests/%s"

	crdEstablishTimeout  = time.Minute
	crdEstablishInterval = time.Second
)

type crdManifest struct {
	name string
	// file in the repository's manifests directory
	file string
}

// the monitoring.coreos.com CRDs every OpenShift release installs from
// cluster-monitoring-operator. operators built for OpenShift can need them to
// start: kuadrant-operator caches ServiceMonitors and PodMonitors.
// monitoring.openshift.io types are left out, as only that operator acts on them
var monitoringCRDs = []crdManifest{
	{"alertmanagerconfigs.monitoring.coreos.com", "0000_50_cluster-monitoring-operator_00_0alertmanager-config-custom-resource-definition.yaml"},
	{"alertmanagers.monitoring.coreos.com", "0000_50_cluster-monitoring-operator_00_0alertmanager-custom-resource-definition.yaml"},
	{"podmonitors.monitoring.coreos.com", "0000_50_cluster-monitoring-operator_00_0podmonitor-custom-resource-definition.yaml"},
	{"probes.monitoring.coreos.com", "0000_50_cluster-monitoring-operator_00_0probe-custom-resource-definition.yaml"},
	{"prometheuses.monitoring.coreos.com", "0000_50_cluster-monitoring-operator_00_0prometheus-custom-resource-definition.yaml"},
	{"prometheusrules.monitoring.coreos.com", "0000_50_cluster-monitoring-operator_00_0prometheusrule-custom-resource-definition.yaml"},
	{"servicemonitors.monitoring.coreos.com", "0000_50_cluster-monitoring-operator_00_0servicemonitor-custom-resource-definition.yaml"},
	{"thanosrulers.monitoring.coreos.com", "0000_50_cluster-monitoring-operator_00_0thanosruler-custom-resource-definition.yaml"},
}

// setupMonitoringCRDs gives the cluster the monitoring CRDs of its release.
func setupMonitoringCRDs(ctx context.Context, kubeconfig []byte, ver version.OCPVersion, logger *slog.Logger) error {
	config, err := clientcmd.RESTConfigFromKubeConfig(kubeconfig)
	if err != nil {
		return fmt.Errorf("building rest config: %w", err)
	}
	client, err := dynamic.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("creating dynamic client: %w", err)
	}
	return applyMonitoringCRDs(ctx, client, ver.APIBranch, monitoringCRDs, logger)
}

// applyMonitoringCRDs creates or updates crds from the release branch's
// manifests and waits until they are served.
func applyMonitoringCRDs(ctx context.Context, client dynamic.Interface, branch string, crds []crdManifest, logger *slog.Logger) error {
	names := make([]string, 0, len(crds))
	for _, crd := range crds {
		url := fmt.Sprintf(monitoringCRDManifests, branch, crd.file)
		obj, err := fetchCRD(ctx, logger, url, crd.name)
		if err != nil {
			return err
		}
		if err := servedAtStorageVersion(obj); err != nil {
			return fmt.Errorf("%s: %w", url, err)
		}
		if err := applyCRDObject(ctx, client, logger, obj); err != nil {
			return err
		}
		names = append(names, crd.name)
	}
	return waitForCRDsEstablished(ctx, client, names, crdEstablishTimeout, crdEstablishInterval)
}

// servedAtStorageVersion strips a CRD's conversion webhook and every version
// but the stored one (AlertmanagerConfig). the webhook is a service OpenShift
// runs and oinc does not, and without it a stored object fails to convert,
// which blocks deleting its namespace
func servedAtStorageVersion(crd *unstructured.Unstructured) error {
	strategy, _, _ := unstructured.NestedString(crd.Object, "spec", "conversion", "strategy")
	if strategy != "Webhook" {
		return nil
	}
	versions, _, _ := unstructured.NestedSlice(crd.Object, "spec", "versions")
	var stored []any
	for _, v := range versions {
		if version, ok := v.(map[string]any); ok && version["storage"] == true {
			stored = append(stored, version)
		}
	}
	if len(stored) != 1 || stored[0].(map[string]any)["served"] != true {
		return fmt.Errorf("CRD %s has no single served storage version to keep", crd.GetName())
	}
	if err := unstructured.SetNestedSlice(crd.Object, stored, "spec", "versions"); err != nil {
		return err
	}
	unstructured.RemoveNestedField(crd.Object, "spec", "conversion")
	// service-ca would inject the webhook's CA
	annotations := crd.GetAnnotations()
	delete(annotations, "service.beta.openshift.io/inject-cabundle")
	crd.SetAnnotations(annotations)
	return nil
}

// monitoringCRDsToAdd returns the monitoring CRDs a cluster created by an
// earlier oinc lacks. ones it has are left alone, whoever installed them.
// nothing when the cluster's release is unknown, as its manifests cannot be
// picked.
func monitoringCRDsToAdd(ctx context.Context, cfg *addons.Config) ([]crdManifest, error) {
	var missing []crdManifest
	var names []string
	for _, crd := range monitoringCRDs {
		_, err := cfg.DynamicClient.Resource(crdGVR).Get(ctx, crd.name, metav1.GetOptions{})
		if errors.IsNotFound(err) {
			missing = append(missing, crd)
			names = append(names, crd.name)
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("getting CRD %s: %w", crd.name, err)
		}
	}
	if len(missing) == 0 {
		return nil, nil
	}
	if cfg.OpenShiftAPIBranch == "" {
		cfg.Logger.Warn("cluster lacks monitoring CRDs that OpenShift serves, but its OpenShift release is unknown, so they are not added", "missing", names)
		return nil, nil
	}
	return missing, nil
}

// ensureMonitoringCRDs adds the monitoring CRDs a cluster created by an
// earlier oinc lacks.
func ensureMonitoringCRDs(ctx context.Context, cfg *addons.Config) error {
	add, err := monitoringCRDsToAdd(ctx, cfg)
	if err != nil || len(add) == 0 {
		return err
	}
	cfg.Logger.Info("adding missing monitoring CRDs", "release", cfg.OpenShiftAPIBranch, "count", len(add))
	return applyMonitoringCRDs(ctx, cfg.DynamicClient, cfg.OpenShiftAPIBranch, add, cfg.Logger)
}
