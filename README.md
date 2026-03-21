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
- **Version switching** -- `oinc create --version 4.20` to target a specific OCP release
- **Console included** -- OpenShift Console runs as a sidecar, no separate setup
- **OLM included** -- baked into the image, operator workflows work out of the box
- **Addon system** -- layer on Gateway API, cert-manager, MetalLB, Istio, Kuadrant as needed
- **Console plugin support** -- `--console-plugin "my-plugin=http://localhost:9001"` for plugin dev
- **Interactive TUI** -- step-by-step progress with spinners, interactive addon picker, live status dashboard

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
# create cluster (latest OCP version, auto-detect runtime)
oinc create

# create with a specific version
oinc create --version 4.20

# create with addons
oinc create --addons gateway-api,cert-manager

# addon version pinning
oinc create --addons cert-manager@1.16.0,metallb@0.14.8

# wire in a console plugin dev server
oinc create --console-plugin "my-plugin=http://host.docker.internal:9001"

# cluster status
oinc status

# interactive status dashboard
oinc status --watch

# fetch/refresh kubeconfig
oinc kubeconfig

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

| Addon | What it provides | Install method |
|-|-|-|
| `gateway-api` | Kubernetes Gateway API CRDs | upstream CRDs (k8s client) |
| `cert-manager` | Certificate management | upstream manifests (kubectl) |
| `metallb` | LoadBalancer IP allocation | upstream manifests (kubectl) |
| `istio` | Istio service mesh via Sail operator | helm |
| `kuadrant` | API management (rate limiting, auth, DNS) | helm |

Dependencies are resolved automatically. Installing `kuadrant` will pull in `gateway-api`, `cert-manager`, `metallb`, and `istio`.

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

## Ports

| Port | Service |
|-|-|
| `6443` | Kubernetes API server |
| `9000` | OpenShift Console |
| `9080` | Ingress HTTP (Routes, Gateway API) |
| `9443` | Ingress HTTPS (Routes, Gateway API) |

## Requirements

- Docker or Podman
- ~4GB RAM available for the container
- `curl` (for fetching upstream manifests and CRDs)
- `kubectl` (for cert-manager and metallb addons)
- `helm` (for istio and kuadrant addons)

## Acknowledgements

oinc builds on the work of several projects:

- [MicroShift](https://github.com/openshift/microshift) -- the lightweight OpenShift runtime that powers the cluster
- [microshift-io](https://github.com/microshift-io/microshift) -- OKD-flavoured MicroShift builds and pre-built RPMs
- [OKD](https://www.okd.io/) -- the community distribution of Kubernetes that powers OpenShift
- [OpenShift Console](https://github.com/openshift/console) -- the web UI
- [minc](https://github.com/redhat-et/minc) -- inspiration for running MicroShift in a container
