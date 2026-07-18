# Addons

## Overview

The base oinc cluster includes MicroShift + OLM + Console + ConsolePlugin CRD. Addons layer extra infrastructure on top for specific use cases.

## Available addons

| Addon | Default version | Install method | Dependencies |
|-|-|-|-|
| `gateway-api` | 1.2.1 | upstream CRD manifests | none (istio, metallb with `--gateway-api-gateway`) |
| `cert-manager` | 1.17.1 | upstream manifests | none |
| `metallb` | 0.14.9 | upstream manifests | none |
| `istio` | 1.29.0 (sail) | helm (sail operator) | none |
| `kuadrant` | 1.4.1 | helm | gateway-api, cert-manager, metallb, istio |
| `rhdh` | 6.2.2 (chart) | helm | none |

## Install methods

### Upstream manifests (gateway-api, cert-manager, metallb)

Downloaded via curl and applied via `kubectl apply --server-side --force-conflicts`. Curl is used instead of Go's `net/http` because Go's network stack breaks inside privileged containers on macOS.

For gateway-api specifically, CRDs are applied via the dynamic k8s client directly (not kubectl) since it only needs to handle CRD resources.

### Helm (istio, kuadrant, rhdh)

Uses `helm upgrade --install` for idempotency. Helm must be available in `$PATH`.

- **Istio**: installs the Sail operator from a GitHub release tarball, then creates an `Istio` CR in the `Ready` phase
- **Kuadrant**: adds the `kuadrant.io` helm repo, installs the operator, then creates a `Kuadrant` CR and waits for it to become ready
- **RHDH**: adds the `rhdh` helm repo and installs the `rhdh/backstage` chart into the `rhdh` namespace (see below)

## RHDH

Installs Red Hat Developer Hub with guest auth enabled, exposed via an OpenShift Route on the HTTP port oinc already maps. With default ports the app is reachable at `http://rhdh.127.0.0.1.nip.io:9080` (guest sign-in, no port-forward needed).

The version syntax pins the chart from https://redhat-developer.github.io/rhdh-chart/: `rhdh@6.2.2` installs chart 6.2.2 (which the addon pairs with the `rhdh:1.10` image line, since the chart's own default image is the `next` nightly), `rhdh@latest` follows the chart index. The chart carries no appVersion and is image-agnostic.

### Options

Configured via flags on `oinc create` and `oinc addon install`:

| Flag | Effect |
|-|-|
| `--rhdh-image repo:tag` | custom RHDH image. Rendered with `registry: ""` so `localhost/` refs sideloaded via `oinc load-image` resolve, and `pullPolicy: IfNotPresent` |
| `--rhdh-values file` | helm values overlay merged into the chart install, for dynamic-plugins config and app-config extras |
| `--rhdh-disable-quickstart` | disables the quickstart onboarding plugin (its persistent progressbar breaks e2e page-ready waits) |

The overlay is passed to helm after the addon's base values, so it wins on conflicts. Helm merges maps per-key but replaces lists wholesale: an overlay that sets `global.dynamic.plugins` owns the whole list, including the quickstart entry added by `--rhdh-disable-quickstart`, and one that sets `upstream.backstage.extraVolumes` must re-declare the full volume set described below.

### MicroShift quirks the addon owns

- The chart defaults `dynamic-plugins-root` to an ephemeral 5Gi PVC; MicroShift has no storage provisioner, so it is overridden to an `emptyDir`. Because helm replaces the `extraVolumes` list wholesale, the addon re-declares the full seven-volume set the `install-dynamic-plugins` initContainer and main container mount (`dynamic-plugins-root`, `dynamic-plugins`, `dynamic-plugins-npmrc`, `dynamic-plugins-registry-auth`, `npmcacache`, `extensions-catalog`, `temp`); dropping any of them gets the Deployment rejected with orphan volumeMounts.
- Postgres persistence is off (emptyDir) with a 2Gi ephemeral-storage limit; the chart default limit of 20Mi assumes a PVC and would evict the pod.
- The Route is created with TLS disabled and an explicit host on the cluster ingress hostname, and the app's `baseUrl`/CORS origin are set to the externally mapped URL (RHDH bakes its external URL into app config).

## Instance options

By default the kuadrant, metallb and gateway-api addons install operators and CRDs but no instances beyond a bare `Kuadrant` CR. Opt-in flags (on `oinc create` and `oinc addon install`) make oinc create the instances a working cluster needs:

| Flag | Effect |
|-|-|
| `--kuadrant-devportal` | sets `spec.components.developerPortal.enabled: true` on the `Kuadrant` CR (created with it, or merge-patched onto an existing CR), then waits for `deployment/developer-portal-controller` in `kuadrant-system` to be Available |
| `--metallb-address-pool VALUE` | creates an `IPAddressPool` (`oinc-pool`) and `L2Advertisement` (`oinc-l2`) in `metallb-system` once the controller is up. `auto` derives an `x.y.z.200-x.y.z.220` range from the cluster container's network subnet; an explicit range (`start-end`) or CIDR is used as given, validated at pre-flight |
| `--gateway-api-gateway` | creates a `Gateway` named `kuadrant-ingressgateway` (istio class, http/80, routes from all namespaces) in `gateway-system` and waits for it to be Programmed with an address |

All three together give a cluster where the developer portal runs and the gateway has a real address:

```bash
oinc create --addons kuadrant@latest \
  --kuadrant-devportal --metallb-address-pool auto --gateway-api-gateway
```

Defaults are unchanged: without the flags the addons behave exactly as before.

Mechanics worth knowing:

- **Portal field verification**: structural CRD pruning silently drops unknown fields, so a write can report success without taking effect. The addon re-reads the CR after writing and fails loud if `spec.components.developerPortal` did not persist, which means the installed kuadrant version predates the field; pin one that ships it (e.g. `kuadrant@latest`).
- **Gateway address via the scoped metallb**: oinc's metallb only manages services with `spec.loadBalancerClass: oinc.io/metallb` (see below), and istio's auto-deployed gateway service would be class-less. The field is immutable after creation, so the addon creates a ConfigMap (`kuadrant-ingressgateway-params`) referenced from the Gateway's `spec.infrastructure.parametersRef`; istio's gateway deployment controller strategic-merges its `service` overlay into the rendered Service before first apply, so the Service is born with the class and the scoped metallb assigns it an address. The metallb scoping itself is untouched.
- **Ordering**: `--gateway-api-gateway` gives the gateway-api addon dependencies on istio and metallb, so the Gateway is only created and waited on once istiod can deploy it and metallb can address it. Pair it with `--metallb-address-pool` (or a pre-existing pool), otherwise the Programmed wait times out.
- **Idempotence**: all instances are create-if-absent; re-running `oinc addon install` with the same flags is a no-op for existing instances.

## Why not OLM?

MicroShift ships OLM, but its bundled version uses an older catalogue format that's incompatible with the FBC (File-Based Catalogue) images from OperatorHub (`quay.io/operatorhubio/catalog:latest`). The Sail operator's own catalogue also requires authentication. Rather than fight these issues, addons use direct manifests or helm.

## Version pinning

Addons accept version overrides via `@` syntax:

```bash
oinc create --addons cert-manager@1.16.0,metallb@0.14.8
oinc addon install istio@1.28.0
```

Versions are parsed by `ParseAddonSpec` in `pkg/addons/addon.go` and passed to the addon via the `Configurable` interface.

## Dependency resolution

Dependencies are declared per-addon and resolved via topological sort (Kahn's algorithm) before installation. If you install `kuadrant`, its dependencies (gateway-api, cert-manager, metallb, istio) are automatically installed first.

## Adding a new addon

1. Create `pkg/addons/<name>.go`
2. Implement the `Addon` interface:
   ```go
   type Addon interface {
       Name() string
       Dependencies() []string
       Install(ctx context.Context, cfg *Config) error
       Ready(ctx context.Context, cfg *Config) error
   }
   ```
3. Optionally implement `Configurable` for version pinning:
   ```go
   type Configurable interface {
       SetOptions(opts map[string]string)
   }
   ```
   and `Validator` for pre-flight config checks, run at the start of `create` (before any container work) and before `addon install`:
   ```go
   type Validator interface {
       Validate() error
   }
   ```
4. Register in `init()`: `func init() { Register(&myAddon{}) }`
5. Add detection in `pkg/oinc/status.go` `addonChecks` for status display

## MicroShift-specific notes

- **SecurityContextConstraints**: OpenShift/MicroShift enforces SCCs. Some addons (metallb) need pods running as specific UIDs or with elevated privileges. The `grantSCC` helper in `metallb.go` adds service accounts to the `privileged` SCC.
- **Namespaces**: some addons create namespaces that already exist in MicroShift, so `ensureNamespace` is used to handle both create and no-op cases.
- **LoadBalancer services**: MicroShift's built-in service-lb binds host ports for LoadBalancer services; that is how Route traffic reaches the mapped ingress ports. The metallb addon is therefore scoped with `--lb-class=oinc.io/metallb` (injected into the manifests before apply) so it only manages services that set `spec.loadBalancerClass: oinc.io/metallb`; existing LoadBalancer services are not managed by metallb unless they set that class. Unscoped, metallb's controller claims class-less services such as `openshift-ingress/router-default` and, having no address pools, clears the status MicroShift wrote, which tears down the host port bindings and kills Route ingress.
