package oinc

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/jasonmadigan/oinc/pkg/download"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/util/retry"
)

// applyCRD downloads the manifest of the CRD called name and creates or
// updates it.
func applyCRD(ctx context.Context, client dynamic.Interface, logger *slog.Logger, url, name string) error {
	crd, err := fetchCRD(ctx, logger, url, name)
	if err != nil {
		return err
	}
	return applyCRDObject(ctx, client, logger, crd)
}

// fetchCRD downloads a manifest that must hold exactly the CRD called name.
func fetchCRD(ctx context.Context, logger *slog.Logger, url, name string) (*unstructured.Unstructured, error) {
	body, err := download.URL(ctx, logger, url)
	if err != nil {
		return nil, err
	}
	crd, err := decodeCRD(body, name)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", url, err)
	}
	return crd, nil
}

// applyCRDObject creates or updates a CRD.
func applyCRDObject(ctx context.Context, client dynamic.Interface, logger *slog.Logger, crd *unstructured.Unstructured) error {
	name := crd.GetName()
	logger.Debug("applying CRD", "name", name)
	crds := client.Resource(crdGVR)
	// the API server writes a CRD's status as it establishes it, which can
	// land between the read and the update
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		existing, err := crds.Get(ctx, name, metav1.GetOptions{})
		if errors.IsNotFound(err) {
			_, err = crds.Create(ctx, crd, metav1.CreateOptions{})
			return err
		}
		if err != nil {
			return err
		}
		crd.SetResourceVersion(existing.GetResourceVersion())
		_, err = crds.Update(ctx, crd, metav1.UpdateOptions{})
		return err
	})
	if err != nil {
		return fmt.Errorf("applying CRD %s: %w", name, err)
	}
	return nil
}

// decodeCRD reads a manifest that must hold exactly the CRD called name.
func decodeCRD(manifest []byte, name string) (*unstructured.Unstructured, error) {
	decoder := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(manifest), 4096)
	var crd *unstructured.Unstructured
	for {
		obj := &unstructured.Unstructured{}
		if err := decoder.Decode(obj); err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("decoding manifest: %w", err)
		}
		if obj.Object == nil {
			continue
		}
		if crd != nil {
			return nil, fmt.Errorf("manifest holds more than one object, want only CRD %s", name)
		}
		crd = obj
	}
	if crd == nil {
		return nil, fmt.Errorf("manifest is empty, want CRD %s", name)
	}
	if crd.GetKind() != "CustomResourceDefinition" || crd.GetName() != name {
		return nil, fmt.Errorf("manifest holds %s %q, want CRD %s", crd.GetKind(), crd.GetName(), name)
	}
	return crd, nil
}

// waitForCRDsEstablished blocks until the API server serves each CRD.
func waitForCRDsEstablished(ctx context.Context, client dynamic.Interface, names []string, timeout, interval time.Duration) error {
	deadline := time.Now().Add(timeout)
	for _, name := range names {
		for {
			crd, err := client.Resource(crdGVR).Get(ctx, name, metav1.GetOptions{})
			if err == nil && crdEstablished(crd) {
				break
			}
			if time.Now().After(deadline) {
				if err != nil {
					return fmt.Errorf("CRD %s not established after %s: %w", name, timeout, err)
				}
				return fmt.Errorf("CRD %s not established after %s", name, timeout)
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(interval):
			}
		}
	}
	return nil
}

func crdEstablished(crd *unstructured.Unstructured) bool {
	conditions, _, _ := unstructured.NestedSlice(crd.Object, "status", "conditions")
	for _, c := range conditions {
		cm, ok := c.(map[string]any)
		if ok && cm["type"] == "Established" && cm["status"] == "True" {
			return true
		}
	}
	return false
}
