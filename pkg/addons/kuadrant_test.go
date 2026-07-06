package addons

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	kruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
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
		"status":     map[string]any{"availableReplicas": int64(1)},
	}}
}

func watchdogFakeClient(objs ...kruntime.Object) *dynamicfake.FakeDynamicClient {
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		kscheme.Scheme,
		map[schema.GroupVersionResource]string{kuadrantGVR: "KuadrantList"},
		objs...,
	)
}

func deploymentPatches(client *dynamicfake.FakeDynamicClient) []k8stesting.PatchAction {
	var patches []k8stesting.PatchAction
	for _, a := range client.Actions() {
		if a.GetVerb() == "patch" && a.GetResource().Resource == "deployments" {
			patches = append(patches, a.(k8stesting.PatchAction))
		}
	}
	return patches
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
		if n := len(deploymentPatches(client)); n != 0 {
			t.Errorf("want no operator restarts, got %d", n)
		}
	})

	t.Run("fires once at threshold, never twice", func(t *testing.T) {
		client := watchdogFakeClient(makeKuadrant(nil), makeOperatorDeployment())
		cfg := &Config{DynamicClient: client, Logger: logger}
		err := waitForKuadrantReady(ctx, cfg, 100*time.Millisecond, 20*time.Millisecond, time.Millisecond)
		if err == nil {
			t.Fatal("want timeout error for a CR that never becomes ready")
		}
		patches := deploymentPatches(client)
		if len(patches) != 1 {
			t.Fatalf("want exactly one operator restart, got %d", len(patches))
		}
		if !bytes.Contains(patches[0].GetPatch(), []byte("kubectl.kubernetes.io/restartedAt")) {
			t.Errorf("restart patch missing restartedAt stamp: %s", patches[0].GetPatch())
		}
	})

	t.Run("recovers when the CR turns ready after the restart", func(t *testing.T) {
		client := watchdogFakeClient(makeOperatorDeployment())
		restarted := false
		client.PrependReactor("patch", "deployments", func(_ k8stesting.Action) (bool, kruntime.Object, error) {
			restarted = true
			return false, nil, nil
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
		if n := len(deploymentPatches(client)); n != 1 {
			t.Errorf("want exactly one operator restart, got %d", n)
		}
	})
}
