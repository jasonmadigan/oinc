package addons

import (
	"context"
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

func consolePluginFakeClient(objs ...kruntime.Object) *dynamicfake.FakeDynamicClient {
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		kscheme.Scheme,
		map[schema.GroupVersionResource]string{
			podGVR:         "PodList",
			installPlanGVR: "InstallPlanList",
		},
		objs...,
	)
}

func makeConsolePlugin(name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "console.openshift.io/v1",
		"kind":       "ConsolePlugin",
		"metadata":   map[string]any{"name": name},
	}}
}

func makeAvailableDeployment(namespace, name string) *unstructured.Unstructured {
	dep := makeDeployment(namespace, name)
	dep.Object["status"] = map[string]any{"availableReplicas": int64(1)}
	return dep
}

// consoleRHCL is an rhcl addon that records the manifests it applies instead
// of running kubectl.
func consoleRHCL() (addon *rhcl, applied *[]string) {
	var urls []string
	addon = newRHCL()
	addon.applyURL = func(_ context.Context, _ *Config, url string) error {
		urls = append(urls, url)
		return nil
	}
	return addon, &urls
}

func loadedPlugins(cfg *Config) *[]string {
	var names []string
	cfg.LoadConsolePlugin = func(_ context.Context, name string) error {
		names = append(names, name)
		return nil
	}
	return &names
}

// the operator deploys its console plugin only where a ClusterVersion named
// version reports a parsable version, and picks the plugin build from it.
func TestRHCLGivesTheClusterAClusterVersion(t *testing.T) {
	client := consolePluginFakeClient()
	cfg := quietConfig(client)
	cfg.OpenShiftVersion = "4.22"
	cfg.OpenShiftAPIBranch = "release-4.22"
	addon, applied := consoleRHCL()

	if err := addon.ensureClusterVersion(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}

	wantURLs := []string{
		"https://raw.githubusercontent.com/openshift/api/release-4.22/config/v1/zz_generated.crd-manifests/0000_00_cluster-version-operator_01_clusterversions-Default.crd.yaml",
		"https://raw.githubusercontent.com/openshift/api/release-4.22/config/v1/zz_generated.crd-manifests/0000_00_cluster-version-operator_01_clusteroperators.crd.yaml",
	}
	if !reflect.DeepEqual(*applied, wantURLs) {
		t.Errorf("applied %v\nwant    %v", *applied, wantURLs)
	}

	version, err := client.Resource(clusterVersionGVR).Get(context.Background(), "version", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("no ClusterVersion named version: %v", err)
	}
	desired, _, _ := unstructured.NestedString(version.Object, "status", "desired", "version")
	if desired != "4.22.0" {
		t.Errorf("status.desired.version = %q, want 4.22.0", desired)
	}

	// a second install must leave it be
	if err := addon.ensureClusterVersion(context.Background(), cfg); err != nil {
		t.Fatalf("second ensureClusterVersion: %v", err)
	}
}

func TestRHCLClusterVersionNeedsAKnownOpenShiftVersion(t *testing.T) {
	addon, applied := consoleRHCL()

	err := addon.ensureClusterVersion(context.Background(), quietConfig(consolePluginFakeClient()))
	if err == nil || !strings.Contains(err.Error(), "OpenShift version") {
		t.Errorf("err = %v, want it to say the cluster's OpenShift version is unknown", err)
	}
	if len(*applied) != 0 {
		t.Errorf("applied %v without knowing the API branch", *applied)
	}
}

func TestRHCLLoadsThePluginTheOperatorDeployed(t *testing.T) {
	client := consolePluginFakeClient(
		makeConsolePlugin("kuadrant-console-plugin"),
		makeAvailableDeployment("kuadrant-system", "kuadrant-console-plugin"),
		makeOperatorDeployment(),
		makeOperatorPod("kuadrant-operator-current", "current-1", true, true),
	)
	cfg := quietConfig(client)
	loaded := loadedPlugins(cfg)
	addon, _ := consoleRHCL()

	if err := addon.loadConsolePlugin(context.Background(), cfg, time.Second, time.Hour, time.Millisecond); err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(*loaded, []string{"kuadrant-console-plugin"}) {
		t.Errorf("Console asked to load %v, want kuadrant-console-plugin", *loaded)
	}
	if n := len(podDeletes(client)); n != 0 {
		t.Errorf("operator restarted %d times, want none when it already deployed the plugin", n)
	}
}

// an operator that started before the ClusterVersion existed never looks for
// it again; only a restart makes it deploy the plugin.
func TestRHCLRestartsAnOperatorThatBootedWithoutAClusterVersion(t *testing.T) {
	client := consolePluginFakeClient(
		makeAvailableDeployment("kuadrant-system", "kuadrant-console-plugin"),
		makeOperatorDeployment(),
		makeOperatorPod("kuadrant-operator-old", "old-1", true, true),
	)
	restarted := false
	client.PrependReactor("delete", "pods", func(k8stesting.Action) (bool, kruntime.Object, error) {
		restarted = true
		return false, nil, nil
	})
	client.PrependReactor("list", "pods", func(k8stesting.Action) (bool, kruntime.Object, error) {
		if restarted {
			return true, podList(makeOperatorPod("kuadrant-operator-new", "new-1", true, true)), nil
		}
		return true, podList(makeOperatorPod("kuadrant-operator-old", "old-1", true, true)), nil
	})
	client.PrependReactor("get", "consoleplugins", func(k8stesting.Action) (bool, kruntime.Object, error) {
		if restarted {
			return true, makeConsolePlugin("kuadrant-console-plugin"), nil
		}
		return false, nil, nil
	})
	cfg := quietConfig(client)
	loaded := loadedPlugins(cfg)
	addon, _ := consoleRHCL()

	if err := addon.loadConsolePlugin(context.Background(), cfg, 5*time.Second, 20*time.Millisecond, time.Millisecond); err != nil {
		t.Fatal(err)
	}

	if n := len(podDeletes(client)); n != 1 {
		t.Errorf("operator restarted %d times, want exactly once", n)
	}
	if !reflect.DeepEqual(*loaded, []string{"kuadrant-console-plugin"}) {
		t.Errorf("Console asked to load %v, want kuadrant-console-plugin", *loaded)
	}
}

func TestRHCLReportsAPluginTheOperatorNeverDeploys(t *testing.T) {
	client := consolePluginFakeClient(
		makeOperatorDeployment(),
		makeOperatorPod("kuadrant-operator-current", "current-1", true, true),
	)
	cfg := quietConfig(client)
	loaded := loadedPlugins(cfg)
	addon, _ := consoleRHCL()

	err := addon.loadConsolePlugin(context.Background(), cfg, 80*time.Millisecond, 20*time.Millisecond, time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "ConsolePlugin") {
		t.Errorf("err = %v, want it to say the operator created no ConsolePlugin", err)
	}
	if len(*loaded) != 0 {
		t.Errorf("Console asked to load %v for a plugin that was never deployed", *loaded)
	}
}

func TestRHCLConsolePluginNeedsAConsole(t *testing.T) {
	cfg := quietConfig(consolePluginFakeClient(makeConsolePlugin("kuadrant-console-plugin")))
	addon, _ := consoleRHCL()

	err := addon.loadConsolePlugin(context.Background(), cfg, time.Second, time.Hour, time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "Console") {
		t.Errorf("err = %v, want it to say there is no Console", err)
	}
}

// the ClusterVersion has to exist before the operator first starts, so the
// install provides it ahead of the subscription.
func TestRHCLInstallProvidesTheClusterVersionOnlyForTheConsolePlugin(t *testing.T) {
	storePullSecret(t, "registry.redhat.io")

	for _, tt := range []struct {
		name        string
		opts        map[string]string
		wantApplied int
	}{
		{"with the console plugin", map[string]string{"version": "1.4.3", "console-plugin": "true"}, 2},
		{"without it", map[string]string{"version": "1.4.3"}, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			client := consolePluginFakeClient(makeCSV("kuadrant-system", "rhcl-operator.v1.4.3", "Succeeded"))
			olmInstalls(client, "rhcl-operator.v1.4.3")
			cfg := quietConfig(client)
			cfg.OpenShiftVersion = "4.23"
			cfg.OpenShiftAPIBranch = "release-4.23"

			addon, applied := consoleRHCL()
			catalog := fixtureRHCLCatalog(t)
			addon.fetchCatalog = func(context.Context, *slog.Logger) (rhclCatalog, error) { return catalog, nil }
			addon.SetOptions(tt.opts)

			if err := addon.Install(context.Background(), cfg); err != nil {
				t.Fatal(err)
			}

			if len(*applied) != tt.wantApplied {
				t.Errorf("applied %d OpenShift manifests, want %d: %v", len(*applied), tt.wantApplied, *applied)
			}
			_, err := client.Resource(clusterVersionGVR).Get(context.Background(), "version", metav1.GetOptions{})
			if exists := err == nil; exists != (tt.wantApplied > 0) {
				t.Errorf("ClusterVersion exists = %v, want %v", exists, tt.wantApplied > 0)
			}
		})
	}
}
