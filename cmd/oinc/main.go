package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/jasonmadigan/oinc/pkg/addons"
	"github.com/jasonmadigan/oinc/pkg/kubeconfig"
	"github.com/jasonmadigan/oinc/pkg/oinc"
	"github.com/jasonmadigan/oinc/pkg/runtime"
	"github.com/jasonmadigan/oinc/pkg/version"
	"github.com/spf13/cobra"
)

var (
	flagRuntime     string
	flagVersion     string
	flagHTTPPort    int
	flagHTTPSPort   int
	flagConsolePort int
	flagOverlay     bool
	flagConsPlugin  string
	flagAddons      string
	flagLogLevel    string
)

var buildVersion = "dev"

func newLogger(level string) *slog.Logger {
	var l slog.Level
	switch strings.ToLower(level) {
	case "debug":
		l = slog.LevelDebug
	case "warn":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		l = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: l}))
}

func main() {
	root := &cobra.Command{
		Use:   "oinc",
		Short: "OKD in a container",
		Long: `  ^..^
 ( oo )  oinc ~ OKD in a container
  (..)`,
	}

	root.PersistentFlags().StringVar(&flagRuntime, "runtime", "", "container runtime (auto-detected if empty)")
	root.PersistentFlags().StringVarP(&flagLogLevel, "log-level", "l", "info", "log level (debug, info, warn, error)")

	createCmd := &cobra.Command{
		Use:   "create",
		Short: "Create a cluster",
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := newLogger(flagLogLevel)
			return oinc.Create(oinc.CreateOpts{
				Version:             flagVersion,
				RuntimeOverride:     flagRuntime,
				HTTPPort:            flagHTTPPort,
				HTTPSPort:           flagHTTPSPort,
				ConsolePort:         flagConsolePort,
				DisableOverlayCache: flagOverlay,
				ConsolePlugin:       flagConsPlugin,
				Addons:              flagAddons,
			}, logger)
		},
	}
	createCmd.Flags().StringVar(&flagVersion, "version", "", "OCP version (default: latest)")
	createCmd.Flags().IntVar(&flagHTTPPort, "http-port", 9080, "HTTP route port")
	createCmd.Flags().IntVar(&flagHTTPSPort, "https-port", 9443, "HTTPS route port")
	createCmd.Flags().IntVar(&flagConsolePort, "console-port", 9000, "console port")
	createCmd.Flags().BoolVar(&flagOverlay, "disable-overlay-cache", false, "use named volume instead of host bind mount")
	createCmd.Flags().StringVar(&flagConsPlugin, "console-plugin", "", "console plugin wiring (name=url)")
	createCmd.Flags().StringVar(&flagAddons, "addons", "", "comma-separated addons to install")

	deleteCmd := &cobra.Command{
		Use:   "delete",
		Short: "Delete the cluster",
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := newLogger(flagLogLevel)
			return oinc.Delete(flagRuntime, logger)
		},
	}

	statusCmd := &cobra.Command{
		Use:   "status",
		Short: "Show cluster status",
		RunE: func(cmd *cobra.Command, args []string) error {
			s := oinc.GetStatus(flagRuntime)
			out, _ := json.MarshalIndent(s, "", "  ")
			fmt.Println(string(out))
			return nil
		},
	}

	versionCmd := &cobra.Command{
		Use:   "version",
		Short: "Show oinc version",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("  ^..^\n ( oo )  oinc %s\n  (..)\n", buildVersion)
		},
	}

	versionListCmd := &cobra.Command{
		Use:   "list",
		Short: "List available OCP versions",
		Run: func(cmd *cobra.Command, args []string) {
			def := version.Default()
			for _, v := range version.All() {
				marker := ""
				if v.Version == def.Version {
					marker = "  [default]"
				}
				fmt.Printf("  %s  (microshift: %s, console: %s)%s\n",
					v.Version, v.MicroShiftTag, v.ConsoleTag, marker)
			}
		},
	}
	versionCmd.AddCommand(versionListCmd)

	switchCmd := &cobra.Command{
		Use:   "switch <version>",
		Short: "Switch to a different OCP version (delete + create)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := newLogger(flagLogLevel)
			logger.Info("switching version", "version", args[0])
			if err := oinc.Delete(flagRuntime, logger); err != nil {
				logger.Warn("delete failed, continuing", "err", err)
			}
			return oinc.Create(oinc.CreateOpts{
				Version:             args[0],
				RuntimeOverride:     flagRuntime,
				HTTPPort:            flagHTTPPort,
				HTTPSPort:           flagHTTPSPort,
				ConsolePort:         flagConsolePort,
				DisableOverlayCache: flagOverlay,
				ConsolePlugin:       flagConsPlugin,
			}, logger)
		},
	}
	switchCmd.Flags().IntVar(&flagHTTPPort, "http-port", 9080, "HTTP route port")
	switchCmd.Flags().IntVar(&flagHTTPSPort, "https-port", 9443, "HTTPS route port")
	switchCmd.Flags().IntVar(&flagConsolePort, "console-port", 9000, "console port")
	switchCmd.Flags().BoolVar(&flagOverlay, "disable-overlay-cache", false, "use named volume instead of host bind mount")
	switchCmd.Flags().StringVar(&flagConsPlugin, "console-plugin", "", "console plugin wiring (name=url)")

	addonCmd := &cobra.Command{
		Use:   "addon",
		Short: "Manage addons",
	}

	addonListCmd := &cobra.Command{
		Use:   "list",
		Short: "List available addons",
		Run: func(cmd *cobra.Command, args []string) {
			for name := range addons.All() {
				fmt.Printf("  %s\n", name)
			}
		},
	}

	addonInstallCmd := &cobra.Command{
		Use:   "install <addon>[,<addon>...]",
		Short: "Install addons into a running cluster",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := newLogger(flagLogLevel)

			rt, err := runtime.Detect(flagRuntime)
			if err != nil {
				return err
			}

			kc, err := kubeconfig.Read()
			if err != nil {
				return fmt.Errorf("reading kubeconfig: %w", err)
			}

			return oinc.InstallAddons(args[0], kc, rt, logger)
		},
	}

	addonCmd.AddCommand(addonListCmd, addonInstallCmd)

	root.AddCommand(createCmd, deleteCmd, statusCmd, versionCmd, switchCmd, addonCmd)

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}
