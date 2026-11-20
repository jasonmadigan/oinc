package oinc

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/jasonmadigan/oinc/pkg/addons"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

func monitoringManifestPath(branch string) string {
	return "openshift/cluster-monitoring-operator/" + branch + "/manifests/"
}

// serveMonitoringCRDs publishes every monitoring CRD manifest on a release
// branch, each carrying marker.
func serveMonitoringCRDs(t *testing.T, root, branch, marker string) {
	t.Helper()
	for _, crd := range monitoringCRDs {
		serve(t, root, monitoringManifestPath(branch)+crd.file, crdManifestYAML(crd.name, marker))
	}
}

func monitoringCRDObjects(marker string) []runtime.Object {
	var objects []runtime.Object
	for _, crd := range monitoringCRDs {
		objects = append(objects, crdObject(crd.name, marker, true))
	}
	return objects
}

// kuadrant-operator cannot start without these two
func TestMonitoringCRDsIncludeWhatKuadrantNeeds(t *testing.T) {
	for _, want := range []string{"servicemonitors.monitoring.coreos.com", "podmonitors.monitoring.coreos.com"} {
		if !slices.ContainsFunc(monitoringCRDs, func(c crdManifest) bool { return c.name == want }) {
			t.Errorf("monitoring CRDs lack %s", want)
		}
	}
	for _, crd := range monitoringCRDs {
		if !strings.HasSuffix(crd.name, ".monitoring.coreos.com") {
			t.Errorf("%s is not a monitoring.coreos.com CRD", crd.name)
		}
	}
}

func TestApplyMonitoringCRDsInstallsTheReleaseManifests(t *testing.T) {
	root, fetched := fakeRawGitHub(t)
	serveMonitoringCRDs(t, root, "release-4.22", "release")
	// the release's copy replaces one the cluster already has
	client := fakeCRDClient(crdObject("servicemonitors.monitoring.coreos.com", "stale", true))

	if err := applyMonitoringCRDs(context.Background(), client, "release-4.22", monitoringCRDs, discardLogger()); err != nil {
		t.Fatalf("applyMonitoringCRDs: %v", err)
	}

	urls := fetched()
	if len(urls) != len(monitoringCRDs) {
		t.Fatalf("fetched %d manifests, want %d: %v", len(urls), len(monitoringCRDs), urls)
	}
	for _, url := range urls {
		if !strings.HasPrefix(url, rawGitHub+monitoringManifestPath("release-4.22")) {
			t.Errorf("fetched %s, want only the release branch's manifests", url)
		}
	}
	for _, crd := range monitoringCRDs {
		if got := crdMarker(t, client, crd.name); got != "release" {
			t.Errorf("%s marker = %q, want the release manifest", crd.name, got)
		}
	}
}

// what release manifests ship for AlertmanagerConfig: two versions converted
// by a webhook in openshift-monitoring
const convertedCRD = `apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: alertmanagerconfigs.monitoring.coreos.com
  annotations:
    service.beta.openshift.io/inject-cabundle: "true"
    test.oinc/manifest: release
spec:
  conversion:
    strategy: Webhook
    webhook:
      clientConfig:
        service:
          name: prometheus-operator-admission-webhook
          namespace: openshift-monitoring
          path: /convert
          port: 8443
      conversionReviewVersions: [v1beta1, v1alpha1]
  group: monitoring.coreos.com
  versions:
  - name: v1alpha1
    served: true
    storage: true
  - name: v1beta1
    served: true
    storage: false
status:
  conditions:
  - type: Established
    status: "True"
`

func TestApplyMonitoringCRDsServeConvertedCRDsAtTheirStorageVersion(t *testing.T) {
	root, _ := fakeRawGitHub(t)
	serveMonitoringCRDs(t, root, "release-4.22", "release")
	serve(t, root, monitoringManifestPath("release-4.22")+monitoringCRDs[0].file, convertedCRD)
	client := fakeCRDClient()

	if err := applyMonitoringCRDs(context.Background(), client, "release-4.22", monitoringCRDs, discardLogger()); err != nil {
		t.Fatalf("applyMonitoringCRDs: %v", err)
	}

	crd, err := client.Resource(crdGVR).Get(context.Background(), "alertmanagerconfigs.monitoring.coreos.com", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, found, _ := unstructured.NestedFieldNoCopy(crd.Object, "spec", "conversion"); found {
		t.Error("conversion webhook kept, with no service to call")
	}
	if _, found := crd.GetAnnotations()["service.beta.openshift.io/inject-cabundle"]; found {
		t.Error("CA injection annotation kept for a webhook that is gone")
	}
	versions, _, _ := unstructured.NestedSlice(crd.Object, "spec", "versions")
	if len(versions) != 1 || versions[0].(map[string]any)["name"] != "v1alpha1" {
		t.Errorf("versions = %v, want the storage version v1alpha1 alone", versions)
	}
}

func TestServedAtStorageVersion(t *testing.T) {
	plain, err := decodeCRD([]byte(crdManifestYAML("servicemonitors.monitoring.coreos.com", "x")), "servicemonitors.monitoring.coreos.com")
	if err != nil {
		t.Fatal(err)
	}
	before := plain.DeepCopy()
	if err := servedAtStorageVersion(plain); err != nil {
		t.Fatalf("CRD without conversion: %v", err)
	}
	if !equality.Semantic.DeepEqual(before, plain) {
		t.Error("changed a CRD that converts nothing")
	}

	// a stored version that is not served cannot be the one left
	unserved := strings.Replace(convertedCRD, "  - name: v1alpha1\n    served: true", "  - name: v1alpha1\n    served: false", 1)
	crd, err := decodeCRD([]byte(unserved), "alertmanagerconfigs.monitoring.coreos.com")
	if err != nil {
		t.Fatal(err)
	}
	if err := servedAtStorageVersion(crd); err == nil {
		t.Error("kept an unserved storage version, want an error")
	}
}

func TestApplyMonitoringCRDsFailsWithoutTheReleaseManifests(t *testing.T) {
	root, _ := fakeRawGitHub(t)
	serveMonitoringCRDs(t, root, "release-4.22", "release")
	noDownloadRetries(t)

	err := applyMonitoringCRDs(context.Background(), fakeCRDClient(), "release-9.9", monitoringCRDs, discardLogger())
	if err == nil || !strings.Contains(err.Error(), "release-9.9") {
		t.Fatalf("err = %v, want the missing branch's manifest named", err)
	}
}

func TestEnsureMonitoringCRDsAddsOnlyWhatIsMissing(t *testing.T) {
	root, fetched := fakeRawGitHub(t)
	serveMonitoringCRDs(t, root, "release-4.22", "release")
	// installed by hand before oinc served them: left alone
	client := fakeCRDClient(crdObject("servicemonitors.monitoring.coreos.com", "user", true))
	cfg := &addons.Config{DynamicClient: client, Logger: discardLogger(), OpenShiftAPIBranch: "release-4.22"}

	if err := ensureMonitoringCRDs(context.Background(), cfg); err != nil {
		t.Fatalf("ensureMonitoringCRDs: %v", err)
	}

	if got := crdMarker(t, client, "servicemonitors.monitoring.coreos.com"); got != "user" {
		t.Errorf("existing ServiceMonitor CRD marker = %q, want it untouched", got)
	}
	for _, crd := range monitoringCRDs {
		if crd.name == "servicemonitors.monitoring.coreos.com" {
			continue
		}
		if got := crdMarker(t, client, crd.name); got != "release" {
			t.Errorf("%s marker = %q, want the release manifest", crd.name, got)
		}
	}
	for _, url := range fetched() {
		if strings.Contains(url, "servicemonitor") {
			t.Errorf("fetched %s for a CRD the cluster has", url)
		}
	}
}

func TestEnsureMonitoringCRDsOnlyReadsWhenAllAreServed(t *testing.T) {
	_, fetched := fakeRawGitHub(t)
	client := fakeCRDClient(monitoringCRDObjects("present")...)
	cfg := &addons.Config{DynamicClient: client, Logger: discardLogger(), OpenShiftAPIBranch: "release-4.22"}

	if err := ensureMonitoringCRDs(context.Background(), cfg); err != nil {
		t.Fatalf("ensureMonitoringCRDs: %v", err)
	}
	if urls := fetched(); len(urls) != 0 {
		t.Errorf("fetched %v, want nothing", urls)
	}
	for _, a := range client.Actions() {
		if a.GetVerb() != "get" {
			t.Errorf("cluster action %s, want reads only", a.GetVerb())
		}
	}
}

// with no release there are no manifests to pick, and the install goes on
// as it did before oinc served them
func TestEnsureMonitoringCRDsSkipsAClusterOfUnknownRelease(t *testing.T) {
	_, fetched := fakeRawGitHub(t)
	client := fakeCRDClient()
	cfg := &addons.Config{DynamicClient: client, Logger: discardLogger()}

	if err := ensureMonitoringCRDs(context.Background(), cfg); err != nil {
		t.Fatalf("ensureMonitoringCRDs: %v", err)
	}
	if urls := fetched(); len(urls) != 0 {
		t.Errorf("fetched %v, want nothing", urls)
	}
	for _, a := range client.Actions() {
		if a.GetVerb() != "get" {
			t.Errorf("cluster action %s, want reads only", a.GetVerb())
		}
	}
}

// create must serve the CRDs before any addon's operator starts, however the
// addon installs it
func TestCreateStepsInstallMonitoringCRDsBeforeAddons(t *testing.T) {
	_, steps := CreateSteps(context.Background(), CreateOpts{Addons: "cert-manager"})
	index := map[string]int{}
	for i, s := range steps {
		index[s.Name] = i
	}
	crds, ok := index["installing monitoring CRDs"]
	if !ok {
		t.Fatalf("no monitoring CRD step in %v", index)
	}
	if crds < index["waiting for pods"] {
		t.Errorf("monitoring CRDs at step %d, before the cluster is ready at %d", crds, index["waiting for pods"])
	}
	if crds > index["installing addons"] {
		t.Errorf("monitoring CRDs at step %d, after addons at %d", crds, index["installing addons"])
	}
}

// monitoredOperator records whether it was installed, and whether the
// cluster served the ServiceMonitor CRD at the time
type monitoredOperator struct {
	installed, served bool
}

func (o *monitoredOperator) Name() string           { return "kuadrant" }
func (o *monitoredOperator) Dependencies() []string { return nil }
func (o *monitoredOperator) Install(ctx context.Context, cfg *addons.Config) error {
	o.installed = true
	_, err := cfg.DynamicClient.Resource(crdGVR).Get(ctx, "servicemonitors.monitoring.coreos.com", metav1.GetOptions{})
	o.served = err == nil
	return nil
}
func (o *monitoredOperator) Ready(context.Context, *addons.Config) error { return nil }

// a cluster created before oinc served them gets them ahead of the addons
func TestInstallAddonsAddsMissingMonitoringCRDsFirst(t *testing.T) {
	root, _ := fakeRawGitHub(t)
	serveMonitoringCRDs(t, root, "release-4.22", "release")
	op := &monitoredOperator{}
	cfg := &addons.Config{DynamicClient: fakeCRDClient(), Logger: discardLogger(), OpenShiftAPIBranch: "release-4.22"}

	if err := installAddons(context.Background(), []addons.Addon{op}, cfg); err != nil {
		t.Fatalf("installAddons: %v", err)
	}
	if !op.installed || !op.served {
		t.Errorf("installed = %t, served = %t; want the addon installed once the CRD was served", op.installed, op.served)
	}
}

func TestInstallAddonsStopsWhenMonitoringCRDsCannotBeAdded(t *testing.T) {
	fakeRawGitHub(t)
	noDownloadRetries(t)
	op := &monitoredOperator{}
	cfg := &addons.Config{DynamicClient: fakeCRDClient(), Logger: discardLogger(), OpenShiftAPIBranch: "release-4.22"}

	err := installAddons(context.Background(), []addons.Addon{op}, cfg)
	if err == nil || !strings.Contains(err.Error(), "monitoring CRDs") {
		t.Fatalf("err = %v, want the monitoring CRD failure", err)
	}
	if op.installed {
		t.Error("addon installed after the CRDs could not be added")
	}
}
