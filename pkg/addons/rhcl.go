package addons

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/jasonmadigan/oinc/pkg/pullsecret"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
)

const (
	rhclCatalogSource = "redhat-operators"
	redHatRegistry    = "registry.redhat.io"
	// where CRI-O in the cluster container reads registry credentials
	clusterPullSecretPath = "/etc/crio/openshift-pull-secret"
	// covers pulling the operator index, unpacking four bundles and pulling
	// their operators
	rhclInstallTimeout = 20 * time.Minute
)

var rhclVersionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)

func init() { Register(newRHCL()) }

// rhcl installs Red Hat Connectivity Link, the downstream build of Kuadrant,
// through OLM from a Red Hat operator index. It replaces the kuadrant addon:
// both run the same operator in the same namespace.
type rhcl struct {
	version       string
	index         string
	devportal     bool
	consolePlugin bool

	// what the options resolve to, set by resolve
	release  rhclRelease
	resolved bool

	// replaced in tests
	fetchCatalog   func(ctx context.Context, logger *slog.Logger) (rhclCatalog, error)
	clusterHasFile func(cfg *Config, path string) bool
	applyURL       func(ctx context.Context, cfg *Config, url string) error
}

func newRHCL() *rhcl {
	return &rhcl{
		fetchCatalog: func(ctx context.Context, logger *slog.Logger) (rhclCatalog, error) {
			return fetchRHCLCatalog(ctx, logger, rhclCatalogAPI)
		},
		clusterHasFile: func(cfg *Config, path string) bool {
			_, err := cfg.Runtime.ExecInContainer(cfg.Container, "test", "-s", path)
			return err == nil
		},
		applyURL: applyManifestURL,
	}
}

func (r *rhcl) Name() string { return "rhcl" }

func (r *rhcl) Dependencies() []string {
	return []string{"gateway-api", "cert-manager", "metallb", "istio"}
}

func (r *rhcl) AlternativeTo() string { return "kuadrant" }

func (r *rhcl) NeedsPullSecret() bool { return true }

func (r *rhcl) Note() string { return "Red Hat pull secret required" }

func (r *rhcl) SetOptions(opts map[string]string) {
	if v, ok := opts["version"]; ok {
		r.version = v
		r.resolved = false
	}
	if v, ok := opts["index"]; ok {
		r.index = v
		r.resolved = false
	}
	if v, ok := opts["devportal"]; ok {
		r.devportal = v == "true"
	}
	if v, ok := opts["console-plugin"]; ok {
		r.consolePlugin = v == "true"
	}
}

// Validate checks the version, the stored pull secret and, unless an index
// is given, that the Red Hat catalog carries the version.
func (r *rhcl) Validate() error {
	if r.version != "" && !rhclVersionPattern.MatchString(r.version) {
		return fmt.Errorf("invalid version %q: use x.y.z, e.g. rhcl@1.4.3", r.version)
	}
	if _, err := pullsecret.PathFor(redHatRegistry); err != nil {
		return err
	}
	return r.resolve(context.Background(), slog.New(slog.DiscardHandler))
}

// resolve works out the channel, CSV and index to install from, once per
// set of options.
func (r *rhcl) resolve(ctx context.Context, logger *slog.Logger) error {
	if r.resolved {
		return nil
	}
	if r.index != "" {
		r.release = rhclReleaseByConvention(r.version, r.index)
		r.resolved = true
		return nil
	}

	catalog, err := r.fetchCatalog(ctx, logger)
	if err != nil {
		return fmt.Errorf("looking up rhcl in the Red Hat catalog: %w; pass --rhcl-index <image> to skip the lookup", err)
	}
	release, err := catalog.release(r.version)
	if err != nil {
		return err
	}
	r.release = release
	r.resolved = true
	return nil
}

// rhclReleaseByConvention maps a version to its minor channel without the
// catalog, for an index the catalog API does not describe or cannot be asked
// about. no version follows the head of stable.
func rhclReleaseByConvention(version, index string) rhclRelease {
	if version == "" {
		return rhclRelease{Channel: rhclStableChannel, Index: index}
	}
	return rhclRelease{
		Version: version,
		CSV:     rhclPackage + ".v" + version,
		Channel: rhclStableChannel + "-" + version[:strings.LastIndex(version, ".")],
		Index:   index,
	}
}

// operator pins the release: approval is manual so that only its InstallPlan
// is approved and later versions in the channel stay pending.
func (r *rhcl) operator() olmOperator {
	return olmOperator{
		namespace:          kuadrantNamespace,
		catalog:            rhclCatalogSource,
		catalogImage:       r.release.Index,
		catalogDisplayName: "Red Hat Operators",
		pkg:                rhclPackage,
		channel:            r.release.Channel,
		manual:             true,
		csv:                r.release.CSV,
	}
}

// Versions lists what the Red Hat catalog offers, newest first, marking the
// head of stable, which is what installing without a version gets. It offers
// nothing for an index override, which the catalog cannot describe.
func (r *rhcl) Versions(ctx context.Context) ([]VersionChoice, error) {
	if r.index != "" {
		return nil, nil
	}
	catalog, err := r.fetchCatalog(ctx, slog.New(slog.DiscardHandler))
	if err != nil {
		return nil, fmt.Errorf("looking up rhcl in the Red Hat catalog: %w; pass --rhcl-index <image> to skip the lookup", err)
	}
	head, headErr := catalog.release("")

	var choices []VersionChoice
	for _, release := range catalog.releases() {
		tag := release.Index[strings.LastIndex(release.Index, ":")+1:]
		choices = append(choices, VersionChoice{
			Version: release.Version,
			Hint:    fmt.Sprintf("%s, index %s", release.Channel, tag),
			Default: headErr == nil && release.Version == head.Version,
		})
	}
	return choices, nil
}

// ValidateCluster rejects a cluster that cannot pull from registry.redhat.io,
// that already runs upstream Kuadrant, or that runs another RHCL version.
func (r *rhcl) ValidateCluster(ctx context.Context, cfg *Config) error {
	if !r.clusterHasFile(cfg, clusterPullSecretPath) {
		return fmt.Errorf("the cluster was created without the Red Hat pull secret, so it cannot pull from %s; recreate it with rhcl in --addons, or with --version ocp-4.23", redHatRegistry)
	}

	sub, err := cfg.DynamicClient.Resource(subscriptionGVR).Namespace(kuadrantNamespace).Get(ctx, rhclPackage, metav1.GetOptions{})
	if err == nil {
		installed, _, _ := unstructured.NestedString(sub.Object, "status", "installedCSV")
		if installed != "" && r.release.CSV != "" && installed != r.release.CSV {
			return fmt.Errorf("%s is installed, not the requested %s: oinc does not change an installed operator's version, recreate the cluster to switch", installed, r.release.CSV)
		}
		return nil
	}
	if !errors.IsNotFound(err) {
		return fmt.Errorf("checking for an existing rhcl subscription: %w", err)
	}

	_, err = cfg.DynamicClient.Resource(deploymentGVR).Namespace(kuadrantNamespace).Get(ctx, kuadrantOperatorDeployment, metav1.GetOptions{})
	if err == nil {
		return fmt.Errorf("upstream kuadrant is installed in %s and rhcl cannot replace it in place; recreate the cluster", kuadrantNamespace)
	}
	if !errors.IsNotFound(err) {
		return fmt.Errorf("checking for an existing kuadrant operator: %w", err)
	}
	return nil
}

// ValidateCluster stops the upstream operator being installed over RHCL.
func (k *kuadrant) ValidateCluster(ctx context.Context, cfg *Config) error {
	if RHCLInstalled(ctx, cfg.DynamicClient) {
		return fmt.Errorf("RHCL is installed in %s and upstream kuadrant cannot be installed over it; recreate the cluster", kuadrantNamespace)
	}
	return nil
}

// RHCLInstalled reports whether the cluster runs RHCL rather than upstream
// Kuadrant: both use the same namespace and operator deployment name.
func RHCLInstalled(ctx context.Context, client dynamic.Interface) bool {
	_, err := client.Resource(subscriptionGVR).Namespace(kuadrantNamespace).Get(ctx, rhclPackage, metav1.GetOptions{})
	return err == nil
}

func (r *rhcl) Install(ctx context.Context, cfg *Config) error {
	if err := r.resolve(ctx, cfg.Logger); err != nil {
		return err
	}
	if r.consolePlugin {
		// ahead of the operator: it only looks for it when it starts
		if err := r.ensureClusterVersion(ctx, cfg); err != nil {
			return err
		}
	}

	cfg.Logger.Info("installing rhcl operator via OLM",
		"csv", r.release.CSV, "channel", r.release.Channel, "index", r.release.Index)

	if err := r.operator().install(ctx, cfg, rhclInstallTimeout, 5*time.Second); err != nil {
		return err
	}

	cfg.Logger.Info("rhcl operator installed")
	return nil
}

func (r *rhcl) Ready(ctx context.Context, cfg *Config) error {
	if err := kuadrantReady(ctx, cfg, r.devportal); err != nil {
		return err
	}
	if r.consolePlugin {
		return r.loadConsolePlugin(ctx, cfg, 5*time.Minute, 45*time.Second, 5*time.Second)
	}
	return nil
}
