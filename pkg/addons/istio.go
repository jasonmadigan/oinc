package addons

import (
	"context"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func init() { Register(&istio{}) }

type istio struct{}

func (i *istio) Name() string           { return "istio" }
func (i *istio) Dependencies() []string { return nil }

func (i *istio) Install(ctx context.Context, cfg *Config) error {
	return installOperator(ctx, cfg, subscriptionOpts{
		name:    "sailoperator",
		channel: "3.0-latest",
		catalog: sailCatalog,
	})
}

func (i *istio) Ready(ctx context.Context, cfg *Config) error {
	if err := waitForCSV(ctx, cfg, "sailoperator", 5*time.Minute); err != nil {
		return err
	}

	if err := ensureNamespace(ctx, cfg, "istio-system"); err != nil {
		return err
	}

	istioGVR := schema.GroupVersionResource{
		Group: "sailoperator.io", Version: "v1alpha1", Resource: "istios",
	}

	cr := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "sailoperator.io/v1alpha1",
			"kind":       "Istio",
			"metadata": map[string]any{
				"name":      "default",
				"namespace": "istio-system",
			},
			"spec": map[string]any{
				"namespace": "istio-system",
			},
		},
	}

	return ensureResource(ctx, cfg, istioGVR, cr)
}
