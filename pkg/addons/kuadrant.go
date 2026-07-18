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
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	defaultKuadrantVersion     = "1.4.1"
	latestKuadrantCatalogImage = "quay.io/kuadrant/kuadrant-operator-catalog:latest"
)

func init() { Register(&kuadrant{}) }

type kuadrant struct {
	version   string
	devportal bool
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
	if v, ok := opts["devportal"]; ok {
		k.devportal = v == "true"
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
	if err := ensureResource(ctx, cfg, kuadrantGVR, k.kuadrantCR()); err != nil {
		return err
	}

	if k.devportal {
		if err := ensureDevportal(ctx, cfg); err != nil {
			return err
		}
	}

	// wait for the Kuadrant CR to become ready
	if err := waitForKuadrantReady(ctx, cfg, 5*time.Minute, kuadrantWatchdogAfter, 5*time.Second); err != nil {
		return err
	}

	if k.devportal {
		// the operator creates the controller deployment asynchronously;
		// waitForDeployment rides out both its appearance and its rollout
		cfg.Logger.Info("waiting for developer portal controller")
		if err := waitForDeployment(ctx, cfg, "kuadrant-system", "developer-portal-controller", 5*time.Minute); err != nil {
			return fmt.Errorf("developer portal enabled on the kuadrant CR but its controller did not roll out: %w", err)
		}
	}
	return nil
}

// kuadrantCR builds the Kuadrant CR. Without options it stays metadata-only,
// matching what previous releases created.
func (k *kuadrant) kuadrantCR() *unstructured.Unstructured {
	obj := map[string]any{
		"apiVersion": "kuadrant.io/v1beta1",
		"kind":       "Kuadrant",
		"metadata": map[string]any{
			"name":      "kuadrant",
			"namespace": "kuadrant-system",
		},
	}
	if k.devportal {
		obj["spec"] = map[string]any{
			"components": map[string]any{
				"developerPortal": map[string]any{"enabled": true},
			},
		}
	}
	return &unstructured.Unstructured{Object: obj}
}

// ensureDevportal enables the developer portal on the Kuadrant CR and
// verifies the field persisted. Structural CRD pruning silently drops unknown
// fields, so both the create and a merge-patch can report success without
// taking effect when the installed kuadrant version predates the field.
func ensureDevportal(ctx context.Context, cfg *Config) error {
	client := cfg.DynamicClient.Resource(kuadrantGVR).Namespace("kuadrant-system")

	obj, err := client.Get(ctx, "kuadrant", metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("getting kuadrant CR: %w", err)
	}
	if devportalEnabled(obj) {
		return nil
	}

	patch := []byte(`{"spec":{"components":{"developerPortal":{"enabled":true}}}}`)
	if _, err := client.Patch(ctx, "kuadrant", types.MergePatchType, patch, metav1.PatchOptions{}); err != nil {
		return fmt.Errorf("enabling developer portal on kuadrant CR: %w", err)
	}

	// verify against a fresh read, not the patch response
	obj, err = client.Get(ctx, "kuadrant", metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("getting kuadrant CR: %w", err)
	}
	if !devportalEnabled(obj) {
		return fmt.Errorf("spec.components.developerPortal did not persist on the kuadrant CR: the installed kuadrant version likely predates the developer portal, pin one that ships it (e.g. kuadrant@latest)")
	}
	return nil
}

func devportalEnabled(obj *unstructured.Unstructured) bool {
	enabled, found, _ := unstructured.NestedBool(obj.Object, "spec", "components", "developerPortal", "enabled")
	return found && enabled
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
	kuadrantGVR    = schema.GroupVersionResource{Group: "kuadrant.io", Version: "v1beta1", Resource: "kuadrants"}
	podGVR         = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"}
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

// one-shot operator restart threshold while waiting for the kuadrant CR
const kuadrantWatchdogAfter = 2 * time.Minute

func waitForKuadrantReady(ctx context.Context, cfg *Config, timeout, watchdogAfter, interval time.Duration) error {
	start := time.Now()
	deadline := start.Add(timeout)
	watchdogFired := false

	for time.Now().Before(deadline) {
		var ready bool
		var why string
		obj, err := cfg.DynamicClient.Resource(kuadrantGVR).Namespace("kuadrant-system").Get(ctx, "kuadrant", metav1.GetOptions{})
		if err != nil {
			why = fmt.Sprintf("cannot get kuadrant CR: %v", err)
		} else {
			ready, why = kuadrantReadyState(obj)
		}

		if ready {
			cfg.Logger.Info("kuadrant ready")
			return nil
		}

		// the operator's startup dependency check doesn't re-run; if a
		// dependency landed after it, the CR wedges at Ready=False until the
		// operator process restarts (kuadrant-operator#1784). delete the
		// operator pod once and keep waiting within the same overall timeout.
		if !watchdogFired && time.Since(start) >= watchdogAfter {
			watchdogFired = true
			cfg.Logger.Warn("kuadrant not ready, restarting operator pod once", "after", time.Since(start).Round(time.Second), "why", why)
			selector, oldUIDs, err := restartOperatorPod(ctx, cfg, "kuadrant-system", "kuadrant-operator-controller-manager")
			if err != nil {
				cfg.Logger.Warn("operator pod restart failed", "err", err)
			} else if err := waitForNewOperatorPod(ctx, cfg, "kuadrant-system", selector, oldUIDs, time.Until(deadline), interval); err != nil {
				cfg.Logger.Warn("operator pod not back after restart", "err", err)
			}
			continue
		}

		cfg.Logger.Debug("waiting for kuadrant to become ready", "why", why)
		time.Sleep(interval)
	}

	return fmt.Errorf("kuadrant not ready after %s", timeout)
}

// kuadrantReadyState reports whether the CR's Ready condition is True, plus a
// short explanation for logging when it is not.
func kuadrantReadyState(obj *unstructured.Unstructured) (bool, string) {
	conditions, found, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
	if !found || len(conditions) == 0 {
		return false, "status empty"
	}
	for _, c := range conditions {
		cm, ok := c.(map[string]any)
		if !ok || cm["type"] != "Ready" {
			continue
		}
		if cm["status"] == "True" {
			return true, ""
		}
		return false, fmt.Sprintf("reason=%v message=%v", cm["reason"], cm["message"])
	}
	return false, "no Ready condition"
}

// restartOperatorPod deletes the operator pods behind a deployment so the
// ReplicaSet recreates them with a fresh process. OLM owns CSV-managed operator
// deployments and reverts out-of-band pod-template edits once the CSV has
// succeeded, so the kubectl rollout restart stamp never cycles the pod in
// steady state (kuadrant-operator#1784); deleting the pod does. returns the
// pods' label selector and UIDs so the caller can wait for a genuinely new pod.
func restartOperatorPod(ctx context.Context, cfg *Config, namespace, deployment string) (labels.Selector, map[types.UID]bool, error) {
	dep, err := cfg.DynamicClient.Resource(deploymentGVR).Namespace(namespace).Get(ctx, deployment, metav1.GetOptions{})
	if err != nil {
		return nil, nil, fmt.Errorf("get deployment %s/%s: %w", namespace, deployment, err)
	}

	matchLabels, found, err := unstructured.NestedStringMap(dep.Object, "spec", "selector", "matchLabels")
	if err != nil || !found || len(matchLabels) == 0 {
		return nil, nil, fmt.Errorf("deployment %s/%s has no spec.selector.matchLabels", namespace, deployment)
	}
	selector := labels.SelectorFromSet(matchLabels)

	pods, err := cfg.DynamicClient.Resource(podGVR).Namespace(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector.String()})
	if err != nil {
		return nil, nil, fmt.Errorf("list operator pods: %w", err)
	}
	if len(pods.Items) == 0 {
		return nil, nil, fmt.Errorf("no pods match %s in %s", selector, namespace)
	}

	oldUIDs := make(map[types.UID]bool, len(pods.Items))
	for i := range pods.Items {
		p := &pods.Items[i]
		oldUIDs[p.GetUID()] = true
		if err := cfg.DynamicClient.Resource(podGVR).Namespace(namespace).Delete(ctx, p.GetName(), metav1.DeleteOptions{}); err != nil && !errors.IsNotFound(err) {
			return nil, nil, fmt.Errorf("delete pod %s: %w", p.GetName(), err)
		}
	}
	return selector, oldUIDs, nil
}

// waitForNewOperatorPod polls until a pod matching selector, with a UID not in
// oldUIDs, is Running and Ready. unlike waitForDeployment's availableReplicas>0
// check, this ignores the surviving old pod during a rolling restart and only
// returns once the replacement process is genuinely up.
func waitForNewOperatorPod(ctx context.Context, cfg *Config, namespace string, selector labels.Selector, oldUIDs map[types.UID]bool, timeout, interval time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		pods, err := cfg.DynamicClient.Resource(podGVR).Namespace(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector.String()})
		if err == nil {
			for i := range pods.Items {
				p := &pods.Items[i]
				if oldUIDs[p.GetUID()] {
					continue
				}
				if podRunningReady(p) {
					cfg.Logger.Info("operator pod restarted", "namespace", namespace, "pod", p.GetName())
					return nil
				}
			}
		}
		time.Sleep(interval)
	}
	return fmt.Errorf("no new operator pod ready for %s in %s after %s", selector, namespace, timeout)
}

// podRunningReady reports whether a pod is in phase Running with Ready=True.
func podRunningReady(pod *unstructured.Unstructured) bool {
	phase, _, _ := unstructured.NestedString(pod.Object, "status", "phase")
	if phase != "Running" {
		return false
	}
	conditions, found, _ := unstructured.NestedSlice(pod.Object, "status", "conditions")
	if !found {
		return false
	}
	for _, c := range conditions {
		cm, ok := c.(map[string]any)
		if !ok || cm["type"] != "Ready" {
			continue
		}
		return cm["status"] == "True"
	}
	return false
}
