package addons

import (
	"context"
	"fmt"
	"slices"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
)

var namespaceGVR = schema.GroupVersionResource{
	Version: "v1", Resource: "namespaces",
}

var (
	operatorGroupGVR = schema.GroupVersionResource{Group: "operators.coreos.com", Version: "v1", Resource: "operatorgroups"}
	catalogSourceGVR = schema.GroupVersionResource{Group: "operators.coreos.com", Version: "v1alpha1", Resource: "catalogsources"}
	subscriptionGVR  = schema.GroupVersionResource{Group: "operators.coreos.com", Version: "v1alpha1", Resource: "subscriptions"}
	installPlanGVR   = schema.GroupVersionResource{Group: "operators.coreos.com", Version: "v1alpha1", Resource: "installplans"}
	csvGVR           = schema.GroupVersionResource{Group: "operators.coreos.com", Version: "v1alpha1", Resource: "clusterserviceversions"}
)

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

// olmOperator is an operator installed by an OLM Subscription from a
// CatalogSource in its own namespace.
type olmOperator struct {
	namespace          string
	catalog            string
	catalogImage       string
	catalogDisplayName string
	// re-pull the catalog image every 5m
	catalogPoll bool
	// package, also the Subscription name
	pkg     string
	channel string
	// approve InstallPlans here instead of leaving every upgrade to OLM
	manual bool
	// with manual, the only CSV whose plan is approved; empty approves the
	// channel head once
	csv string
	// extra operator environment
	env []any
}

// install creates the operator's OLM resources and waits for its CSV to
// succeed. existing resources are left as they are.
func (o olmOperator) install(ctx context.Context, cfg *Config, timeout, interval time.Duration) error {
	if err := ensureNamespace(ctx, cfg, o.namespace); err != nil {
		return fmt.Errorf("create namespace: %w", err)
	}
	if err := ensureResource(ctx, cfg, operatorGroupGVR, o.operatorGroup()); err != nil {
		return fmt.Errorf("create operatorgroup: %w", err)
	}
	if err := ensureResource(ctx, cfg, catalogSourceGVR, o.catalogSource()); err != nil {
		return fmt.Errorf("create catalogsource: %w", err)
	}
	if err := ensureResource(ctx, cfg, subscriptionGVR, o.subscription()); err != nil {
		return fmt.Errorf("create subscription: %w", err)
	}
	return o.wait(ctx, cfg, timeout, interval)
}

// operatorGroup targets no namespaces, which OLM treats as all of them.
func (o olmOperator) operatorGroup() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "operators.coreos.com/v1",
		"kind":       "OperatorGroup",
		"metadata":   map[string]any{"name": o.namespace, "namespace": o.namespace},
		"spec":       map[string]any{"upgradeStrategy": "Default"},
	}}
}

func (o olmOperator) catalogSource() *unstructured.Unstructured {
	spec := map[string]any{
		"sourceType":  "grpc",
		"image":       o.catalogImage,
		"displayName": o.catalogDisplayName,
		"publisher":   "grpc",
	}
	if o.catalogPoll {
		spec["updateStrategy"] = map[string]any{
			"registryPoll": map[string]any{"interval": "5m"},
		}
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "operators.coreos.com/v1alpha1",
		"kind":       "CatalogSource",
		"metadata":   map[string]any{"name": o.catalog, "namespace": o.namespace},
		"spec":       spec,
	}}
}

func (o olmOperator) subscription() *unstructured.Unstructured {
	spec := map[string]any{
		"channel":             o.channel,
		"installPlanApproval": "Automatic",
		"name":                o.pkg,
		"source":              o.catalog,
		"sourceNamespace":     o.namespace,
	}
	if o.manual {
		spec["installPlanApproval"] = "Manual"
	}
	if o.csv != "" {
		spec["startingCSV"] = o.csv
	}
	if len(o.env) > 0 {
		spec["config"] = map[string]any{"env": o.env}
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "operators.coreos.com/v1alpha1",
		"kind":       "Subscription",
		"metadata":   map[string]any{"name": o.pkg, "namespace": o.namespace},
		"spec":       spec,
	}}
}

// consecutive polls for which a ready catalog must fail to satisfy the
// subscription before the install gives up. resolution failures while the
// catalog pod starts clear by themselves.
const unsatisfiablePolls = 6

func (o olmOperator) wait(ctx context.Context, cfg *Config, timeout, interval time.Duration) error {
	deadline := time.Now().Add(timeout)
	waiting := "subscription not reconciled"
	unsatisfiable := 0
	for time.Now().Before(deadline) {
		progress, err := o.progress(ctx, cfg)
		if err != nil {
			return err
		}
		if progress.done {
			return nil
		}
		waiting = progress.why
		cfg.Logger.Debug("waiting for operator", "package", o.pkg, "why", waiting)

		if progress.unsatisfiable {
			unsatisfiable++
		} else {
			unsatisfiable = 0
		}
		if unsatisfiable >= unsatisfiablePolls {
			return fmt.Errorf("%s cannot be installed from %s: %s", o.pkg, o.catalogImage, waiting)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
	return fmt.Errorf("%s not installed after %s: %s", o.pkg, timeout, waiting)
}

// olmProgress is one observation of an install.
type olmProgress struct {
	done bool
	// what the install is still waiting for
	why string
	// the catalog is up and the resolver says it cannot satisfy the
	// subscription, e.g. the CSV is not in the channel
	unsatisfiable bool
}

// progress observes the install once, approving the wanted InstallPlan when
// approval is manual. err is a failure that waiting cannot fix.
func (o olmOperator) progress(ctx context.Context, cfg *Config) (olmProgress, error) {
	sub, err := cfg.DynamicClient.Resource(subscriptionGVR).Namespace(o.namespace).Get(ctx, o.pkg, metav1.GetOptions{})
	if err != nil {
		return olmProgress{why: fmt.Sprintf("getting subscription: %v", err)}, nil
	}

	installed, _, _ := unstructured.NestedString(sub.Object, "status", "installedCSV")
	if installed != "" {
		if o.csv != "" && installed != o.csv {
			return olmProgress{}, fmt.Errorf("%s is installed, not the requested %s: oinc does not change an installed operator's version, recreate the cluster to switch", installed, o.csv)
		}
		csv, err := cfg.DynamicClient.Resource(csvGVR).Namespace(o.namespace).Get(ctx, installed, metav1.GetOptions{})
		if err != nil {
			return olmProgress{why: fmt.Sprintf("getting csv %s: %v", installed, err)}, nil
		}
		phase, _, _ := unstructured.NestedString(csv.Object, "status", "phase")
		if phase == "Succeeded" {
			cfg.Logger.Info("operator CSV ready", "csv", installed)
			return olmProgress{done: true}, nil
		}
		message, _, _ := unstructured.NestedString(csv.Object, "status", "message")
		return olmProgress{why: fmt.Sprintf("csv %s is %s: %s", installed, phase, message)}, nil
	}

	if o.manual {
		target := o.csv
		if target == "" {
			target, _, _ = unstructured.NestedString(sub.Object, "status", "currentCSV")
		}
		if target != "" {
			if err := o.approvePlan(ctx, cfg, target); err != nil {
				return olmProgress{}, err
			}
		}
	}

	if reason, message := subscriptionCondition(sub, "ResolutionFailed"); message != "" {
		return olmProgress{
			why:           "resolution failed: " + message,
			unsatisfiable: reason == "ConstraintsNotSatisfiable" && o.catalogReady(ctx, cfg),
		}, nil
	}
	state, _, _ := unstructured.NestedString(sub.Object, "status", "state")
	return olmProgress{why: fmt.Sprintf("subscription state %q", state)}, nil
}

func (o olmOperator) catalogReady(ctx context.Context, cfg *Config) bool {
	catalog, err := cfg.DynamicClient.Resource(catalogSourceGVR).Namespace(o.namespace).Get(ctx, o.catalog, metav1.GetOptions{})
	if err != nil {
		return false
	}
	state, _, _ := unstructured.NestedString(catalog.Object, "status", "connectionState", "lastObservedState")
	return state == "READY"
}

// approvePlan approves the InstallPlan that carries csv. plans without it,
// such as those for later versions, are not touched and stay pending.
func (o olmOperator) approvePlan(ctx context.Context, cfg *Config, csv string) error {
	plans := cfg.DynamicClient.Resource(installPlanGVR).Namespace(o.namespace)
	list, err := plans.List(ctx, metav1.ListOptions{})
	if err != nil {
		cfg.Logger.Debug("listing install plans", "err", err)
		return nil
	}
	for i := range list.Items {
		plan := &list.Items[i]
		names, _, _ := unstructured.NestedStringSlice(plan.Object, "spec", "clusterServiceVersionNames")
		if !slices.Contains(names, csv) {
			continue
		}
		if phase, _, _ := unstructured.NestedString(plan.Object, "status", "phase"); phase == "Failed" {
			message, _, _ := unstructured.NestedString(plan.Object, "status", "message")
			return fmt.Errorf("install plan %s for %s failed: %s", plan.GetName(), csv, message)
		}
		if approved, _, _ := unstructured.NestedBool(plan.Object, "spec", "approved"); approved {
			continue
		}
		patch := []byte(`{"spec":{"approved":true}}`)
		if _, err := plans.Patch(ctx, plan.GetName(), types.MergePatchType, patch, metav1.PatchOptions{}); err != nil {
			return fmt.Errorf("approving install plan %s: %w", plan.GetName(), err)
		}
		cfg.Logger.Info("approved install plan", "plan", plan.GetName(), "csvs", names)
	}
	return nil
}

// subscriptionCondition returns the reason and message of a condition that
// is True. the message falls back to the reason, so it is empty only when the
// condition is not True.
func subscriptionCondition(sub *unstructured.Unstructured, conditionType string) (reason, message string) {
	conditions, _, _ := unstructured.NestedSlice(sub.Object, "status", "conditions")
	for _, c := range conditions {
		cm, ok := c.(map[string]any)
		if !ok || cm["type"] != conditionType || cm["status"] != "True" {
			continue
		}
		reason, _ = cm["reason"].(string)
		message, _ = cm["message"].(string)
		if message == "" {
			message = reason
		}
		return reason, message
	}
	return "", ""
}
