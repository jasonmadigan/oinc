package addons

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	kruntime "k8s.io/apimachinery/pkg/runtime"
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
