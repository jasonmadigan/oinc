package addons

import (
	"context"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func init() { Register(&metalLB{}) }

type metalLB struct{}

func (m *metalLB) Name() string           { return "metallb" }
func (m *metalLB) Dependencies() []string { return nil }

func (m *metalLB) Install(ctx context.Context, cfg *Config) error {
	return installOperator(ctx, cfg, subscriptionOpts{
		name:    "metallb-operator",
		channel: "stable",
		catalog: communityCatalog,
	})
}

func (m *metalLB) Ready(ctx context.Context, cfg *Config) error {
	if err := waitForCSV(ctx, cfg, "metallb-operator", 5*time.Minute); err != nil {
		return err
	}

	if err := ensureNamespace(ctx, cfg, "metallb-system"); err != nil {
		return err
	}

	metallbGVR := schema.GroupVersionResource{
		Group: "metallb.io", Version: "v1beta1", Resource: "metallbs",
	}

	cr := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "metallb.io/v1beta1",
			"kind":       "MetalLB",
			"metadata": map[string]any{
				"name":      "metallb",
				"namespace": "metallb-system",
			},
		},
	}

	return ensureResource(ctx, cfg, metallbGVR, cr)
}
