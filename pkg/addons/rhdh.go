package addons

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

const (
	defaultRHDHChartVersion = "6.2.2"
	// image line paired with the default chart version. the chart carries no
	// appVersion and defaults its image to the "next" nightly, so the pairing
	// lives here for determinism.
	defaultRHDHImage     = "quay.io/rhdh-community/rhdh:1.10"
	rhdhChartRepoURL     = "https://redhat-developer.github.io/rhdh-chart/"
	rhdhNamespace        = "rhdh"
	rhdhDeployment       = "rhdh-developer-hub"
	rhdhQuickstartPlugin = "./dynamic-plugins/dist/red-hat-developer-hub-backstage-plugin-quickstart"
)

func init() { Register(&rhdh{}) }

type rhdh struct {
	version           string
	image             string
	valuesFile        string
	disableQuickstart bool
}

func (r *rhdh) Name() string           { return "rhdh" }
func (r *rhdh) Dependencies() []string { return nil }

func (r *rhdh) SetOptions(opts map[string]string) {
	if v, ok := opts["version"]; ok {
		r.version = v
	}
	if v, ok := opts["image"]; ok {
		r.image = v
	}
	if v, ok := opts["values"]; ok {
		r.valuesFile = v
	}
	if v, ok := opts["disable-quickstart"]; ok {
		r.disableQuickstart = v == "true"
	}
}

func (r *rhdh) resolveVersion() string {
	if r.version != "" {
		return r.version
	}
	return defaultRHDHChartVersion
}

// chartVersionArgs pins the chart unless version is "latest", which follows
// the chart index.
func (r *rhdh) chartVersionArgs() []string {
	v := r.resolveVersion()
	if v == "latest" {
		return nil
	}
	return []string{"--version", v}
}

// splitImageRef splits an image ref into repository and tag. The tag is the
// part after the last colon, unless that colon precedes a slash (registry
// port). Missing tag defaults to latest.
func splitImageRef(ref string) (string, string) {
	if i := strings.LastIndex(ref, ":"); i >= 0 && !strings.Contains(ref[i:], "/") {
		return ref[:i], ref[i+1:]
	}
	return ref, "latest"
}

// rhdhBaseURL derives the route hostname and external URL from the cluster
// ingress mapping. RHDH bakes its external URL into app config, so the mapped
// host port must be known up front.
func rhdhBaseURL(cfg *Config) (string, string, error) {
	if cfg.IngressHost == "" || cfg.IngressHTTPPort == 0 {
		if cfg.IngressErr != nil {
			return "", "", fmt.Errorf("cannot determine the host port mapped to ingress port 80: %w", cfg.IngressErr)
		}
		return "", "", fmt.Errorf("cannot determine the host port mapped to ingress port 80")
	}
	host := "rhdh." + cfg.IngressHost
	if cfg.IngressHTTPPort == 80 {
		return host, "http://" + host, nil
	}
	return host, fmt.Sprintf("http://%s:%d", host, cfg.IngressHTTPPort), nil
}

// renderValues composes the base helm values for the rhdh/backstage chart.
//
// microshift specifics baked in:
//   - the chart defaults dynamic-plugins-root to an ephemeral 5Gi PVC;
//     microshift has no provisioner, so it becomes an emptyDir. helm replaces
//     extraVolumes wholesale, so the full seven-volume set the
//     install-dynamic-plugins initContainer and main container mount is
//     re-declared here or the Deployment is rejected with orphan volumeMounts.
//   - postgres persistence off (emptyDir) with a sane ephemeral-storage limit.
//
// exposure: the chart's Route is enabled with TLS off and an explicit host on
// the cluster ingress, so the app is reachable on the HTTP port oinc already
// maps without port-forwarding.
func (r *rhdh) renderValues(host, baseURL string) string {
	plugins := " []"
	if r.disableQuickstart {
		// the quickstart onboarding overlay renders a persistent progressbar
		// that breaks e2e page-ready waits
		plugins = fmt.Sprintf("\n      - package: %s\n        disabled: true", rhdhQuickstartPlugin)
	}

	// explicit override wins; the default chart version gets its paired image
	// line; other chart pins and latest keep the chart's own image defaults
	imageRef := r.image
	if imageRef == "" && r.resolveVersion() == defaultRHDHChartVersion {
		imageRef = defaultRHDHImage
	}
	image := ""
	if imageRef != "" {
		repo, tag := splitImageRef(imageRef)
		// registry stays empty so localhost/ sideloaded refs resolve as given;
		// tag quoted so numeric-looking tags like 1.10 stay strings
		image = fmt.Sprintf(`
    image:
      registry: ""
      repository: %q
      tag: %q
      pullPolicy: IfNotPresent`, repo, tag)
	}

	return fmt.Sprintf(`global:
  host: %[1]s
  dynamic:
    includes:
      - dynamic-plugins.default.yaml
    plugins:%[3]s
route:
  tls:
    enabled: false
upstream:
  backstage:%[4]s
    appConfig:
      app:
        baseUrl: %[2]s
      backend:
        baseUrl: %[2]s
        cors:
          origin: %[2]s
      auth:
        environment: development
        providers:
          guest:
            dangerouslyAllowOutsideDevelopment: true
            userEntityRef: user:default/guest
    extraVolumes:
      - name: dynamic-plugins-root
        emptyDir: {}
      - name: dynamic-plugins
        configMap:
          defaultMode: 420
          name: rhdh-dynamic-plugins
          optional: true
      - name: dynamic-plugins-npmrc
        secret:
          defaultMode: 420
          optional: true
          secretName: rhdh-dynamic-plugins-npmrc
      - name: dynamic-plugins-registry-auth
        secret:
          defaultMode: 416
          optional: true
          secretName: rhdh-dynamic-plugins-registry-auth
      - name: npmcacache
        emptyDir: {}
      - name: extensions-catalog
        emptyDir: {}
      - name: temp
        emptyDir: {}
  postgresql:
    primary:
      persistence:
        enabled: false
      resources:
        limits:
          ephemeral-storage: 2Gi
`, host, baseURL, plugins, image)
}

// helmArgs builds the chart install invocation. the user overlay must stay
// the final --values: helm value files merge left to right and user-wins
// semantics depend on that ordering.
func (r *rhdh) helmArgs(baseValues string) []string {
	args := []string{"upgrade", "--install", "rhdh", "rhdh/backstage",
		"--create-namespace",
		"-n", rhdhNamespace,
		"--values", baseValues,
	}
	if r.valuesFile != "" {
		args = append(args, "--values", r.valuesFile)
	}
	args = append(args, r.chartVersionArgs()...)
	return append(args, "--wait", "--timeout", "15m")
}

func (r *rhdh) Install(ctx context.Context, cfg *Config) error {
	if _, err := exec.LookPath("helm"); err != nil {
		return fmt.Errorf("rhdh addon requires helm: %w", err)
	}

	if r.valuesFile != "" {
		if _, err := os.Stat(r.valuesFile); err != nil {
			return fmt.Errorf("rhdh values overlay: %w", err)
		}
	}

	host, baseURL, err := rhdhBaseURL(cfg)
	if err != nil {
		return err
	}

	values, err := os.CreateTemp("", "oinc-rhdh-values-*.yaml")
	if err != nil {
		return fmt.Errorf("creating temp values file: %w", err)
	}
	defer os.Remove(values.Name())
	if _, err := values.WriteString(r.renderValues(host, baseURL)); err != nil {
		values.Close()
		return fmt.Errorf("writing temp values file: %w", err)
	}
	values.Close()

	cfg.Logger.Info("installing rhdh via helm", "version", r.resolveVersion(), "url", baseURL)

	out, err := exec.CommandContext(ctx, "helm", "repo", "add", "rhdh",
		rhdhChartRepoURL, "--force-update",
	).CombinedOutput()
	if err != nil {
		return fmt.Errorf("helm repo add: %s: %w", string(out), err)
	}

	out, err = exec.CommandContext(ctx, "helm", "repo", "update", "rhdh").CombinedOutput()
	if err != nil {
		return fmt.Errorf("helm repo update: %s: %w", string(out), err)
	}

	out, err = exec.CommandContext(ctx, "helm", r.helmArgs(values.Name())...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("helm install rhdh: %s: %w", string(out), err)
	}

	cfg.Logger.Info("rhdh installed", "url", baseURL)
	return nil
}

func (r *rhdh) Ready(ctx context.Context, cfg *Config) error {
	return waitForDeployment(ctx, cfg, rhdhNamespace, rhdhDeployment, 10*time.Minute)
}
