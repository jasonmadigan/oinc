package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/jasonmadigan/oinc/pkg/addons"
	"github.com/jasonmadigan/oinc/pkg/kubeconfig"
	"github.com/jasonmadigan/oinc/pkg/oinc"
	"github.com/jasonmadigan/oinc/pkg/runtime"
	"github.com/jasonmadigan/oinc/pkg/tui"
	"github.com/jasonmadigan/oinc/pkg/version"
	"github.com/spf13/cobra"
)

var (
	flagRuntime         string
	flagVersion         string
	flagHTTPPort        int
	flagHTTPSPort       int
	flagConsolePort     int
	flagConsPlugin      string
	flagAddons          string
	flagLogLevel        string
	flagOutput          string
	flagKubeconfigPrint bool
	flagWatch           bool
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
		Long:  tui.Pig("oinc ~ OKD in a container"),
	}

	root.PersistentFlags().StringVar(&flagRuntime, "runtime", "", "container runtime (auto-detected if empty)")
	root.PersistentFlags().StringVarP(&flagLogLevel, "log-level", "l", "info", "log level (debug, info, warn, error)")

	createCmd := &cobra.Command{
		Use:   "create",
		Short: "Create a cluster",
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := newLogger(flagLogLevel)
			return oinc.Create(cmd.Context(), oinc.CreateOpts{
				Version:         flagVersion,
				RuntimeOverride: flagRuntime,
				HTTPPort:        flagHTTPPort,
				HTTPSPort:       flagHTTPSPort,
				ConsolePort:     flagConsolePort,
				ConsolePlugin:   flagConsPlugin,
				Addons:          flagAddons,
			}, logger)
		},
	}
	createCmd.Flags().StringVar(&flagVersion, "version", "", "OCP version (default: latest)")
	createCmd.Flags().IntVar(&flagHTTPPort, "http-port", 9080, "HTTP route port")
	createCmd.Flags().IntVar(&flagHTTPSPort, "https-port", 9443, "HTTPS route port")
	createCmd.Flags().IntVar(&flagConsolePort, "console-port", 9000, "console port")
	createCmd.Flags().StringVar(&flagConsPlugin, "console-plugin", "", "console plugin wiring (name=url)")
	createCmd.Flags().StringVar(&flagAddons, "addons", "", "comma-separated addons to install")

	var flagForce bool
	deleteCmd := &cobra.Command{
		Use:   "delete",
		Short: "Delete the cluster",
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := newLogger(flagLogLevel)
			if !flagForce && tui.IsTTY() {
				ok, err := tui.Confirm("delete the oinc cluster?")
				if err != nil {
					return err
				}
				if !ok {
					return nil
				}
			}
			return oinc.Delete(flagRuntime, logger)
		},
	}
	deleteCmd.Flags().BoolVarP(&flagForce, "force", "f", false, "skip confirmation")

	statusCmd := &cobra.Command{
		Use:   "status",
		Short: "Show cluster status",
		RunE: func(cmd *cobra.Command, args []string) error {
			if flagWatch {
				return runStatusDashboard()
			}

			var s oinc.Status
			if flagOutput != "json" && tui.IsTTY() {
				m := tui.NewLoadingModel("fetching cluster status", func() oinc.Status {
					return oinc.GetStatus(flagRuntime)
				})
				p := tea.NewProgram(m)
				final, err := p.Run()
				if err != nil {
					return err
				}
				s = final.(tui.LoadingModel[oinc.Status]).Result()
			} else {
				s = oinc.GetStatus(flagRuntime)
			}

			if flagOutput == "json" {
				out, err := json.MarshalIndent(s, "", "  ")
				if err != nil {
					return fmt.Errorf("marshalling status: %w", err)
				}
				fmt.Println(string(out))
				return nil
			}
			fmt.Print(s.Render())
			return nil
		},
	}
	statusCmd.Flags().StringVarP(&flagOutput, "output", "o", "", "output format (json)")
	statusCmd.Flags().BoolVarP(&flagWatch, "watch", "w", false, "interactive dashboard with auto-refresh")

	versionCmd := &cobra.Command{
		Use:   "version",
		Short: "Show oinc version",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Print(tui.Pig("oinc " + buildVersion))
		},
	}

	versionListCmd := &cobra.Command{
		Use:   "list",
		Short: "List available OCP versions",
		Run: func(cmd *cobra.Command, args []string) {
			def := version.Default()
			var rows []string
			for _, v := range version.All() {
				marker := ""
				if v.Version == def.Version {
					marker = "  " + tui.Green.Render("[default]")
				}
				rows = append(rows, fmt.Sprintf("  %-6s %s%s",
					v.Version, tui.Dim.Render(fmt.Sprintf("microshift: %s, console: %s", v.MicroShiftTag, v.ConsoleTag)), marker))
			}
			box := tui.Box.Render(strings.Join(rows, "\n"))
			fmt.Println(indent(box, 2))
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
			return oinc.Create(cmd.Context(), oinc.CreateOpts{
				Version:         args[0],
				RuntimeOverride: flagRuntime,
				HTTPPort:        flagHTTPPort,
				HTTPSPort:       flagHTTPSPort,
				ConsolePort:     flagConsolePort,
				ConsolePlugin:   flagConsPlugin,
			}, logger)
		},
	}
	switchCmd.Flags().IntVar(&flagHTTPPort, "http-port", 9080, "HTTP route port")
	switchCmd.Flags().IntVar(&flagHTTPSPort, "https-port", 9443, "HTTPS route port")
	switchCmd.Flags().IntVar(&flagConsolePort, "console-port", 9000, "console port")
	switchCmd.Flags().StringVar(&flagConsPlugin, "console-plugin", "", "console plugin wiring (name=url)")

	addonCmd := &cobra.Command{
		Use:   "addon",
		Short: "Manage addons",
	}

	addonListCmd := &cobra.Command{
		Use:   "list",
		Short: "List available addons",
		Run: func(cmd *cobra.Command, args []string) {
			all := addons.All()
			names := make([]string, 0, len(all))
			for name := range all {
				names = append(names, name)
			}
			sort.Strings(names)
			var rows []string
			for _, name := range names {
				a := all[name]
				deps := a.Dependencies()
				if len(deps) > 0 {
					rows = append(rows, fmt.Sprintf("  %-16s %s", name, tui.Dim.Render("requires: "+strings.Join(deps, ", "))))
				} else {
					rows = append(rows, fmt.Sprintf("  %s", name))
				}
			}
			box := tui.Box.Render(strings.Join(rows, "\n"))
			fmt.Println(indent(box, 2))
		},
	}

	addonInstallCmd := &cobra.Command{
		Use:   "install [addon[,addon...]]",
		Short: "Install addons into a running cluster",
		Args:  cobra.MaximumNArgs(1),
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

			addonArg := ""
			if len(args) > 0 {
				addonArg = args[0]
			}

			// no args + TTY: interactive picker
			if addonArg == "" && tui.IsTTY() {
				selected, err := runAddonPicker(flagRuntime)
				if err != nil {
					return err
				}
				if len(selected) == 0 {
					return nil
				}
				addonArg = strings.Join(selected, ",")
			}

			if addonArg == "" {
				return fmt.Errorf("specify addons to install, or run interactively in a terminal")
			}

			if tui.IsTTY() {
				steps, err := oinc.AddonInstallSteps(cmd.Context(), addonArg, kc, rt, flagRuntime)
				if err != nil {
					return err
				}
				if len(steps) == 0 {
					fmt.Fprintln(os.Stderr, "All requested addons are already installed")
					return nil
				}
				return tui.RunSteps("installing addons", steps)
			}

			return oinc.InstallAddons(cmd.Context(), addonArg, kc, rt, logger)
		},
	}

	addonCmd.AddCommand(addonListCmd, addonInstallCmd)

	kubeconfigCmd := &cobra.Command{
		Use:   "kubeconfig",
		Short: "Fetch cluster kubeconfig",
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := newLogger(flagLogLevel)
			return oinc.Kubeconfig(flagRuntime, flagKubeconfigPrint, logger)
		},
	}
	kubeconfigCmd.Flags().BoolVarP(&flagKubeconfigPrint, "print", "p", false, "print raw kubeconfig to stdout")

	root.AddCommand(createCmd, deleteCmd, statusCmd, versionCmd, switchCmd, addonCmd, kubeconfigCmd)

	// suppress usage on RunE errors -- the TUI already shows what went wrong
	root.SilenceUsage = true

	setStyledHelp(root)

	// give subcommands their own standard help template so they don't inherit root's
	stdTmpl := `Usage:{{if .Runnable}}
  {{.UseLine}}{{end}}{{if .HasAvailableSubCommands}}
  {{.CommandPath}} [command]{{end}}{{if gt (len .Aliases) 0}}

Aliases:
  {{.NameAndAliases}}{{end}}{{if .HasAvailableSubCommands}}

Available Commands:{{range .Commands}}{{if (or .IsAvailableCommand (eq .Name "help"))}}
  {{rpad .Name .NamePadding }} {{.Short}}{{end}}{{end}}{{end}}{{if .HasAvailableLocalFlags}}

Flags:
{{.LocalFlags.FlagUsages | trimTrailingWhitespaces}}{{end}}{{if .HasAvailableInheritedFlags}}

Global Flags:
{{.InheritedFlags.FlagUsages | trimTrailingWhitespaces}}{{end}}{{if .HasExample}}

Examples:
{{.Example}}{{end}}
`
	for _, cmd := range root.Commands() {
		cmd.SetHelpTemplate(stdTmpl)
	}

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

func runAddonPicker(runtimeOverride string) ([]string, error) {
	all := addons.All()
	names := make([]string, 0, len(all))
	for name := range all {
		names = append(names, name)
	}
	sort.Strings(names)

	// check what's already installed via status
	s := oinc.GetStatus(runtimeOverride)
	installed := map[string]bool{}
	for _, a := range s.Addons {
		installed[a.Name] = true
	}

	var items []tui.PickerItem
	for _, name := range names {
		a := all[name]
		hint := ""
		if deps := a.Dependencies(); len(deps) > 0 {
			hint = "requires: " + strings.Join(deps, ", ")
		}
		items = append(items, tui.PickerItem{
			Name:      name,
			Hint:      hint,
			Installed: installed[name],
		})
	}

	m := tui.NewPickerModel("addon install", items)
	p := tea.NewProgram(m)
	final, err := p.Run()
	if err != nil {
		return nil, err
	}
	fm := final.(tui.PickerModel)
	if fm.Aborted() {
		return nil, nil
	}
	return fm.Selected(), nil
}

func runStatusDashboard() error {
	fetch := func() tui.StatusData {
		s := oinc.GetStatus(flagRuntime)
		data := tui.StatusData{
			State:        s.State,
			Runtime:      s.Runtime,
			Version:      s.Version,
			APIServer:    s.APIServer,
			ConsoleURL:   s.ConsoleURL,
			IngressHTTP:  s.IngressHTTP,
			IngressHTTPS: s.IngressHTTPS,
			Uptime:       s.Uptime,
			Error:        s.Error,
		}
		for _, a := range s.Addons {
			data.Addons = append(data.Addons, tui.AddonData{Name: a.Name, Ready: a.Ready})
		}
		for _, p := range oinc.GetPods() {
			data.Pods = append(data.Pods, tui.PodData{
				Name:      p.Name,
				Namespace: p.Namespace,
				Ready:     p.Ready,
				Status:    p.Status,
			})
		}
		return data
	}

	m := tui.NewStatusModel(fetch, 5*time.Second)
	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err := p.Run()
	return err
}

func setStyledHelp(root *cobra.Command) {
	heading := lipgloss.NewStyle().Foreground(lipgloss.Color("#ff69b4")).Bold(true)
	cmdName := lipgloss.NewStyle().Foreground(lipgloss.Color("#22c55e"))

	cobra.AddTemplateFunc("styledHeading", heading.Render)
	cobra.AddTemplateFunc("styledCmd", cmdName.Render)

	root.SetHelpTemplate(tui.Pig("oinc ~ OKD in a container") + `
{{ styledHeading "Commands:" }}
{{- range .Commands }}{{- if (or .IsAvailableCommand (eq .Name "help")) }}
  {{ styledCmd (rpad .Name .NamePadding) }}  {{ .Short }}
{{- end }}{{- end }}

{{ styledHeading "Flags:" }}
{{ .LocalFlags.FlagUsages }}
Use "oinc [command] --help" for more about a command.
`)
}

func indent(s string, n int) string {
	pad := strings.Repeat(" ", n)
	lines := strings.Split(s, "\n")
	for i := range lines {
		if lines[i] != "" {
			lines[i] = pad + lines[i]
		}
	}
	return strings.Join(lines, "\n")
}
