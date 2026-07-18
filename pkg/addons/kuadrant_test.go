package addons

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	kruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kscheme "k8s.io/client-go/kubernetes/scheme"
	k8stesting "k8s.io/client-go/testing"
)

func TestAdmissionProbeConfigMap(t *testing.T) {
	cm := admissionProbeConfigMap()

	if cm.GetNamespace() != "kuadrant-system" {
		t.Errorf("namespace = %q, want kuadrant-system", cm.GetNamespace())
	}
	if cm.GetName() == "" {
		t.Error("name must not be empty")
	}
	if other := admissionProbeConfigMap(); other.GetName() == cm.GetName() {
		t.Errorf("names must be random per attempt, got %q twice", cm.GetName())
	}

	refs, found, err := unstructured.NestedSlice(cm.Object, "metadata", "ownerReferences")
	if err != nil || !found || len(refs) != 1 {
		t.Fatalf("ownerReferences = %v (found=%v, err=%v), want exactly one", refs, found, err)
	}
	ref, ok := refs[0].(map[string]any)
	if !ok {
		t.Fatalf("ownerReference has unexpected type %T", refs[0])
	}

	want := map[string]any{
		"apiVersion":         "kuadrant.io/v1beta1",
		"kind":               "Kuadrant",
		"controller":         true,
		"blockOwnerDeletion": true,
	}
	for k, v := range want {
		if ref[k] != v {
			t.Errorf("ownerReference[%q] = %v, want %v", k, ref[k], v)
		}
	}
	for _, k := range []string{"name", "uid"} {
		if s, _ := ref[k].(string); s == "" {
			t.Errorf("ownerReference[%q] must not be empty", k)
		}
	}
}

func TestPollAdmissionProbe(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	t.Run("returns true once a create is admitted", func(t *testing.T) {
		client := dynamicfake.NewSimpleDynamicClient(kscheme.Scheme)
		if !pollAdmissionProbe(context.Background(), logger, client, time.Millisecond, time.Second) {
			t.Fatal("want true when create succeeds")
		}
	})

	t.Run("retries through errors until admitted", func(t *testing.T) {
		client := dynamicfake.NewSimpleDynamicClient(kscheme.Scheme)
		failures := 2
		client.PrependReactor("create", "configmaps", func(_ k8stesting.Action) (bool, kruntime.Object, error) {
			if failures > 0 {
				failures--
				return true, nil, fmt.Errorf("cannot find RESTMapping for APIVersion kuadrant.io/v1beta1 Kind Kuadrant")
			}
			return false, nil, nil
		})
		if !pollAdmissionProbe(context.Background(), logger, client, time.Millisecond, time.Second) {
			t.Fatal("want true after transient errors clear")
		}
		if failures != 0 {
			t.Errorf("want all injected failures consumed, %d left", failures)
		}
	})

	t.Run("returns false on timeout instead of erroring", func(t *testing.T) {
		client := dynamicfake.NewSimpleDynamicClient(kscheme.Scheme)
		client.PrependReactor("create", "configmaps", func(_ k8stesting.Action) (bool, kruntime.Object, error) {
			return true, nil, fmt.Errorf("cannot find RESTMapping")
		})
		if pollAdmissionProbe(context.Background(), logger, client, time.Millisecond, 20*time.Millisecond) {
			t.Fatal("want false on timeout")
		}
	})

	t.Run("returns false when context is cancelled", func(t *testing.T) {
		client := dynamicfake.NewSimpleDynamicClient(kscheme.Scheme)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		client.PrependReactor("create", "configmaps", func(_ k8stesting.Action) (bool, kruntime.Object, error) {
			return true, nil, fmt.Errorf("cannot find RESTMapping")
		})
		if pollAdmissionProbe(ctx, logger, client, time.Millisecond, time.Second) {
			t.Fatal("want false on cancelled context")
		}
	})
}

func makeKuadrant(conditions []any) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "kuadrant.io/v1beta1",
		"kind":       "Kuadrant",
		"metadata":   map[string]any{"name": "kuadrant", "namespace": "kuadrant-system"},
	}}
	if conditions != nil {
		obj.Object["status"] = map[string]any{"conditions": conditions}
	}
	return obj
}

var readyTrue = []any{map[string]any{"type": "Ready", "status": "True"}}

func TestKuadrantReadyState(t *testing.T) {
	tests := []struct {
		name       string
		conditions []any
		wantReady  bool
		wantWhy    string
	}{
		{"empty status", nil, false, "status empty"},
		{"ready", readyTrue, true, ""},
		{
			"missing dependency",
			[]any{map[string]any{
				"type": "Ready", "status": "False",
				"reason":  "MissingDependency",
				"message": "please restart Kuadrant Operator pod once dependency has been installed",
			}},
			false,
			"MissingDependency",
		},
		{
			"no ready condition",
			[]any{map[string]any{"type": "Other", "status": "True"}},
			false,
			"no Ready condition",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ready, why := kuadrantReadyState(makeKuadrant(tt.conditions))
			if ready != tt.wantReady {
				t.Errorf("ready = %v, want %v", ready, tt.wantReady)
			}
			if !strings.Contains(why, tt.wantWhy) {
				t.Errorf("why = %q, want it to contain %q", why, tt.wantWhy)
			}
		})
	}
}

func makeOperatorDeployment() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"metadata":   map[string]any{"name": "kuadrant-operator-controller-manager", "namespace": "kuadrant-system"},
		"spec":       map[string]any{"selector": map[string]any{"matchLabels": map[string]any{"app": "kuadrant-operator"}}},
		"status":     map[string]any{"availableReplicas": int64(1)},
	}}
}

func makeOperatorPod(name, uid string, running, ready bool) *unstructured.Unstructured {
	phase := "Pending"
	if running {
		phase = "Running"
	}
	readyStatus := "False"
	if ready {
		readyStatus = "True"
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Pod",
		"metadata": map[string]any{
			"name":      name,
			"namespace": "kuadrant-system",
			"uid":       uid,
			"labels":    map[string]any{"app": "kuadrant-operator"},
		},
		"status": map[string]any{
			"phase":      phase,
			"conditions": []any{map[string]any{"type": "Ready", "status": readyStatus}},
		},
	}}
}

func podList(pods ...*unstructured.Unstructured) *unstructured.UnstructuredList {
	list := &unstructured.UnstructuredList{}
	for _, p := range pods {
		list.Items = append(list.Items, *p)
	}
	return list
}

func watchdogFakeClient(objs ...kruntime.Object) *dynamicfake.FakeDynamicClient {
	// empty scheme keeps seeded pods unstructured; kscheme's typed v1.Pod would
	// make List attempt a typed conversion and fail.
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		kruntime.NewScheme(),
		map[schema.GroupVersionResource]string{
			kuadrantGVR:   "KuadrantList",
			podGVR:        "PodList",
			deploymentGVR: "DeploymentList",
		},
		objs...,
	)
}

func podDeletes(client *dynamicfake.FakeDynamicClient) []k8stesting.DeleteAction {
	var deletes []k8stesting.DeleteAction
	for _, a := range client.Actions() {
		if a.GetVerb() == "delete" && a.GetResource().Resource == "pods" {
			deletes = append(deletes, a.(k8stesting.DeleteAction))
		}
	}
	return deletes
}

func TestRestartOperatorPod(t *testing.T) {
	ctx := context.Background()

	t.Run("deletes pods matching the deployment selector and returns their uids", func(t *testing.T) {
		old := makeOperatorPod("kuadrant-operator-old", "old-1", true, true)
		other := makeOperatorPod("unrelated", "other-1", true, true)
		other.Object["metadata"].(map[string]any)["labels"] = map[string]any{"app": "something-else"}
		client := watchdogFakeClient(makeOperatorDeployment(), old, other)
		cfg := &Config{DynamicClient: client, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}

		selector, oldUIDs, err := restartOperatorPod(ctx, cfg, "kuadrant-system", "kuadrant-operator-controller-manager")
		if err != nil {
			t.Fatalf("want nil, got %v", err)
		}
		if selector.String() != "app=kuadrant-operator" {
			t.Errorf("selector = %q, want app=kuadrant-operator", selector.String())
		}
		if len(oldUIDs) != 1 || !oldUIDs["old-1"] {
			t.Errorf("oldUIDs = %v, want only old-1", oldUIDs)
		}
		deletes := podDeletes(client)
		if len(deletes) != 1 || deletes[0].GetName() != "kuadrant-operator-old" {
			t.Fatalf("want only kuadrant-operator-old deleted, got %v", deletes)
		}
	})

	t.Run("errors when the deployment has no selector", func(t *testing.T) {
		dep := makeOperatorDeployment()
		delete(dep.Object, "spec")
		client := watchdogFakeClient(dep)
		cfg := &Config{DynamicClient: client, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
		if _, _, err := restartOperatorPod(ctx, cfg, "kuadrant-system", "kuadrant-operator-controller-manager"); err == nil {
			t.Fatal("want error when matchLabels is absent")
		}
	})

	t.Run("errors when no pods match the selector", func(t *testing.T) {
		client := watchdogFakeClient(makeOperatorDeployment())
		cfg := &Config{DynamicClient: client, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
		if _, _, err := restartOperatorPod(ctx, cfg, "kuadrant-system", "kuadrant-operator-controller-manager"); err == nil {
			t.Fatal("want error when the selector matches no pods")
		}
	})
}

func TestWaitForNewOperatorPod(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()
	selector := labels.SelectorFromSet(map[string]string{"app": "kuadrant-operator"})
	oldUIDs := map[types.UID]bool{"old-1": true}

	t.Run("detects a new-uid running ready pod", func(t *testing.T) {
		client := watchdogFakeClient(makeOperatorPod("kuadrant-operator-new", "new-1", true, true))
		cfg := &Config{DynamicClient: client, Logger: logger}
		if err := waitForNewOperatorPod(ctx, cfg, "kuadrant-system", selector, oldUIDs, time.Second, time.Millisecond); err != nil {
			t.Fatalf("want nil, got %v", err)
		}
	})

	t.Run("ignores the surviving old pod even when ready", func(t *testing.T) {
		client := watchdogFakeClient(makeOperatorPod("kuadrant-operator-old", "old-1", true, true))
		cfg := &Config{DynamicClient: client, Logger: logger}
		if err := waitForNewOperatorPod(ctx, cfg, "kuadrant-system", selector, oldUIDs, 30*time.Millisecond, time.Millisecond); err == nil {
			t.Fatal("want timeout, old pod must be ignored")
		}
	})

	t.Run("ignores a new pod that is not yet ready", func(t *testing.T) {
		client := watchdogFakeClient(makeOperatorPod("kuadrant-operator-new", "new-1", true, false))
		cfg := &Config{DynamicClient: client, Logger: logger}
		if err := waitForNewOperatorPod(ctx, cfg, "kuadrant-system", selector, oldUIDs, 30*time.Millisecond, time.Millisecond); err == nil {
			t.Fatal("want timeout while the new pod is not ready")
		}
	})

	t.Run("returns when the context is cancelled", func(t *testing.T) {
		client := watchdogFakeClient(makeOperatorPod("kuadrant-operator-old", "old-1", true, true))
		cfg := &Config{DynamicClient: client, Logger: logger}
		cctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := waitForNewOperatorPod(cctx, cfg, "kuadrant-system", selector, oldUIDs, time.Second, time.Millisecond); err == nil {
			t.Fatal("want error on cancelled context")
		}
	})
}

func TestWaitForKuadrantReadyWatchdog(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()

	t.Run("does not fire when ready before threshold", func(t *testing.T) {
		client := watchdogFakeClient(makeKuadrant(readyTrue), makeOperatorDeployment())
		cfg := &Config{DynamicClient: client, Logger: logger}
		if err := waitForKuadrantReady(ctx, cfg, time.Second, time.Hour, time.Millisecond); err != nil {
			t.Fatalf("want nil, got %v", err)
		}
		if n := len(podDeletes(client)); n != 0 {
			t.Errorf("want no operator restarts, got %d", n)
		}
	})

	t.Run("fires once at threshold, never twice", func(t *testing.T) {
		client := watchdogFakeClient(makeKuadrant(nil), makeOperatorDeployment(),
			makeOperatorPod("kuadrant-operator-old", "old-1", true, true))
		cfg := &Config{DynamicClient: client, Logger: logger}
		err := waitForKuadrantReady(ctx, cfg, 100*time.Millisecond, 20*time.Millisecond, time.Millisecond)
		if err == nil {
			t.Fatal("want timeout error for a CR that never becomes ready")
		}
		deletes := podDeletes(client)
		if len(deletes) != 1 {
			t.Fatalf("want exactly one operator pod delete, got %d", len(deletes))
		}
		if deletes[0].GetName() != "kuadrant-operator-old" {
			t.Errorf("deleted %q, want kuadrant-operator-old", deletes[0].GetName())
		}
	})

	t.Run("recovers when a new operator pod comes up after the restart", func(t *testing.T) {
		client := watchdogFakeClient(makeOperatorDeployment(),
			makeOperatorPod("kuadrant-operator-old", "old-1", true, true))
		restarted := false
		client.PrependReactor("delete", "pods", func(_ k8stesting.Action) (bool, kruntime.Object, error) {
			restarted = true
			return false, nil, nil
		})
		client.PrependReactor("list", "pods", func(_ k8stesting.Action) (bool, kruntime.Object, error) {
			if restarted {
				return true, podList(makeOperatorPod("kuadrant-operator-new", "new-1", true, true)), nil
			}
			return true, podList(makeOperatorPod("kuadrant-operator-old", "old-1", true, true)), nil
		})
		client.PrependReactor("get", "kuadrants", func(_ k8stesting.Action) (bool, kruntime.Object, error) {
			if restarted {
				return true, makeKuadrant(readyTrue), nil
			}
			return true, makeKuadrant(nil), nil
		})
		cfg := &Config{DynamicClient: client, Logger: logger}
		if err := waitForKuadrantReady(ctx, cfg, time.Second, 20*time.Millisecond, time.Millisecond); err != nil {
			t.Fatalf("want recovery after restart, got %v", err)
		}
		if n := len(podDeletes(client)); n != 1 {
			t.Errorf("want exactly one operator pod delete, got %d", n)
		}
	})
}

// defaults unchanged: without the option the CR must stay metadata-only,
// byte-identical to what previous releases created.
func TestKuadrantCR(t *testing.T) {
	bare := (&kuadrant{}).kuadrantCR()
	if _, found, _ := unstructured.NestedMap(bare.Object, "spec"); found {
		t.Errorf("bare CR must carry no spec, got %v", bare.Object["spec"])
	}

	portal := (&kuadrant{devportal: true}).kuadrantCR()
	enabled, found, _ := unstructured.NestedBool(portal.Object, "spec", "components", "developerPortal", "enabled")
	if !found || !enabled {
		t.Errorf("devportal CR spec = %v, want components.developerPortal.enabled true", portal.Object["spec"])
	}
}

func bareKuadrantCR() *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "kuadrant.io/v1beta1",
			"kind":       "Kuadrant",
			"metadata": map[string]any{
				"name":      "kuadrant",
				"namespace": "kuadrant-system",
			},
		},
	}
}

func devportalConfig(t *testing.T, client *dynamicfake.FakeDynamicClient) *Config {
	t.Helper()
	if _, err := client.Resource(kuadrantGVR).Namespace("kuadrant-system").Create(context.Background(), bareKuadrantCR(), metav1.CreateOptions{}); err != nil {
		t.Fatalf("seeding kuadrant CR: %v", err)
	}
	return &Config{DynamicClient: client, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

// an existing bare CR (idempotent re-install) must be patched, and the patch
// verified against a fresh read.
func TestEnsureDevportalPatchesExistingCR(t *testing.T) {
	client := dynamicfake.NewSimpleDynamicClient(kscheme.Scheme)
	cfg := devportalConfig(t, client)

	for range 2 { // second call must be a no-op
		if err := ensureDevportal(context.Background(), cfg); err != nil {
			t.Fatalf("ensureDevportal: %v", err)
		}
	}

	obj, err := client.Resource(kuadrantGVR).Namespace("kuadrant-system").Get(context.Background(), "kuadrant", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !devportalEnabled(obj) {
		t.Errorf("CR spec = %v, want developerPortal enabled", obj.Object["spec"])
	}
}

// structural CRD pruning drops unknown fields while the write still reports
// success; a kuadrant version that predates the portal field must fail loud,
// not silently install without the portal.
func TestEnsureDevportalFailsLoudWhenPruned(t *testing.T) {
	client := dynamicfake.NewSimpleDynamicClient(kscheme.Scheme)
	cfg := devportalConfig(t, client)

	// simulate pruning: the patch "succeeds" but the stored CR never changes
	client.PrependReactor("patch", "kuadrants", func(k8stesting.Action) (bool, kruntime.Object, error) {
		return true, bareKuadrantCR(), nil
	})

	err := ensureDevportal(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected error when the field does not persist")
	}
	if !strings.Contains(err.Error(), "developerPortal") {
		t.Errorf("error %q should name the dropped field", err)
	}
}

// the merge patch must only add the portal field: unrelated spec the user
// already set on the CR has to survive.
func TestEnsureDevportalPreservesExistingSpec(t *testing.T) {
	cr := bareKuadrantCR()
	cr.Object["spec"] = map[string]any{
		"observability": map[string]any{"enable": true},
	}
	client := dynamicfake.NewSimpleDynamicClient(kscheme.Scheme)
	if _, err := client.Resource(kuadrantGVR).Namespace("kuadrant-system").Create(context.Background(), cr, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seeding kuadrant CR: %v", err)
	}
	cfg := &Config{DynamicClient: client, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}

	if err := ensureDevportal(context.Background(), cfg); err != nil {
		t.Fatalf("ensureDevportal: %v", err)
	}

	obj, err := client.Resource(kuadrantGVR).Namespace("kuadrant-system").Get(context.Background(), "kuadrant", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !devportalEnabled(obj) {
		t.Errorf("CR spec = %v, want developerPortal enabled", obj.Object["spec"])
	}
	kept, found, _ := unstructured.NestedBool(obj.Object, "spec", "observability", "enable")
	if !found || !kept {
		t.Errorf("CR spec = %v, pre-existing observability field must survive the patch", obj.Object["spec"])
	}
}
