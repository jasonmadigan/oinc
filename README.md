# oinc

```
  ^..^
 ( oo )  oinc ~ OKD in a container
  (..)
```

MicroShift under the hood, with Console, OLM, and ConsolePlugin CRD out of the box.

```
oinc create
```

That's it. You get a single-node cluster with the OpenShift Console on `localhost:9000`, OLM running, and the ConsolePlugin CRD available -- close enough to real OCP for local dev work.

## Features

- **Auto-detects container runtime** (docker, podman) -- no flags needed
- **Version switching** -- `oinc create --version 4.18` to target a specific OCP release
- **Console included** -- OpenShift Console runs as a sidecar, no separate setup
- **OLM included** -- baked into the image, operator workflows work out of the box
- **Addon system** -- layer on Gateway API, cert-manager, MetalLB, Istio as needed
- **Console plugin support** -- `--console-plugin "my-plugin=http://localhost:9001"` for plugin dev

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
oinc create --version 4.18

# create with addons
oinc create --addons gateway-api,cert-manager

# wire in a console plugin dev server
oinc create --console-plugin "my-plugin=http://host.docker.internal:9001"

# switch OCP version
oinc switch 4.18

# list available versions
oinc version list

# tear down
oinc delete
```

## Addons

The base cluster includes MicroShift + OLM + Console + ConsolePlugin CRD. Addons layer extra infrastructure on top:

| Addon | What it provides |
|-|-|
| `gateway-api` | Kubernetes Gateway API CRDs |
| `cert-manager` | Certificate management |
| `metallb` | LoadBalancer IP allocation |
| `istio` | Istio service mesh via Sail operator |

```bash
# at create time
oinc create --addons gateway-api,cert-manager,metallb,istio

# or post-hoc
oinc addon install gateway-api
oinc addon list
```

## Requirements

- Docker or Podman
- ~4GB RAM available for the container

## Acknowledgements

oinc builds on the work of several projects:

- [MicroShift](https://github.com/openshift/microshift) -- the lightweight OpenShift runtime that powers the cluster
- [OKD](https://www.okd.io/) -- the community distribution of Kubernetes that powers OpenShift
- [OpenShift Console](https://github.com/openshift/console) -- the web UI
- [minc](https://github.com/redhat-et/minc) -- inspiration for running MicroShift in a container
