package oinc

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jasonmadigan/oinc/pkg/addons"
	"github.com/jasonmadigan/oinc/pkg/cluster"
	"github.com/jasonmadigan/oinc/pkg/kubeconfig"
	"github.com/jasonmadigan/oinc/pkg/pullsecret"
	"github.com/jasonmadigan/oinc/pkg/runtime"
	"github.com/jasonmadigan/oinc/pkg/tui"
	"github.com/jasonmadigan/oinc/pkg/version"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	containerName = "oinc"
	hostname      = "127.0.0.1.nip.io"
	labelKey      = "io.oinc.cluster"
	kubeconfigDir = "/var/lib/microshift/resources/kubeadmin"
)

type CreateOpts struct {
	Version         string
	RuntimeOverride string
	HTTPPort        int
	HTTPSPort       int
	Console         ConsoleOpts
	Addons          string
}

// resolveAddons splits a comma-separated addon list, resolves dependency
// order and runs each addon's validation hook.
func resolveAddons(addonList string) ([]addons.Addon, error) {
	sorted, err := addons.Resolve(splitAddons(addonList))
	if err != nil {
		return nil, err
	}
	if err := addons.Validate(sorted); err != nil {
		return nil, err
	}

	return sorted, nil
}

func splitAddons(addonList string) []string {
	names := strings.Split(addonList, ",")
	for i := range names {
		names[i] = strings.TrimSpace(names[i])
	}
	return names
}

// PreflightAddons validates an addon list before any container work, so bad
// input fails in seconds rather than minutes into a create.
func PreflightAddons(addonList string) error {
	_, err := resolveAddons(addonList)
	return err
}

// CreateSteps returns the create flow as discrete steps for the TUI.
func CreateSteps(ctx context.Context, opts CreateOpts) (string, []*tui.Step) {
	var (
		ver version.OCPVersion
		rt  *runtime.Runtime
		raw []byte
	)

	// resolved by the pre-flight step, for what the container must provide
	var requested []addons.Addon

	var steps []*tui.Step
	if opts.Addons != "" {
		steps = append(steps, &tui.Step{Name: "validating addons", Run: func() error {
			sorted, err := resolveAddons(opts.Addons)
			requested = sorted
			return err
		}})
	}

	steps = append(steps,
		&tui.Step{Name: "resolving version", Run: func() error {
			v, err := version.ResolveContext(ctx, opts.Version)
			if err != nil {
				return err
			}
			ver = v
			return PreflightVersion(ver)
		}},
		&tui.Step{Name: "detecting runtime", Run: func() error {
			r, err := runtime.Detect(opts.RuntimeOverride)
			if err != nil {
				return err
			}
			rt = r
			return nil
		}},
		&tui.Step{Name: "pulling image", Run: func() error {
			return rt.PullImage(ver.MicroShiftImage(), ver.Platform())
		}},
		&tui.Step{Name: "creating container", Run: func() error {
			if rt.ContainerExists(containerName) {
				if err := checkExistingVersion(rt, ver); err != nil {
					return err
				}
				return rt.StartContainer(containerName)
			}
			copts, err := clusterContainerOpts(opts, ver, requested)
			if err != nil {
				return err
			}
			if err := rt.CreateContainer(copts); err != nil {
				return err
			}
			return rt.StartContainer(containerName)
		}},
		&tui.Step{Name: "waiting for microshift", Run: func() error {
			return rt.WaitForService(containerName, "microshift", 120, 5*time.Second)
		}},
		&tui.Step{Name: "merging kubeconfig", Run: func() error {
			kcPath := fmt.Sprintf("%s/%s/kubeconfig", kubeconfigDir, hostname)
			kc, err := rt.CopyFromContainer(containerName, kcPath)
			if err != nil {
				return err
			}
			raw = kc
			return kubeconfig.Update(raw)
		}},
		&tui.Step{Name: "waiting for pods", Run: func() error {
			return cluster.WaitForReady(ctx, raw, 60, 5*time.Second)
		}},
		&tui.Step{Name: "setting up console", Run: func() error {
			logger := slog.New(slog.NewTextHandler(devNull{}, nil))
			return setupConsole(rt, raw, ver, opts.Console, logger)
		}},
	)

	if opts.Addons != "" {
		steps = append(steps, &tui.Step{
			Name: "installing addons",
			Run: func() error {
				logger := slog.New(slog.NewTextHandler(devNull{}, nil))
				return InstallAddons(ctx, opts.Addons, raw, rt, logger)
			},
		})
	}

	title := "creating cluster"
	if opts.Version != "" {
		title = fmt.Sprintf("creating cluster (%s)", opts.Version)
	}

	return title, steps
}

// Create runs the full create flow. Uses TUI when possible, plain output otherwise.
func Create(ctx context.Context, opts CreateOpts, logger *slog.Logger) error {
	title, steps := CreateSteps(ctx, opts)
	if !tui.IsTTY() {
		return createPlain(ctx, opts, logger)
	}
	summary := func() string {
		s := GetStatus(opts.RuntimeOverride)
		return s.RenderSummary()
	}
	return tui.RunSteps(title, steps, tui.WithSummary(summary))
}

// createPlain is the original slog-based create for non-TTY contexts.
func createPlain(ctx context.Context, opts CreateOpts, logger *slog.Logger) error {
	var requested []addons.Addon
	if opts.Addons != "" {
		logger.Info("validating addons", "addons", opts.Addons)
		sorted, err := resolveAddons(opts.Addons)
		if err != nil {
			return fmt.Errorf("addon pre-flight: %w", err)
		}
		requested = sorted
	}

	ver, err := version.ResolveContext(ctx, opts.Version)
	if err != nil {
		return err
	}
	if err := PreflightVersion(ver); err != nil {
		return err
	}
	logger.Info("resolved version", "version", ver.Version)

	rt, err := runtime.Detect(opts.RuntimeOverride)
	if err != nil {
		return err
	}
	logger.Info("using runtime", "runtime", rt.Name())

	image := ver.MicroShiftImage()
	platform := ver.Platform()
	logger.Info("pulling image", "image", image)
	if err := rt.PullImage(image, platform); err != nil {
		return fmt.Errorf("pulling image: %w", err)
	}

	if rt.ContainerExists(containerName) {
		if err := checkExistingVersion(rt, ver); err != nil {
			return err
		}
		logger.Info("container already exists, starting")
	} else {
		logger.Info("creating container")
		copts, err := clusterContainerOpts(opts, ver, requested)
		if err != nil {
			return err
		}
		if err := rt.CreateContainer(copts); err != nil {
			return fmt.Errorf("creating container: %w", err)
		}
	}

	if err := rt.StartContainer(containerName); err != nil {
		return fmt.Errorf("starting container: %w", err)
	}

	logger.Info("waiting for microshift service")
	if err := rt.WaitForService(containerName, "microshift", 120, 5*time.Second); err != nil {
		return fmt.Errorf("microshift service: %w", err)
	}

	logger.Info("fetching kubeconfig")
	kcPath := fmt.Sprintf("%s/%s/kubeconfig", kubeconfigDir, hostname)
	raw, err := rt.CopyFromContainer(containerName, kcPath)
	if err != nil {
		return fmt.Errorf("fetching kubeconfig: %w", err)
	}
	if err := kubeconfig.Update(raw); err != nil {
		return fmt.Errorf("updating kubeconfig: %w", err)
	}
	logger.Info("kubeconfig merged", "path", kubeconfig.Path())

	logger.Info("waiting for pods to be ready")
	if err := cluster.WaitForReady(ctx, raw, 60, 5*time.Second); err != nil {
		return fmt.Errorf("cluster readiness: %w", err)
	}

	logger.Info("setting up console")
	if err := setupConsole(rt, raw, ver, opts.Console, logger); err != nil {
		return fmt.Errorf("console setup: %w", err)
	}

	if opts.Addons != "" {
		if err := InstallAddons(ctx, opts.Addons, raw, rt, logger); err != nil {
			return fmt.Errorf("addon installation: %w", err)
		}
	}

	logger.Info("cluster ready")
	return nil
}

// devNull discards all writes.
type devNull struct{}

func (devNull) Write(p []byte) (int, error) { return len(p), nil }

// ingressHTTPPort returns the host port mapped to the cluster container's
// port 80. 0 with a non-nil error when it cannot be determined.
func ingressHTTPPort(rt *runtime.Runtime) (int, error) {
	info, err := rt.InspectContainer(containerName)
	if err != nil {
		return 0, fmt.Errorf("inspecting %s container: %w", containerName, err)
	}
	if info.Ports[80] == 0 {
		return 0, fmt.Errorf("%s container has no host port mapped to port 80", containerName)
	}
	return info.Ports[80], nil
}

func InstallAddons(ctx context.Context, addonList string, kubeconfig []byte, rt *runtime.Runtime, logger *slog.Logger) error {
	sorted, err := resolveAddons(addonList)
	if err != nil {
		return err
	}
	cfg, err := addonConfig(kubeconfig, rt, logger)
	if err != nil {
		return err
	}
	return installAddons(ctx, sorted, cfg)
}

// addonConfig builds what addons need to reach the cluster and its container.
func addonConfig(kubeconfig []byte, rt *runtime.Runtime, logger *slog.Logger) (*addons.Config, error) {
	config, err := clientcmd.RESTConfigFromKubeConfig(kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("building rest config: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("creating k8s client: %w", err)
	}

	dynClient, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("creating dynamic client: %w", err)
	}

	ingressPort, ingressErr := ingressHTTPPort(rt)
	cfg := &addons.Config{
		Kubeconfig:      kubeconfig,
		DynamicClient:   dynClient,
		Clientset:       clientset,
		Runtime:         rt,
		Logger:          logger,
		Container:       containerName,
		IngressHost:     hostname,
		IngressHTTPPort: ingressPort,
		IngressErr:      ingressErr,
		LoadConsolePlugin: func(ctx context.Context, name string) error {
			return loadConsolePlugin(ctx, rt, kubeconfig, name, logger)
		},
	}
	// addons that install OpenShift API types need the matching branch
	if info, err := rt.InspectContainer(containerName); err == nil {
		if ver, ok := version.ResolveFromImage(info.Image); ok {
			cfg.OpenShiftVersion = ver.Minor
			cfg.OpenShiftAPIBranch = ver.APIBranch
		}
	}
	return cfg, nil
}

// installAddons installs addons in the order given, once every one of them
// accepts the cluster.
func installAddons(ctx context.Context, sorted []addons.Addon, cfg *addons.Config) error {
	if err := addons.ValidateCluster(ctx, sorted, cfg); err != nil {
		return err
	}

	logger := cfg.Logger
	for _, a := range sorted {
		logger.Info("installing addon", "addon", a.Name())
		if err := a.Install(ctx, cfg); err != nil {
			return fmt.Errorf("installing %s: %w", a.Name(), err)
		}
		logger.Info("waiting for addon readiness", "addon", a.Name())
		if err := a.Ready(ctx, cfg); err != nil {
			return fmt.Errorf("%s not ready: %w", a.Name(), err)
		}
		logger.Info("addon ready", "addon", a.Name())
	}

	return nil
}

// AddonInstallSteps returns the addon install flow as discrete steps for the TUI.
// Already-installed addons (detected via status) are skipped.
func AddonInstallSteps(ctx context.Context, addonList string, kc []byte, rt *runtime.Runtime, runtimeOverride string) ([]*tui.Step, error) {
	sorted, err := resolveAddons(addonList)
	if err != nil {
		return nil, err
	}

	// detect what's already installed so we can skip
	installed := map[string]bool{}
	s := GetStatus(runtimeOverride)
	for _, a := range s.Addons {
		if a.Ready {
			installed[a.Name] = true
		}
	}

	cfg, err := addonConfig(kc, rt, slog.New(slog.NewTextHandler(devNull{}, nil)))
	if err != nil {
		return nil, err
	}
	if err := addons.ValidateCluster(ctx, sorted, cfg); err != nil {
		return nil, err
	}

	var steps []*tui.Step
	for _, a := range sorted {
		a := a
		// skip ready addons, unless options were set for them this
		// invocation: readiness only proves the base install, and the
		// idempotent re-install is what applies the options
		if installed[a.Name()] && !addons.HasOptions(a.Name()) {
			continue
		}
		step := &tui.Step{Name: a.Name()}
		step.Run = func() error {
			step.SetStatus("installing")
			if err := a.Install(ctx, cfg); err != nil {
				return fmt.Errorf("installing: %w", err)
			}
			step.SetStatus("waiting for readiness")
			return a.Ready(ctx, cfg)
		}
		steps = append(steps, step)
	}

	return steps, nil
}

func PreflightVersion(ver version.OCPVersion) error {
	if !ver.RequiresPullSecret {
		return nil
	}
	_, err := pullsecret.RequiredPath()
	return err
}

// configurePullSecret mounts the stored pull secret read-only where CRI-O
// looks for it, when the version pulls its own images with it or an addon in
// the request needs it. every other cluster gets no credentials.
func configurePullSecret(opts *runtime.ContainerOpts, ver version.OCPVersion, addonsNeedIt bool) error {
	var path string
	var err error
	switch {
	case ver.RequiresPullSecret:
		path, err = pullsecret.RequiredPath()
	case addonsNeedIt:
		// the addon's own pre-flight checks for its registry's credentials
		if _, err = pullsecret.Load(); err == nil {
			path, err = pullsecret.Path()
		}
	default:
		return nil
	}
	if err != nil {
		return err
	}
	opts.Volumes = append(opts.Volumes, path+":/etc/crio/openshift-pull-secret:ro")
	return nil
}

// clusterContainerOpts describes the cluster container for a version, with
// what the requested addons need from it.
func clusterContainerOpts(opts CreateOpts, ver version.OCPVersion, requested []addons.Addon) (runtime.ContainerOpts, error) {
	copts := runtime.ContainerOpts{
		Name:             containerName,
		Image:            ver.MicroShiftImage(),
		Hostname:         hostname,
		Privileged:       true,
		NestedContainers: true,
		Platform:         ver.Platform(),
		Labels:           map[string]string{labelKey: containerName},
		Ports: []runtime.PortMapping{
			{Host: opts.HTTPPort, Container: 80},
			{Host: opts.HTTPSPort, Container: 443},
			{Host: 6443, Container: 6443},
		},
	}
	if err := configurePullSecret(&copts, ver, addons.NeedPullSecret(requested)); err != nil {
		return runtime.ContainerOpts{}, err
	}
	return copts, nil
}

func checkExistingVersion(rt *runtime.Runtime, ver version.OCPVersion) error {
	info, err := rt.InspectContainer(containerName)
	if err != nil {
		return err
	}
	if info.Image != ver.MicroShiftImage() {
		return fmt.Errorf("existing cluster uses %s; use oinc switch %s to recreate it", info.Image, ver.Version)
	}
	return nil
}
