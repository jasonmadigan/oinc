package addons

import (
	"context"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	kruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kscheme "k8s.io/client-go/kubernetes/scheme"
	k8stesting "k8s.io/client-go/testing"
)

func olmFakeClient(objs ...kruntime.Object) *dynamicfake.FakeDynamicClient {
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		kscheme.Scheme,
		map[schema.GroupVersionResource]string{
			installPlanGVR: "InstallPlanList",
		},
		objs...,
	)
}

func makeCSV(namespace, name, phase string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "operators.coreos.com/v1alpha1",
		"kind":       "ClusterServiceVersion",
		"metadata":   map[string]any{"name": name, "namespace": namespace},
		"status":     map[string]any{"phase": phase},
	}}
}

// olmInstalls makes the fake behave like OLM with automatic approval: a
// created Subscription is at once reported installed at csv.
func olmInstalls(client *dynamicfake.FakeDynamicClient, csv string) {
	client.PrependReactor("create", "subscriptions", func(action k8stesting.Action) (bool, kruntime.Object, error) {
		sub := action.(k8stesting.CreateAction).GetObject().(*unstructured.Unstructured)
		sub.Object["status"] = map[string]any{
			"state":        "AtLatestKnown",
			"currentCSV":   csv,
			"installedCSV": csv,
		}
		return false, nil, nil
	})
}

func getSpec(t *testing.T, client *dynamicfake.FakeDynamicClient, gvr schema.GroupVersionResource, namespace, name string) map[string]any {
	t.Helper()
	obj, err := client.Resource(gvr).Namespace(namespace).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("getting %s %s/%s: %v", gvr.Resource, namespace, name, err)
	}
	spec, _, _ := unstructured.NestedMap(obj.Object, "spec")
	return spec
}

func TestKuadrantLatestInstallsFromItsCatalogAutomatically(t *testing.T) {
	client := olmFakeClient(makeCSV("kuadrant-system", "kuadrant-operator.v0.0.0", "Succeeded"))
	olmInstalls(client, "kuadrant-operator.v0.0.0")
	cfg := &Config{DynamicClient: client, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}

	if err := (&kuadrant{version: "latest"}).Install(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}

	wantGroup := map[string]any{"upgradeStrategy": "Default"}
	if got := getSpec(t, client, operatorGroupGVR, "kuadrant-system", "kuadrant-system"); !reflect.DeepEqual(got, wantGroup) {
		t.Errorf("OperatorGroup spec = %v, want %v", got, wantGroup)
	}

	wantCatalog := map[string]any{
		"sourceType":  "grpc",
		"image":       "quay.io/kuadrant/kuadrant-operator-catalog:latest",
		"displayName": "Kuadrant Operators",
		"publisher":   "grpc",
		"updateStrategy": map[string]any{
			"registryPoll": map[string]any{"interval": "5m"},
		},
	}
	if got := getSpec(t, client, catalogSourceGVR, "kuadrant-system", "kuadrant-operator-catalog"); !reflect.DeepEqual(got, wantCatalog) {
		t.Errorf("CatalogSource spec = %v, want %v", got, wantCatalog)
	}

	wantSubscription := map[string]any{
		"channel":             "preview",
		"installPlanApproval": "Automatic",
		"name":                "kuadrant-operator",
		"source":              "kuadrant-operator-catalog",
		"sourceNamespace":     "kuadrant-system",
		"config": map[string]any{
			"env": []any{
				map[string]any{
					"name":  "ISTIO_GATEWAY_CONTROLLER_NAMES",
					"value": "openshift.io/gateway-controller/v1",
				},
			},
		},
	}
	if got := getSpec(t, client, subscriptionGVR, "kuadrant-system", "kuadrant-operator"); !reflect.DeepEqual(got, wantSubscription) {
		t.Errorf("Subscription spec = %v, want %v", got, wantSubscription)
	}
}

func quietConfig(client *dynamicfake.FakeDynamicClient) *Config {
	return &Config{DynamicClient: client, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func pinnedOperator() olmOperator {
	return olmOperator{
		namespace:          "kuadrant-system",
		catalog:            "redhat-operators",
		catalogImage:       "registry.redhat.io/redhat/redhat-operator-index:v4.22",
		catalogDisplayName: "Red Hat Operators",
		pkg:                "rhcl-operator",
		channel:            "stable-1.4",
		manual:             true,
		csv:                "rhcl-operator.v1.4.3",
	}
}

func makeInstallPlan(name string, approved bool, phase string, csvs ...string) *unstructured.Unstructured {
	names := make([]any, len(csvs))
	for i, csv := range csvs {
		names[i] = csv
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "operators.coreos.com/v1alpha1",
		"kind":       "InstallPlan",
		"metadata":   map[string]any{"name": name, "namespace": "kuadrant-system"},
		"spec": map[string]any{
			"approval":                   "Manual",
			"approved":                   approved,
			"clusterServiceVersionNames": names,
		},
		"status": map[string]any{"phase": phase},
	}}
}

func makeSubscription(name string, spec, status map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "operators.coreos.com/v1alpha1",
		"kind":       "Subscription",
		"metadata":   map[string]any{"name": name, "namespace": "kuadrant-system"},
		"spec":       spec,
		"status":     status,
	}}
}

// olmAwaitsApproval makes the fake behave like OLM with manual approval: a
// created Subscription resolves to currentCSV and waits, and approving the
// InstallPlan named plan installs installsCSV, after which upgradeCSV is the
// next pending version.
func olmAwaitsApproval(t *testing.T, client *dynamicfake.FakeDynamicClient, subscription, currentCSV, plan, installsCSV, upgradeCSV string) {
	t.Helper()
	client.PrependReactor("create", "subscriptions", func(action k8stesting.Action) (bool, kruntime.Object, error) {
		sub := action.(k8stesting.CreateAction).GetObject().(*unstructured.Unstructured)
		sub.Object["status"] = map[string]any{"state": "UpgradePending", "currentCSV": currentCSV}
		return false, nil, nil
	})
	client.PrependReactor("patch", "installplans", func(action k8stesting.Action) (bool, kruntime.Object, error) {
		if action.(k8stesting.PatchAction).GetName() != plan {
			return false, nil, nil
		}
		sub, err := client.Tracker().Get(subscriptionGVR, "kuadrant-system", subscription)
		if err != nil {
			t.Errorf("subscription missing when its plan was approved: %v", err)
			return false, nil, nil
		}
		updated := sub.(*unstructured.Unstructured).DeepCopy()
		updated.Object["status"] = map[string]any{"state": "UpgradePending", "currentCSV": upgradeCSV, "installedCSV": installsCSV}
		if err := client.Tracker().Update(subscriptionGVR, updated, "kuadrant-system"); err != nil {
			t.Error(err)
		}
		if err := client.Tracker().Add(makeCSV("kuadrant-system", installsCSV, "Succeeded")); err != nil {
			t.Error(err)
		}
		return false, nil, nil
	})
}

func planApproved(t *testing.T, client *dynamicfake.FakeDynamicClient, name string) bool {
	t.Helper()
	approved, _ := getSpec(t, client, installPlanGVR, "kuadrant-system", name)["approved"].(bool)
	return approved
}

func TestPinnedOperatorApprovesOnlyThePlanCarryingItsCSV(t *testing.T) {
	client := olmFakeClient(
		makeInstallPlan("install-wanted", false, "RequiresApproval",
			"rhcl-operator.v1.4.3", "authorino-operator.v1.4.3", "limitador-operator.v1.4.2", "dns-operator.v1.4.2"),
		makeInstallPlan("install-newer", false, "RequiresApproval",
			"rhcl-operator.v1.4.4", "authorino-operator.v1.4.4", "limitador-operator.v1.4.3", "dns-operator.v1.4.3"),
	)
	olmAwaitsApproval(t, client, "rhcl-operator", "rhcl-operator.v1.4.3", "install-wanted", "rhcl-operator.v1.4.3", "rhcl-operator.v1.4.4")

	if err := pinnedOperator().install(context.Background(), quietConfig(client), time.Second, time.Millisecond); err != nil {
		t.Fatal(err)
	}

	if !planApproved(t, client, "install-wanted") {
		t.Error("the plan carrying rhcl-operator.v1.4.3 was not approved")
	}
	if planApproved(t, client, "install-newer") {
		t.Error("the plan carrying rhcl-operator.v1.4.4 was approved; later versions must stay pending")
	}
}

func TestPinnedOperatorSubscribesManuallyAtItsCSV(t *testing.T) {
	client := olmFakeClient(makeInstallPlan("install-wanted", false, "RequiresApproval", "rhcl-operator.v1.4.3"))
	olmAwaitsApproval(t, client, "rhcl-operator", "rhcl-operator.v1.4.3", "install-wanted", "rhcl-operator.v1.4.3", "rhcl-operator.v1.4.4")

	if err := pinnedOperator().install(context.Background(), quietConfig(client), time.Second, time.Millisecond); err != nil {
		t.Fatal(err)
	}

	wantSubscription := map[string]any{
		"channel":             "stable-1.4",
		"installPlanApproval": "Manual",
		"name":                "rhcl-operator",
		"source":              "redhat-operators",
		"sourceNamespace":     "kuadrant-system",
		"startingCSV":         "rhcl-operator.v1.4.3",
	}
	if got := getSpec(t, client, subscriptionGVR, "kuadrant-system", "rhcl-operator"); !reflect.DeepEqual(got, wantSubscription) {
		t.Errorf("Subscription spec = %v, want %v", got, wantSubscription)
	}

	// a pinned install must not follow the index tag as it moves
	wantCatalog := map[string]any{
		"sourceType":  "grpc",
		"image":       "registry.redhat.io/redhat/redhat-operator-index:v4.22",
		"displayName": "Red Hat Operators",
		"publisher":   "grpc",
	}
	if got := getSpec(t, client, catalogSourceGVR, "kuadrant-system", "redhat-operators"); !reflect.DeepEqual(got, wantCatalog) {
		t.Errorf("CatalogSource spec = %v, want %v", got, wantCatalog)
	}
}

func TestUnpinnedManualOperatorApprovesTheChannelHeadOnce(t *testing.T) {
	op := pinnedOperator()
	op.channel = "stable"
	op.csv = ""
	client := olmFakeClient(
		makeInstallPlan("install-head", false, "RequiresApproval", "rhcl-operator.v1.4.3"),
		makeInstallPlan("install-unrelated", false, "RequiresApproval", "rhcl-operator.v1.4.2"),
	)
	olmAwaitsApproval(t, client, "rhcl-operator", "rhcl-operator.v1.4.3", "install-head", "rhcl-operator.v1.4.3", "rhcl-operator.v1.4.4")

	if err := op.install(context.Background(), quietConfig(client), time.Second, time.Millisecond); err != nil {
		t.Fatal(err)
	}

	if !planApproved(t, client, "install-head") {
		t.Error("the plan for the channel head was not approved")
	}
	if planApproved(t, client, "install-unrelated") {
		t.Error("a plan for a CSV the subscription did not resolve to was approved")
	}
	if spec := getSpec(t, client, subscriptionGVR, "kuadrant-system", "rhcl-operator"); spec["startingCSV"] != nil {
		t.Errorf("startingCSV = %v, want none when following the channel head", spec["startingCSV"])
	}
}

func TestPinnedOperatorRefusesToReplaceAnotherInstalledVersion(t *testing.T) {
	client := olmFakeClient(
		makeSubscription("rhcl-operator",
			map[string]any{"channel": "stable-1.4", "startingCSV": "rhcl-operator.v1.4.3"},
			map[string]any{"state": "AtLatestKnown", "currentCSV": "rhcl-operator.v1.4.4", "installedCSV": "rhcl-operator.v1.4.4"}),
		makeCSV("kuadrant-system", "rhcl-operator.v1.4.4", "Succeeded"),
		makeInstallPlan("install-wanted", false, "RequiresApproval", "rhcl-operator.v1.4.3"),
	)

	err := pinnedOperator().install(context.Background(), quietConfig(client), time.Second, time.Millisecond)
	if err == nil {
		t.Fatal("want an error when a different version is already installed")
	}
	for _, want := range []string{"rhcl-operator.v1.4.4", "rhcl-operator.v1.4.3"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %s", err, want)
		}
	}
	if planApproved(t, client, "install-wanted") {
		t.Error("a plan was approved over a different installed version")
	}
}

func TestPinnedOperatorFailsWhenItsPlanFails(t *testing.T) {
	plan := makeInstallPlan("install-wanted", true, "Failed", "rhcl-operator.v1.4.3")
	plan.Object["status"].(map[string]any)["message"] = "bundle unpacking failed. Reason: DeadlineExceeded"
	client := olmFakeClient(plan)
	client.PrependReactor("create", "subscriptions", func(action k8stesting.Action) (bool, kruntime.Object, error) {
		sub := action.(k8stesting.CreateAction).GetObject().(*unstructured.Unstructured)
		sub.Object["status"] = map[string]any{"state": "UpgradePending", "currentCSV": "rhcl-operator.v1.4.3"}
		return false, nil, nil
	})

	start := time.Now()
	err := pinnedOperator().install(context.Background(), quietConfig(client), time.Minute, time.Millisecond)
	if err == nil {
		t.Fatal("want an error when the install plan failed")
	}
	if !strings.Contains(err.Error(), "bundle unpacking failed") {
		t.Errorf("error %q does not carry the plan's failure message", err)
	}
	if time.Since(start) > 10*time.Second {
		t.Error("a failed plan must fail the install at once, not at the timeout")
	}
}

func TestOperatorInstallTimeoutReportsWhyResolutionFailed(t *testing.T) {
	client := olmFakeClient()
	client.PrependReactor("create", "subscriptions", func(action k8stesting.Action) (bool, kruntime.Object, error) {
		sub := action.(k8stesting.CreateAction).GetObject().(*unstructured.Unstructured)
		sub.Object["status"] = map[string]any{
			"conditions": []any{
				map[string]any{"type": "CatalogSourcesUnhealthy", "status": "False", "reason": "AllCatalogSourcesHealthy"},
				map[string]any{
					"type":    "ResolutionFailed",
					"status":  "True",
					"reason":  "ErrorPreventedResolution",
					"message": "failed to populate resolver cache from source redhat-operators/kuadrant-system: failed to list bundles",
				},
			},
		}
		return false, nil, nil
	})

	err := pinnedOperator().install(context.Background(), quietConfig(client), 50*time.Millisecond, time.Millisecond)
	if err == nil {
		t.Fatal("want a timeout error")
	}
	if !strings.Contains(err.Error(), "failed to populate resolver cache") {
		t.Errorf("error %q does not say why resolution failed", err)
	}
}

// olmCannotResolve makes the fake behave like OLM facing a subscription its
// catalog cannot satisfy: the catalog reports catalogState and the
// subscription carries the resolver's verdict.
func olmCannotResolve(client *dynamicfake.FakeDynamicClient, catalogState, reason, message string) {
	client.PrependReactor("create", "catalogsources", func(action k8stesting.Action) (bool, kruntime.Object, error) {
		catalog := action.(k8stesting.CreateAction).GetObject().(*unstructured.Unstructured)
		catalog.Object["status"] = map[string]any{"connectionState": map[string]any{"lastObservedState": catalogState}}
		return false, nil, nil
	})
	client.PrependReactor("create", "subscriptions", func(action k8stesting.Action) (bool, kruntime.Object, error) {
		sub := action.(k8stesting.CreateAction).GetObject().(*unstructured.Unstructured)
		sub.Object["status"] = map[string]any{
			"conditions": []any{
				map[string]any{"type": "ResolutionFailed", "status": "True", "reason": reason, "message": message},
			},
		}
		return false, nil, nil
	})
}

// as reported by OLM on MicroShift 4.23 for rhcl-operator.v1.4.2, which the
// v4.22 index only carries on the stable channel
const notInChannel = "constraints not satisfiable: no operators found with name rhcl-operator.v1.4.2 in channel stable-1.4 of package rhcl-operator in the catalog referenced by subscription rhcl-operator, subscription rhcl-operator exists"

func TestOperatorInstallFailsOnceAReadyCatalogCannotSatisfyIt(t *testing.T) {
	client := olmFakeClient()
	olmCannotResolve(client, "READY", "ConstraintsNotSatisfiable", notInChannel)

	start := time.Now()
	err := pinnedOperator().install(context.Background(), quietConfig(client), time.Minute, time.Millisecond)
	if err == nil {
		t.Fatal("want an error when the catalog cannot satisfy the subscription")
	}
	if !strings.Contains(err.Error(), "no operators found with name rhcl-operator.v1.4.2 in channel stable-1.4") {
		t.Errorf("error %q does not carry the resolver's reason", err)
	}
	if !strings.Contains(err.Error(), "registry.redhat.io/redhat/redhat-operator-index:v4.22") {
		t.Errorf("error %q does not name the index that lacks it", err)
	}
	if time.Since(start) > 10*time.Second {
		t.Error("an unsatisfiable subscription must fail the install early, not at the timeout")
	}
}

// while the catalog pod is still starting the resolver reports failures that
// clear by themselves; giving up on those would break every install.
func TestOperatorInstallKeepsWaitingWhileTheCatalogStarts(t *testing.T) {
	for _, tt := range []struct {
		name, catalogState, reason, message string
	}{
		{
			name:         "catalog still connecting",
			catalogState: "CONNECTING",
			reason:       "ConstraintsNotSatisfiable",
			message:      "constraints not satisfiable: no operators found from catalog redhat-operators in namespace kuadrant-system referenced by subscription rhcl-operator, subscription rhcl-operator exists",
		},
		{
			name:         "resolver could not reach the catalog",
			catalogState: "READY",
			reason:       "ErrorPreventedResolution",
			message:      "error using catalogsource kuadrant-system/redhat-operators: failed to list bundles: rpc error: code = Unavailable",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			client := olmFakeClient()
			olmCannotResolve(client, tt.catalogState, tt.reason, tt.message)

			err := pinnedOperator().install(context.Background(), quietConfig(client), 100*time.Millisecond, time.Millisecond)
			if err == nil || !strings.Contains(err.Error(), "not installed after") {
				t.Errorf("err = %v, want it to wait out the timeout", err)
			}
		})
	}
}
