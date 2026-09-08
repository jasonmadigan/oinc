package oinc

import (
	"context"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func TestManagedMCPGatewayReady(t *testing.T) {
	for _, tt := range []struct {
		name                string
		controllerAvailable int64
		extension           bool
		want                bool
	}{
		{"bundled controller only", 1, false, false},
		{"configured instance", 1, true, true},
		{"controller unavailable", 0, true, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			objects := []runtime.Object{&unstructured.Unstructured{Object: map[string]any{
				"apiVersion": "apps/v1", "kind": "Deployment",
				"metadata": map[string]any{"name": "mcp-gateway-controller", "namespace": "kuadrant-system"},
				"status":   map[string]any{"availableReplicas": tt.controllerAvailable},
			}}}
			if tt.extension {
				objects = append(objects, &unstructured.Unstructured{Object: map[string]any{
					"apiVersion": "mcp.kuadrant.io/v1", "kind": "MCPGatewayExtension",
					"metadata": map[string]any{"name": "mcp-gateway-extension", "namespace": "mcp-gateway-system"},
				}})
			}
			client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), objects...)
			if got := managedMCPGatewayReady(context.Background(), client); got != tt.want {
				t.Fatalf("ready = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestFormatUptime(t *testing.T) {
	tests := []struct {
		name string
		d    time.Duration
		want string
	}{
		{"seconds", 45 * time.Second, "45s"},
		{"minutes", 12 * time.Minute, "12m"},
		{"hours exact", 3 * time.Hour, "3h"},
		{"hours and minutes", 3*time.Hour + 25*time.Minute, "3h 25m"},
		{"days exact", 48 * time.Hour, "2d"},
		{"days and hours", 50 * time.Hour, "2d 2h"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatUptime(tt.d)
			if got != tt.want {
				t.Errorf("formatUptime(%v) = %q, want %q", tt.d, got, tt.want)
			}
		})
	}
}
