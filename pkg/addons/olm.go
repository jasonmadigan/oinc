package addons

import (
	"context"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

var (
	catalogSourceGVR = schema.GroupVersionResource{
		Group: "operators.coreos.com", Version: "v1alpha1", Resource: "catalogsources",
	}
	subscriptionGVR = schema.GroupVersionResource{
		Group: "operators.coreos.com", Version: "v1alpha1", Resource: "subscriptions",
	}
	csvGVR = schema.GroupVersionResource{
		Group: "operators.coreos.com", Version: "v1alpha1", Resource: "clusterserviceversions",
	}
	operatorGroupGVR = schema.GroupVersionResource{
		Group: "operators.coreos.com", Version: "v1", Resource: "operatorgroups",
	}
	namespaceGVR = schema.GroupVersionResource{
		Version: "v1", Resource: "namespaces",
	}
)

const (
	olmNamespace       = "olm"
	operatorsNamespace = "openshift-operators"
)

type catalogDef struct {
	name  string
	image string
}

var (
	communityCatalog = catalogDef{
		name:  "community-operators",
		image: "quay.io/operatorhubio/catalog:latest",
	}
	sailCatalog = catalogDef{
		name:  "sail-operator-catalog",
		image: "quay.io/sail-operator/sail-operator-catalog:3.0-latest",
	}
)

type subscriptionOpts struct {
	name    string
	channel string
	catalog catalogDef
}

func ensureNamespace(ctx context.Context, cfg *Config, name string) error {
	_, err := cfg.DynamicClient.Resource(namespaceGVR).Get(ctx, name, metav1.GetOptions{})
	if err == nil {
		return nil
	}
	if !errors.IsNotFound(err) {
		return err
	}

	ns := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "v1",
			"kind":       "Namespace",
			"metadata":   map[string]any{"name": name},
		},
	}
	_, err = cfg.DynamicClient.Resource(namespaceGVR).Create(ctx, ns, metav1.CreateOptions{})
	return err
}

func ensureCatalogSource(ctx context.Context, cfg *Config, cat catalogDef) error {
	_, err := cfg.DynamicClient.Resource(catalogSourceGVR).Namespace(olmNamespace).Get(ctx, cat.name, metav1.GetOptions{})
	if err == nil {
		return nil
	}
	if !errors.IsNotFound(err) {
		return err
	}

	cfg.Logger.Info("creating catalog source", "name", cat.name, "image", cat.image)
	cs := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "operators.coreos.com/v1alpha1",
			"kind":       "CatalogSource",
			"metadata": map[string]any{
				"name":      cat.name,
				"namespace": olmNamespace,
			},
			"spec": map[string]any{
				"sourceType":  "grpc",
				"image":       cat.image,
				"displayName": cat.name,
				"publisher":   "oinc",
			},
		},
	}

	_, err = cfg.DynamicClient.Resource(catalogSourceGVR).Namespace(olmNamespace).Create(ctx, cs, metav1.CreateOptions{})
	return err
}

func ensureOperatorGroup(ctx context.Context, cfg *Config, namespace string) error {
	list, err := cfg.DynamicClient.Resource(operatorGroupGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	if len(list.Items) > 0 {
		return nil
	}

	og := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "operators.coreos.com/v1",
			"kind":       "OperatorGroup",
			"metadata": map[string]any{
				"name":      namespace,
				"namespace": namespace,
			},
			"spec": map[string]any{},
		},
	}

	_, err = cfg.DynamicClient.Resource(operatorGroupGVR).Namespace(namespace).Create(ctx, og, metav1.CreateOptions{})
	return err
}

func createSubscription(ctx context.Context, cfg *Config, opts subscriptionOpts) error {
	_, err := cfg.DynamicClient.Resource(subscriptionGVR).Namespace(operatorsNamespace).Get(ctx, opts.name, metav1.GetOptions{})
	if err == nil {
		return nil
	}

	sub := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "operators.coreos.com/v1alpha1",
			"kind":       "Subscription",
			"metadata": map[string]any{
				"name":      opts.name,
				"namespace": operatorsNamespace,
			},
			"spec": map[string]any{
				"channel":             opts.channel,
				"name":                opts.name,
				"source":              opts.catalog.name,
				"sourceNamespace":     olmNamespace,
				"installPlanApproval": "Automatic",
			},
		},
	}

	_, err = cfg.DynamicClient.Resource(subscriptionGVR).Namespace(operatorsNamespace).Create(ctx, sub, metav1.CreateOptions{})
	return err
}

// installOperator is the common flow for OLM-based addons.
func installOperator(ctx context.Context, cfg *Config, opts subscriptionOpts) error {
	if err := ensureCatalogSource(ctx, cfg, opts.catalog); err != nil {
		return fmt.Errorf("catalog source: %w", err)
	}
	if err := ensureNamespace(ctx, cfg, operatorsNamespace); err != nil {
		return fmt.Errorf("namespace: %w", err)
	}
	if err := ensureOperatorGroup(ctx, cfg, operatorsNamespace); err != nil {
		return fmt.Errorf("operator group: %w", err)
	}
	return createSubscription(ctx, cfg, opts)
}

// waitForCSV polls until a subscription's CSV reaches Succeeded phase.
func waitForCSV(ctx context.Context, cfg *Config, subName string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		sub, err := cfg.DynamicClient.Resource(subscriptionGVR).Namespace(operatorsNamespace).Get(ctx, subName, metav1.GetOptions{})
		if err != nil {
			time.Sleep(5 * time.Second)
			continue
		}

		csvName, found, _ := unstructured.NestedString(sub.Object, "status", "installedCSV")
		if !found || csvName == "" {
			cfg.Logger.Debug("waiting for operator", "subscription", subName)
			time.Sleep(5 * time.Second)
			continue
		}

		csv, err := cfg.DynamicClient.Resource(csvGVR).Namespace(operatorsNamespace).Get(ctx, csvName, metav1.GetOptions{})
		if err != nil {
			time.Sleep(5 * time.Second)
			continue
		}

		phase, _, _ := unstructured.NestedString(csv.Object, "status", "phase")
		if phase == "Succeeded" {
			cfg.Logger.Info("operator ready", "csv", csvName)
			return nil
		}

		cfg.Logger.Debug("operator installing", "csv", csvName, "phase", phase)
		time.Sleep(5 * time.Second)
	}

	return fmt.Errorf("%s not ready after %s", subName, timeout)
}

// ensureResource creates an unstructured resource if it doesn't exist,
// retrying to handle CRD propagation delay.
func ensureResource(ctx context.Context, cfg *Config, gvr schema.GroupVersionResource, obj *unstructured.Unstructured) error {
	ns := obj.GetNamespace()
	name := obj.GetName()

	var client dynamic.ResourceInterface
	if ns != "" {
		client = cfg.DynamicClient.Resource(gvr).Namespace(ns)
	} else {
		client = cfg.DynamicClient.Resource(gvr)
	}

	_, err := client.Get(ctx, name, metav1.GetOptions{})
	if err == nil {
		return nil
	}

	for range 6 {
		_, err = client.Create(ctx, obj, metav1.CreateOptions{})
		if err == nil {
			return nil
		}
		cfg.Logger.Debug("waiting for CRD", "kind", obj.GetKind(), "err", err)
		time.Sleep(5 * time.Second)
	}

	return fmt.Errorf("creating %s/%s: %w", obj.GetKind(), name, err)
}
