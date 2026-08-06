package addons

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kscheme "k8s.io/client-go/kubernetes/scheme"
)

func fakeMCPGateway(listeners []map[string]any) *unstructured.Unstructured {
	obj := map[string]any{
		"apiVersion": "gateway.networking.k8s.io/v1",
		"kind":       "Gateway",
		"metadata": map[string]any{
			"name":      "mcp-gateway",
			"namespace": gatewaySystemNamespace,
		},
	}
	if listeners != nil {
		obj["status"] = map[string]any{
			"listeners": func() []any {
				out := make([]any, len(listeners))
				for i, l := range listeners {
					out[i] = l
				}
				return out
			}(),
		}
	}
	return &unstructured.Unstructured{Object: obj}
}

func mcpGatewayReadyConfig(t *testing.T, gw *unstructured.Unstructured) *Config {
	t.Helper()
	client := dynamicfake.NewSimpleDynamicClient(kscheme.Scheme)
	if _, err := client.Resource(gatewayGVR).Namespace(gatewaySystemNamespace).Create(
		context.Background(), gw, metav1.CreateOptions{},
	); err != nil {
		t.Fatalf("seeding gateway: %v", err)
	}
	return &Config{
		DynamicClient: client,
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func TestWaitForMCPListenerReady(t *testing.T) {
	t.Run("mcp listener accepted", func(t *testing.T) {
		cfg := mcpGatewayReadyConfig(t, fakeMCPGateway([]map[string]any{
			{
				"name": "http",
				"conditions": []any{
					map[string]any{"type": "Accepted", "status": "True"},
				},
			},
			{
				"name": "mcp",
				"conditions": []any{
					map[string]any{"type": "Accepted", "status": "True"},
				},
			},
		}))
		err := waitForMCPListener(context.Background(), cfg, 50*time.Millisecond, 5*time.Millisecond)
		if err != nil {
			t.Fatalf("expected success, got: %v", err)
		}
	})

	t.Run("mcp listener not yet present", func(t *testing.T) {
		cfg := mcpGatewayReadyConfig(t, fakeMCPGateway([]map[string]any{
			{
				"name": "http",
				"conditions": []any{
					map[string]any{"type": "Accepted", "status": "True"},
				},
			},
		}))
		err := waitForMCPListener(context.Background(), cfg, 30*time.Millisecond, 5*time.Millisecond)
		if err == nil {
			t.Fatal("expected timeout when mcp listener is missing")
		}
		if !strings.Contains(err.Error(), "mcp") {
			t.Errorf("error %q should mention the mcp listener", err)
		}
	})

	t.Run("mcp listener not accepted", func(t *testing.T) {
		cfg := mcpGatewayReadyConfig(t, fakeMCPGateway([]map[string]any{
			{
				"name": "mcp",
				"conditions": []any{
					map[string]any{"type": "Accepted", "status": "False", "reason": "Pending"},
				},
			},
		}))
		err := waitForMCPListener(context.Background(), cfg, 30*time.Millisecond, 5*time.Millisecond)
		if err == nil {
			t.Fatal("expected timeout when mcp listener is not accepted")
		}
	})

	t.Run("no status yet", func(t *testing.T) {
		cfg := mcpGatewayReadyConfig(t, fakeMCPGateway(nil))
		err := waitForMCPListener(context.Background(), cfg, 30*time.Millisecond, 5*time.Millisecond)
		if err == nil {
			t.Fatal("expected timeout when gateway has no status")
		}
	})

	t.Run("context cancelled", func(t *testing.T) {
		cfg := mcpGatewayReadyConfig(t, fakeMCPGateway(nil))
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err := waitForMCPListener(ctx, cfg, time.Second, 5*time.Millisecond)
		if err == nil {
			t.Fatal("expected error on cancelled context")
		}
	})
}
