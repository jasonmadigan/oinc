# oinc

```
  ^..^
 ( oo )  oinc ~ OKD in a container
  (..)
```

MicroShift under the hood, with Console, OLM, and ConsolePlugin CRD out of the box.

> [!WARNING]
> This project is largely the product of coffee and vibe coding. It works, but set your expectations accordingly.

```
oinc create
```

That's it. You get a single-node cluster with the OpenShift Console on `localhost:9000`, OLM running, and the ConsolePlugin CRD available -- close enough to real OCP for local dev work.

## Features

- **Auto-detects container runtime** (docker, podman) -- no flags needed
- **Version selection** -- `oinc create --version 4.20` for a fresh cluster; `oinc switch 4.20` recreates an existing cluster
- **Console included** -- OpenShift Console runs as a sidecar, no separate setup
- **OLM included** -- baked into the image; operator installation requires a compatible catalogue and operator
- **Addon system** -- layer on Gateway API, cert-manager, MetalLB, Istio, Kuadrant as needed
- **Console plugin support** -- `--console-plugin "my-plugin=http://localhost:9001"` for plugin dev
- **Interactive TUI** -- step-by-step progress with spinners, interactive addon picker, live status dashboard

## Supported versions

| OCP | OKD component payload | Console | Architectures |
|-|-|-|-|
| 5.0 (default) | 5.0.0-okd-scos.0 | 5.0.0-okd-scos.0 payload | amd64, arm64 |
| 4.22 (pre-release) | 4.22.0-okd-scos.ec.16 | 4.22 | amd64, arm64 |
| 4.21 (pre-release) | 4.21.0-okd-scos.ec.15 | 4.21 | amd64, arm64 |
| 4.20 | 4.20.0-okd-scos.16 | 4.20 | amd64, arm64 |

All entries use **OKD MicroShift** builds. The OCP minor identifies the matching Console and API branch; it does not select a Red Hat OCP payload.

The 5.0 image builds MicroShift from a pinned `release-5.0` commit (the upstream rc2 source), with Kubernetes 1.36.3, CRI-O 5.0 RPMs and OKD 5.0 payload images. This is a community build, not a Red Hat MicroShift GA binary. Its `-oinc.1` image revision replaces the earlier 5.1-based experiment; the CLI excludes the superseded image from remote selection. See [release inputs](images/5.0.json) and [build details](docs/images.md#release-aligned-source-builds).

The 5.0 replacement images are published for amd64 and arm64. Native amd64 Docker/Podman smoke tests and local native ARM checks passed on 29 September 2026.

Up to 4.22 the Console is the amd64-only `origin-console` image, emulated on ARM. 5.0's Console cannot run under that emulation, so ARM hosts get a native build of the same source. See [Console images](docs/images.md#console-images).

The default is the newest stable OKD payload pin in the catalogue. Payload tags containing `ec` or `rc` require an explicit selection; this classification does not describe the MicroShift source's release status. To follow published builds:

```bash
oinc version list --remote                  # published oinc images and architectures
oinc create --version 4@latest              # newest stable OKD build in supported 4.x minors
oinc create --version @latest               # newest stable OKD build in any supported minor
oinc create --version 5.0@next               # opt into 5.0 builds, including ec/rc
oinc create --version 5.0.0-okd-scos.0        # exact OKD payload
```

`@latest` excludes all `ec` and `rc` tags, including those under 4.x. `@next` includes them. These selectors query oinc's GHCR images for the host architecture; new upstream RPMs must first be built into an oinc image. They resolve once at create/switch time and do not update an existing cluster. See [version management](docs/versions.md).

The opt-in Red Hat MicroShift 4.23 preview is available as `oinc create --version ocp-4.23` after `oinc pull-secret set <path>`. It requires registry credentials and uses community networking. See [4.23 setup and build inputs](docs/ocp-4.23.md).

Use `/add-version` in Claude Code to add a new version, or see [docs/images.md](docs/images.md) for the manual process.

## Install

Download a binary from [releases](https://github.com/jasonmadigan/oinc/releases):

```bash
# macOS (Apple Silicon)
curl -L https://github.com/jasonmadigan/oinc/releases/latest/download/oinc-darwin-arm64 -o oinc
chmod +x oinc && sudo mv oinc /usr/local/bin/

# macOS (Intel)
curl -L https://github.com/jasonmadigan/oinc/releases/latest/download/oinc-darwin-amd64 -o oinc
chmod +x oinc && sudo mv oinc /usr/local/bin/

# Linux (amd64)
curl -L https://github.com/jasonmadigan/oinc/releases/latest/download/oinc-linux-amd64 -o oinc
chmod +x oinc && sudo mv oinc /usr/local/bin/
```

Or with Go:

```bash
go install github.com/jasonmadigan/oinc/cmd/oinc@latest
```

## Quick start

```bash
# create cluster (stable catalogue default, auto-detect runtime)
oinc create

# create with a specific version
oinc create --version 4.20

# create with addons
oinc create --addons gateway-api,cert-manager

# addon version pinning
oinc create --addons cert-manager@1.16.0,metallb@0.14.8

# wire in a console plugin dev server
oinc create --console-plugin "my-plugin=http://host.docker.internal:9001"

# after the plugin operator creates a ConsolePlugin with service proxies,
# restart the standalone dev Console with those proxies
oinc console sync-plugin-proxy my-plugin \
  --console-plugin "my-plugin=http://host.docker.internal:9001"

# cluster status
oinc status

# interactive status dashboard
oinc status --watch

# fetch/refresh kubeconfig
oinc kubeconfig

# load a locally built image into the cluster
oinc load-image localhost/my-image:dev

# switch OCP version (delete + create)
oinc switch 4.20

# list available versions
oinc version list

# tear down
oinc delete
```

## CLI

Commands show styled progress in a terminal (spinners, checkmarks, boxed output) and fall back to plain log output when piped or in CI.

- `oinc create` -- step-by-step progress with a summary of endpoints on completion
- `oinc delete` -- confirmation prompt (skip with `--force`)
- `oinc status` -- boxed endpoint and addon status; `--watch` for a live-updating dashboard with pod listing
- `oinc addon install` -- interactive picker when run with no arguments; shows installed addons as checked
- `oinc addon install kuadrant` -- step progress with live sub-status per addon
- `oinc status -o json` -- machine-readable output for scripting

## Addons

The base cluster includes MicroShift + OLM + Console + ConsolePlugin CRD. Addons layer extra infrastructure on top:

| Addon          | What it provides                          | Install method               |
| -------------- | ----------------------------------------- | ---------------------------- |
| `gateway-api`  | Kubernetes Gateway API CRDs               | upstream CRDs (k8s client)   |
| `cert-manager` | Certificate management                    | upstream manifests (kubectl) |
| `metallb`      | LoadBalancer IP allocation                | upstream manifests (kubectl) |
| `istio`        | Istio service mesh via Sail operator      | helm                         |
| `kuadrant`     | API management (rate limiting, auth, DNS) | helm                         |
| `rhdh`         | Red Hat Developer Hub (Backstage)         | helm                         |
| `mcp-gateway`  | MCP Gateway (AI tool gateway)             | helm (OCI)                   |

Dependencies are resolved automatically. Installing `kuadrant` will pull in `gateway-api`, `cert-manager`, `metallb`, and `istio`. Installing `mcp-gateway` will pull in `kuadrant` and all its dependencies.

With recent `kuadrant@latest` builds, `mcp-gateway` reuses Kuadrant's bundled MCP controller and CRDs and configures the gateway instance. Older Kuadrant releases get the standalone Helm install. See [MCP Gateway addon details](docs/addons.md#mcp-gateway) for values overlays and recovery from a duplicate CRD install.

MCP Gateway readiness requires a programmed Gateway with an address; include `--metallb-address-pool auto` unless a pool already exists. Consumer-created Istio Gateways need a Service overlay to opt into scoped MetalLB. See the [example and migration guidance](docs/addons.md#consumer-created-istio-gateways) when upgrading from v0.4.3.

```bash
# at create time
oinc create --addons kuadrant

# or post-hoc (interactive picker)
oinc addon install

# or specify directly
oinc addon install gateway-api
oinc addon list
```

Pin addon versions with `@`:

```bash
oinc addon install cert-manager@1.16.0
```

### Instance options

Opt-in flags make the addons create the instances a working cluster needs, on top of the operators and CRDs they already install:

```bash
# --kuadrant-devportal: enable the developer portal on the Kuadrant CR
# --metallb-address-pool: IPAddressPool + L2Advertisement (auto-derived range)
# --gateway-api-gateway: default Gateway (kuadrant-ingressgateway, istio class), waits for Programmed
oinc create --addons kuadrant@latest \
  --kuadrant-devportal \
  --metallb-address-pool auto \
  --gateway-api-gateway
```

`--metallb-address-pool` also takes an explicit range (`172.17.0.200-172.17.0.220`) or CIDR. Defaults are unchanged: without the flags the addons behave exactly as before. See [docs/addons.md](docs/addons.md) for the mechanics (portal field verification, gateway address via the class-scoped metallb).

### RHDH

The `rhdh` addon installs Red Hat Developer Hub with guest auth enabled and exposes it via a Route on the ingress HTTP port. With default ports it is reachable at `http://rhdh.127.0.0.1.nip.io:9080` (no port-forward needed).

```bash
oinc create --addons rhdh

# pin the chart version (rhdh@latest follows the chart index)
oinc create --addons rhdh@6.2.2

# custom image (e.g. sideloaded via oinc load-image), values overlay, quickstart off
oinc create --addons rhdh \
  --rhdh-image localhost/my-rhdh:dev \
  --rhdh-values overlay.yaml \
  --rhdh-disable-quickstart
```

`--rhdh-values` merges a helm values overlay into the chart install, for dynamic-plugins config and app-config extras. See [docs/addons.md](docs/addons.md) for the full option reference and the MicroShift-specific overrides the addon applies.

## Kubeconfig

`oinc create` automatically merges the cluster kubeconfig into `~/.kube/config` with context name `oinc`. If you need to refresh it:

```bash
# merge into ~/.kube/config
oinc kubeconfig

# print raw kubeconfig to stdout
oinc kubeconfig --print

# switch to oinc context
kubectl config use-context oinc
```

## Loading local images

`oinc load-image` streams a locally built image into the cluster's CRI-O store, the `kind load docker-image` equivalent. Pods can then use it with `imagePullPolicy: IfNotPresent` and no registry.

```bash
docker build -t localhost/my-image:dev .
oinc load-image localhost/my-image:dev
```

The ref is preserved exactly, so `localhost/<name>:<tag>` refs resolve as given. Re-running with the same ref succeeds. Works with docker or podman as the host runtime; the command picks whichever one owns the running cluster.

## Console plugin service proxies

On a real OpenShift cluster, the Console operator translates a dynamic plugin's
`ConsolePlugin.spec.proxy` entries into Bridge configuration. OINC runs its
Console as a standalone development container, so it does not have that
reconciliation layer.

After installing or rebuilding an operator that reconciles service proxies, run:

```bash
oinc console sync-plugin-proxy <ConsolePlugin-name> \
  --console-plugin "<plugin-name>=http://host.docker.internal:<dev-server-port>"
```

The command reads `ConsolePlugin.spec.proxy`, creates OINC-only LoadBalancer
shadow Services so the Console container can reach the in-cluster backends,
mounts the OpenShift service CA, and restarts the standalone Console. It does
not change the product deployment contract: the plugin operator remains the
source of truth for the backend Service and `ConsolePlugin` resource.
Reload the browser after the Console restarts.

## Ports

| Port   | Service                             |
| ------ | ----------------------------------- |
| `6443` | Kubernetes API server               |
| `9000` | OpenShift Console                   |
| `9080` | OpenShift Route HTTP                |
| `9443` | OpenShift Route HTTPS               |

## Requirements

- Docker or Podman
- ~4GB RAM available for the container
- `curl` (for fetching upstream manifests and CRDs)
- `kubectl` (for cert-manager and metallb addons)
- `helm` (for istio, kuadrant, rhdh and mcp-gateway addons)

## Acknowledgements

oinc builds on the work of several projects:

- [MicroShift](https://github.com/openshift/microshift) -- the lightweight OpenShift runtime that powers the cluster
- [microshift-io](https://github.com/microshift-io/microshift) -- OKD-flavoured MicroShift builds and pre-built RPMs
- [OKD](https://www.okd.io/) -- the community distribution of Kubernetes that powers OpenShift
- [OpenShift Console](https://github.com/openshift/console) -- the web UI
- [minc](https://github.com/minc-org/minc) -- inspiration for running MicroShift in a container
