# Architecture

## Overview

oinc runs a MicroShift cluster inside a privileged container (docker or podman), with an OpenShift Console sidecar container alongside it. OLM is baked into the MicroShift image at build time.

```
┌─────────────────────────────────────────┐
│  host (macOS / Linux)                   │
│                                         │
│  ┌──────────────┐  ┌────────────────┐   │
│  │ oinc         │  │ oinc-console   │   │
│  │ (MicroShift) │  │ (Console UI)   │   │
│  │              │  │                │   │
│  │ :6443 (API)  │  │ :9000 (web)    │   │
│  │ :80 (HTTP)   │  │                │   │
│  │ :443 (HTTPS) │  │                │   │
│  └──────────────┘  └────────────────┘   │
│                                         │
│  ports: 6443, 9080->80, 9443->443, 9000 │
└─────────────────────────────────────────┘
```

## Container runtime

Single `Runtime` struct (`pkg/runtime/`) wraps docker or podman. Auto-detected at startup (docker first, then podman). No abstraction layer -- one implementation parameterised by binary name.

On Linux, validates cgroup v2 and rootful mode. On macOS, skips validation (Docker Desktop / Podman Desktop handle this).

Podman containers explicitly use the `k8s-file` log driver so they do not depend on the host's `conmon` build including journald support. The MicroShift node also mounts `/var/lib/containers` as tmpfs, matching MicroShift's own Podman cluster manager and avoiding nested container-storage driver failures.

Container host address varies by runtime:
- docker on macOS: `host.docker.internal`
- podman on macOS: `host.containers.internal`
- Linux: `localhost`

## Version catalogue

`pkg/version/version.go` defines a catalogue of supported OCP versions. Each entry coordinates:

- MicroShift image tag (e.g. `4.21.0-okd-scos.ec.15`)
- Console image tag (e.g. `4.21`)
- openshift/api branch for CRD fetch (e.g. `release-4.21`)
- Supported architectures

The latest entry is the default. `--version` selects a specific entry.

## Kubeconfig

MicroShift generates a kubeconfig inside the container at `/var/lib/microshift/resources/kubeadmin/<hostname>/kubeconfig`. oinc copies this out and merges it into the user's `~/.kube/config`.

The hostname is `127.0.0.1.nip.io` (set in the MicroShift DNS config inside the image), so the API server is reachable at `https://127.0.0.1:6443`.

## Console sidecar

The OpenShift Console runs as a separate container (`oinc-console`) using the `origin-console` image from `quay.io/openshift/origin-console`.

Setup flow:
1. Apply ConsolePlugin CRD (fetched from `openshift/api` at the correct branch)
2. Create ServiceAccount + cluster-admin ClusterRoleBinding + impersonation RBAC
3. Generate a long-lived bearer token
4. Start console container with env vars pointing at the API server

Note: `origin-console` is amd64-only. On ARM hosts it runs via Rosetta/emulation.

## Addon system

Addons live in `pkg/addons/`. Each addon implements the `Addon` interface:

```go
type Addon interface {
    Name() string
    Dependencies() []string
    Install(ctx context.Context, cfg *Config) error
    Ready(ctx context.Context, cfg *Config) error
}
```

Key design points:
- `Install` must be idempotent (safe to run repeatedly)
- `Ready` blocks until the addon is operational
- Dependencies resolved via topological sort (Kahn's algorithm)
- Version pinning via `Configurable` interface and `@` syntax (e.g. `cert-manager@1.16.0`)

Install methods vary by addon:
- **Upstream manifests** (gateway-api, cert-manager, metallb): downloaded via curl; cert-manager and metallb use `kubectl apply --server-side`, while gateway-api updates its CRDs through the dynamic Kubernetes client
- **Helm** (istio, pinned kuadrant releases, rhdh, standalone mcp-gateway): `helm upgrade --install` for idempotency
- **Kuadrant-managed MCP Gateway**: render the OCI chart with its controller disabled, then apply instance resources using the MCP API version served by Kuadrant's CRDs. Controller and CRD lifecycle stays with Kuadrant.
- **OLM** (`kuadrant@latest`): install from Kuadrant's compatible latest catalogue

MicroShift's OLM is present but uses an older catalogue format incompatible with FBC (File-Based Catalogue) images from OperatorHub. Most addons therefore use manifests or Helm; Kuadrant's latest catalogue is supported through OLM.

## Networking

MicroShift runs its own ingress router (based on HAProxy). Ports 80 and 443 inside the container are mapped to 9080 and 9443 on the host. These handle both OpenShift Routes and Gateway API HTTPRoutes.

MetalLB addon is needed for `LoadBalancer` type Services (e.g. Istio's ingress gateway). On OpenShift/MicroShift, MetalLB pods need the `privileged` SecurityContextConstraint -- oinc handles this automatically via the `grantSCC` helper.
