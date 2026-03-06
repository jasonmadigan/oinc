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
	"github.com/jasonmadigan/oinc/pkg/runtime"
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
	Version             string
	RuntimeOverride     string
	HTTPPort            int
	HTTPSPort           int
	ConsolePort         int
	DisableOverlayCache bool
	ConsolePlugin       string // "name=url" for plugin wiring
	Addons              string // comma-separated addon names
}

func Create(opts CreateOpts, logger *slog.Logger) error {
	ver, err := version.Resolve(opts.Version)
	if err != nil {
		return err
	}
	logger.Info("resolved version", "version", ver.Version)

	rt, err := runtime.Detect(opts.RuntimeOverride)
	if err != nil {
		return err
	}
	logger.Info("using runtime", "runtime", rt.Name())

	image := ver.MicroShiftImage()
	logger.Info("pulling image", "image", image)
	if err := rt.PullImage(image); err != nil {
		return fmt.Errorf("pulling image: %w", err)
	}

	if rt.ContainerExists(containerName) {
		logger.Info("container already exists, starting")
	} else {
		logger.Info("creating container")
		copts := runtime.ContainerOpts{
			Name:       containerName,
			Image:      image,
			Hostname:   hostname,
			Privileged: true,
			Labels:     map[string]string{labelKey: containerName},
			Ports: []runtime.PortMapping{
				{Host: opts.HTTPPort, Container: 80},
				{Host: opts.HTTPSPort, Container: 443},
				{Host: 6443, Container: 6443},
			},
		}

		if opts.DisableOverlayCache {
			copts.Volumes = append(copts.Volumes, "oinc-storage:/host-container")
		} else {
			copts.Volumes = append(copts.Volumes, "/var/lib/containers/storage:/host-container:ro,rshared")
		}

		if err := rt.CreateContainer(copts); err != nil {
			return fmt.Errorf("creating container: %w", err)
		}
	}

	if err := rt.StartContainer(containerName); err != nil {
		return fmt.Errorf("starting container: %w", err)
	}

	logger.Info("waiting for microshift service")
	if err := rt.WaitForService(containerName, "microshift", 30, 2*time.Second); err != nil {
		return fmt.Errorf("microshift service: %w", err)
	}

	// cri-o fix: remove read-only host-container store if present
	fixCRIOStorage(rt, containerName, logger)

	logger.Info("fetching kubeconfig")
	kcPath := fmt.Sprintf("%s/%s/kubeconfig", kubeconfigDir, hostname)
	raw, err := rt.CopyFromContainer(containerName, kcPath)
	if err != nil {
		return fmt.Errorf("fetching kubeconfig: %w", err)
	}
	if err := kubeconfig.Update(raw); err != nil {
		return fmt.Errorf("updating kubeconfig: %w", err)
	}

	logger.Info("waiting for pods to be ready")
	if err := cluster.WaitForReady(raw, 60, 5*time.Second); err != nil {
		return fmt.Errorf("cluster readiness: %w", err)
	}

	logger.Info("setting up console")
	if err := setupConsole(rt, raw, ver, opts.ConsolePort, opts.ConsolePlugin, logger); err != nil {
		return fmt.Errorf("console setup: %w", err)
	}

	if opts.Addons != "" {
		if err := InstallAddons(opts.Addons, raw, rt, logger); err != nil {
			return fmt.Errorf("addon installation: %w", err)
		}
	}

	logger.Info("cluster ready")
	return nil
}

func InstallAddons(addonList string, kubeconfig []byte, rt *runtime.Runtime, logger *slog.Logger) error {
	names := strings.Split(addonList, ",")
	for i := range names {
		names[i] = strings.TrimSpace(names[i])
	}

	sorted, err := addons.Resolve(names)
	if err != nil {
		return err
	}

	config, err := clientcmd.RESTConfigFromKubeConfig(kubeconfig)
	if err != nil {
		return fmt.Errorf("building rest config: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("creating k8s client: %w", err)
	}

	dynClient, err := dynamic.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("creating dynamic client: %w", err)
	}

	cfg := &addons.Config{
		Kubeconfig:    kubeconfig,
		DynamicClient: dynClient,
		Clientset:     clientset,
		Runtime:       rt,
		Logger:        logger,
	}

	ctx := context.Background()
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

// fixCRIOStorage removes the read-only /host-container store from CRI-O config
// when the host-container store is mounted read-only and breaks pod scheduling.
func fixCRIOStorage(rt *runtime.Runtime, container string, logger *slog.Logger) {
	out, err := rt.ExecInContainer(container, "grep", "-q", "additionalimagestores.*host-container", "/etc/containers/storage.conf")
	if err != nil {
		return
	}
	_ = out

	logger.Info("fixing CRI-O storage config")
	_, _ = rt.ExecInContainer(container, "sed", "-i", `s|"/host-container"||`, "/etc/containers/storage.conf")
	_, _ = rt.ExecInContainer(container, "systemctl", "restart", "crio")
	// cri-o needs time to stabilise after restart
	time.Sleep(10 * time.Second)
}
