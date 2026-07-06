package addons

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"os/exec"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	defaultKuadrantVersion     = "1.4.1"
	latestKuadrantCatalogImage = "quay.io/kuadrant/kuadrant-operator-catalog:latest"
)

func init() { Register(&kuadrant{}) }

type kuadrant struct {
	version string
}

func (k *kuadrant) Name() string {
	return "kuadrant"
}

func (k *kuadrant) Dependencies() []string {
	return []string{"gateway-api", "cert-manager", "metallb", "istio"}
}

func (k *kuadrant) SetOptions(opts map[string]string) {
	if v, ok := opts["version"]; ok {
		k.version = v
	}
}

func (k *kuadrant) resolveVersion() string {
	if k.version != "" {
		return k.version
	}
	return defaultKuadrantVersion
}

func (k *kuadrant) Install(ctx context.Context, cfg *Config) error {
	v := k.resolveVersion()

	if v == "latest" {
		return k.installViaOLM(ctx, cfg)
	}
	return k.installViaHelm(ctx, cfg, v)
}

func (k *kuadrant) installViaHelm(ctx context.Context, cfg *Config, version string) error {
	if _, err := exec.LookPath("helm"); err != nil {
		return fmt.Errorf("kuadrant addon requires helm: %w", err)
	}

	cfg.Logger.Info("installing kuadrant operator via helm", "version", version)

	// ensure helm repo
	out, err := exec.CommandContext(ctx, "helm", "repo", "add", "kuadrant",
		"https://kuadrant.io/helm-charts/", "--force-update",
	).CombinedOutput()
	if err != nil {
		return fmt.Errorf("helm repo add: %s: %w", string(out), err)
	}

	out, err = exec.CommandContext(ctx, "helm", "repo", "update", "kuadrant").CombinedOutput()
	if err != nil {
		return fmt.Errorf("helm repo update: %s: %w", string(out), err)
	}

	out, err = exec.CommandContext(ctx, "helm", "upgrade", "--install",
		"kuadrant-operator", "kuadrant/kuadrant-operator",
		"--version", version,
		"--create-namespace",
		"-n", "kuadrant-system",
		"--wait",
		"--timeout", "5m",
	).CombinedOutput()
	if err != nil {
		return fmt.Errorf("helm install kuadrant-operator: %s: %w", string(out), err)
	}

	cfg.Logger.Info("kuadrant operator installed")
	return nil
}

func (k *kuadrant) installViaOLM(ctx context.Context, cfg *Config) error {
	if _, err := exec.LookPath("kubectl"); err != nil {
		return fmt.Errorf("kuadrant addon with version=latest requires kubectl: %w", err)
	}

	cfg.Logger.Info("installing kuadrant operator via OLM", "catalogImage", latestKuadrantCatalogImage)

	// create namespace
	ns := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "v1",
			"kind":       "Namespace",
			"metadata": map[string]any{
				"name": "kuadrant-system",
			},
		},
	}
	nsGVR := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "namespaces"}
	if err := ensureResource(ctx, cfg, nsGVR, ns); err != nil {
		return fmt.Errorf("create namespace: %w", err)
	}

	// create OperatorGroup
	operatorGroup := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "operators.coreos.com/v1",
			"kind":       "OperatorGroup",
			"metadata": map[string]any{
				"name":      "kuadrant-system",
				"namespace": "kuadrant-system",
			},
			"spec": map[string]any{
				"upgradeStrategy": "Default",
			},
		},
	}
	ogGVR := schema.GroupVersionResource{Group: "operators.coreos.com", Version: "v1", Resource: "operatorgroups"}
	if err := ensureResource(ctx, cfg, ogGVR, operatorGroup); err != nil {
		return fmt.Errorf("create operatorgroup: %w", err)
	}

	// create CatalogSource
	catalogSource := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "operators.coreos.com/v1alpha1",
			"kind":       "CatalogSource",
			"metadata": map[string]any{
				"name":      "kuadrant-operator-catalog",
				"namespace": "kuadrant-system",
			},
			"spec": map[string]any{
				"sourceType":  "grpc",
				"image":       latestKuadrantCatalogImage,
				"displayName": "Kuadrant Operators",
				"publisher":   "grpc",
				"updateStrategy": map[string]any{
					"registryPoll": map[string]any{
						"interval": "5m",
					},
				},
			},
		},
	}
	csGVR := schema.GroupVersionResource{Group: "operators.coreos.com", Version: "v1alpha1", Resource: "catalogsources"}
	if err := ensureResource(ctx, cfg, csGVR, catalogSource); err != nil {
		return fmt.Errorf("create catalogsource: %w", err)
	}

	// create Subscription
	subscription := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "operators.coreos.com/v1alpha1",
			"kind":       "Subscription",
			"metadata": map[string]any{
				"name":      "kuadrant-operator",
				"namespace": "kuadrant-system",
			},
			"spec": map[string]any{
				"channel":             "preview",
				"installPlanApproval": "Automatic",
				"name":                "kuadrant-operator",
				"source":              "kuadrant-operator-catalog",
				"sourceNamespace":     "kuadrant-system",
				"config": map[string]any{
					"env": []map[string]any{
						{
							"name":  "ISTIO_GATEWAY_CONTROLLER_NAMES",
							"value": "openshift.io/gateway-controller/v1",
						},
					},
				},
			},
		},
	}
	subGVR := schema.GroupVersionResource{Group: "operators.coreos.com", Version: "v1alpha1", Resource: "subscriptions"}
	if err := ensureResource(ctx, cfg, subGVR, subscription); err != nil {
		return fmt.Errorf("create subscription: %w", err)
	}

	// wait for subscription to be ready
	cfg.Logger.Info("waiting for kuadrant operator subscription")
	if err := k.waitForSubscription(ctx, cfg, 5*time.Minute); err != nil {
		return err
	}

	cfg.Logger.Info("kuadrant operator installed via OLM")
	return nil
}

func (k *kuadrant) waitForSubscription(ctx context.Context, cfg *Config, timeout time.Duration) error {
	subGVR := schema.GroupVersionResource{Group: "operators.coreos.com", Version: "v1alpha1", Resource: "subscriptions"}
	csvGVR := schema.GroupVersionResource{Group: "operators.coreos.com", Version: "v1alpha1", Resource: "clusterserviceversions"}

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		// get subscription
		sub, err := cfg.DynamicClient.Resource(subGVR).Namespace("kuadrant-system").Get(ctx, "kuadrant-operator", metav1.GetOptions{})
		if err != nil {
			cfg.Logger.Debug("waiting for subscription", "err", err)
			time.Sleep(5 * time.Second)
			continue
		}

		// check subscription state
		state, found, _ := unstructured.NestedString(sub.Object, "status", "state")
		if !found || state != "AtLatestKnown" {
			cfg.Logger.Debug("waiting for subscription state", "current", state)
			time.Sleep(5 * time.Second)
			continue
		}

		// get installed CSV
		csvName, found, _ := unstructured.NestedString(sub.Object, "status", "installedCSV")
		if !found || csvName == "" {
			cfg.Logger.Debug("waiting for installedCSV")
			time.Sleep(5 * time.Second)
			continue
		}

		// check CSV phase
		csv, err := cfg.DynamicClient.Resource(csvGVR).Namespace("kuadrant-system").Get(ctx, csvName, metav1.GetOptions{})
		if err != nil {
			cfg.Logger.Debug("waiting for CSV", "name", csvName, "err", err)
			time.Sleep(5 * time.Second)
			continue
		}

		phase, found, _ := unstructured.NestedString(csv.Object, "status", "phase")
		if found && phase == "Succeeded" {
			cfg.Logger.Info("kuadrant operator CSV ready", "csv", csvName)
			return nil
		}

		cfg.Logger.Debug("waiting for CSV phase", "csv", csvName, "phase", phase)
		time.Sleep(5 * time.Second)
	}

	return fmt.Errorf("kuadrant operator not ready after %s", timeout)
}

func (k *kuadrant) Ready(ctx context.Context, cfg *Config) error {
	if err := waitForDeployment(ctx, cfg, "kuadrant-system", "kuadrant-operator-controller-manager", 5*time.Minute); err != nil {
		return err
	}

	waitForAdmissionMapper(ctx, cfg)

	// create the Kuadrant CR to deploy operand components
	kuadrantGVR := schema.GroupVersionResource{
		Group: "kuadrant.io", Version: "v1beta1", Resource: "kuadrants",
	}

	cr := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "kuadrant.io/v1beta1",
			"kind":       "Kuadrant",
			"metadata": map[string]any{
				"name":      "kuadrant",
				"namespace": "kuadrant-system",
			},
		},
	}

	if err := ensureResource(ctx, cfg, kuadrantGVR, cr); err != nil {
		return err
	}

	// wait for the Kuadrant CR to become ready
	return waitForKuadrantReady(ctx, cfg, 5*time.Minute)
}

const (
	admissionProbeUser     = "oinc-admission-probe"
	admissionProbeInterval = 2 * time.Second
	admissionProbeTimeout  = 90 * time.Second
)

var (
	roleGVR        = schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "roles"}
	roleBindingGVR = schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "rolebindings"}
	configMapGVR   = schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}
)

// waitForAdmissionMapper blocks until the apiserver's
// OwnerReferencesPermissionEnforcement plugin can resolve the Kuadrant kind.
// its RESTMapper only refreshes every 30s, and until it has seen the kuadrant
// CRD the operator's first ownerref write is rejected and the reconcile is
// dropped without requeue (kuadrant-operator#1578), so the CR never gets
// status. probes with impersonated server-side dry-run creates; on timeout
// logs a warning and proceeds so the gate can never make setup less reliable.
func waitForAdmissionMapper(ctx context.Context, cfg *Config) {
	cfg.Logger.Info("waiting for admission RESTMapper to discover kuadrant CRD")
	start := time.Now()

	role := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "rbac.authorization.k8s.io/v1",
			"kind":       "Role",
			"metadata": map[string]any{
				"name":      admissionProbeUser,
				"namespace": "kuadrant-system",
			},
			"rules": []any{
				map[string]any{
					"apiGroups": []any{""},
					"resources": []any{"configmaps"},
					"verbs":     []any{"create"},
				},
				map[string]any{
					"apiGroups": []any{"kuadrant.io"},
					"resources": []any{"kuadrants/finalizers"},
					"verbs":     []any{"update"},
				},
			},
		},
	}

	binding := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "rbac.authorization.k8s.io/v1",
			"kind":       "RoleBinding",
			"metadata": map[string]any{
				"name":      admissionProbeUser,
				"namespace": "kuadrant-system",
			},
			"subjects": []any{
				map[string]any{
					"kind":     "User",
					"name":     admissionProbeUser,
					"apiGroup": "rbac.authorization.k8s.io",
				},
			},
			"roleRef": map[string]any{
				"kind":     "Role",
				"name":     admissionProbeUser,
				"apiGroup": "rbac.authorization.k8s.io",
			},
		},
	}

	// detached ctx so cleanup still runs if the parent is cancelled
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		for _, gvr := range []schema.GroupVersionResource{roleBindingGVR, roleGVR} {
			err := cfg.DynamicClient.Resource(gvr).Namespace("kuadrant-system").Delete(cleanupCtx, admissionProbeUser, metav1.DeleteOptions{})
			if err != nil && !errors.IsNotFound(err) {
				cfg.Logger.Debug("admission probe rbac cleanup", "err", err)
			}
		}
	}()

	if err := ensureResource(ctx, cfg, roleGVR, role); err != nil {
		cfg.Logger.Warn("admission gate rbac setup failed, skipping gate", "err", err)
		return
	}
	if err := ensureResource(ctx, cfg, roleBindingGVR, binding); err != nil {
		cfg.Logger.Warn("admission gate rbac setup failed, skipping gate", "err", err)
		return
	}

	probeClient, err := admissionProbeClient(cfg.Kubeconfig)
	if err != nil {
		cfg.Logger.Warn("admission gate client setup failed, skipping gate", "err", err)
		return
	}

	if pollAdmissionProbe(ctx, cfg.Logger, probeClient, admissionProbeInterval, admissionProbeTimeout) {
		cfg.Logger.Info("admission RESTMapper ready", "waited", time.Since(start).Round(time.Millisecond))
	} else if ctx.Err() != nil {
		cfg.Logger.Warn("admission RESTMapper gate cancelled", "waited", time.Since(start).Round(time.Millisecond))
	} else {
		cfg.Logger.Warn("admission RESTMapper gate timed out, proceeding", "waited", time.Since(start).Round(time.Millisecond))
	}
}

// admissionProbeClient builds a dynamic client impersonating the probe user,
// so dry-run creates exercise the same authz path as the operator rather than
// short-circuiting on admin privileges.
func admissionProbeClient(kubeconfig []byte) (dynamic.Interface, error) {
	restCfg, err := clientcmd.RESTConfigFromKubeConfig(kubeconfig)
	if err != nil {
		return nil, err
	}
	restCfg.Impersonate.UserName = admissionProbeUser
	return dynamic.NewForConfig(restCfg)
}

// admissionProbeConfigMap returns a configmap owned by a fake Kuadrant with
// blockOwnerDeletion set: the write shape the admission plugin rejects while
// its mapper is cold. names are random so a 409 can't mask the result.
func admissionProbeConfigMap() *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "v1",
			"kind":       "ConfigMap",
			"metadata": map[string]any{
				"name":      fmt.Sprintf("%s-%x", admissionProbeUser, rand.Uint64()),
				"namespace": "kuadrant-system",
				"ownerReferences": []any{
					map[string]any{
						"apiVersion":         "kuadrant.io/v1beta1",
						"kind":               "Kuadrant",
						"name":               admissionProbeUser,
						"uid":                "00000000-0000-0000-0000-000000000000",
						"controller":         true,
						"blockOwnerDeletion": true,
					},
				},
			},
		},
	}
}

// pollAdmissionProbe dry-run creates probe configmaps until one is admitted.
func pollAdmissionProbe(ctx context.Context, logger *slog.Logger, client dynamic.Interface, interval, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return false
		}
		_, err := client.Resource(configMapGVR).Namespace("kuadrant-system").Create(ctx, admissionProbeConfigMap(), metav1.CreateOptions{
			DryRun: []string{metav1.DryRunAll},
		})
		if err == nil {
			return true
		}
		logger.Debug("admission probe rejected", "err", err)
		time.Sleep(interval)
	}
	return false
}

func waitForKuadrantReady(ctx context.Context, cfg *Config, timeout time.Duration) error {
	kuadrantGVR := schema.GroupVersionResource{
		Group: "kuadrant.io", Version: "v1beta1", Resource: "kuadrants",
	}

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		obj, err := cfg.DynamicClient.Resource(kuadrantGVR).Namespace("kuadrant-system").Get(ctx, "kuadrant", metav1.GetOptions{})
		if err != nil {
			cfg.Logger.Debug("waiting for kuadrant CR", "err", err)
			time.Sleep(5 * time.Second)
			continue
		}

		conditions, found, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
		if found {
			for _, c := range conditions {
				cm, ok := c.(map[string]any)
				if !ok {
					continue
				}
				if cm["type"] == "Ready" && cm["status"] == "True" {
					cfg.Logger.Info("kuadrant ready")
					return nil
				}
			}
		}

		cfg.Logger.Debug("waiting for kuadrant to become ready")
		time.Sleep(5 * time.Second)
	}

	return fmt.Errorf("kuadrant not ready after %s", timeout)
}
