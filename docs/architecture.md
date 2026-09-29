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

Podman containers explicitly use the `k8s-file` log driver so they do not depend on the host's `conmon` build including journald support. With Podman, the MicroShift node also mounts `/var/lib/containers` as tmpfs to avoid nested container-storage driver failures; the Docker path does not add that mount.

Container host address varies by runtime:
- docker on macOS: `host.docker.internal`
- podman on macOS: `host.containers.internal`
- Linux outside WSL: `localhost`
- WSL: the runtime-specific host name above

## Version catalogue

`pkg/version/version.go` defines a catalogue of supported OCP versions. Each entry coordinates:

- MicroShift image tag (e.g. `4.21.0-okd-scos.ec.15`)
- Optional replacement image tag (`ImageTag`), separating an OINC build revision from its OKD payload version
- Console image tag (e.g. `4.21`), or per-architecture Console images from 5.0
- openshift/api branch for CRD fetch (e.g. `release-4.21`)
- Supported architectures

The newest stable entry is the default; `ec` and `rc` tags are opt-in. `--version` accepts a catalogue minor, a full OKD tag for a supported minor, or a remote channel. `@latest` selects stable published images only; `@next` includes prereleases. Both can be scoped to a major or minor, such as `4@latest` or `5.0@next`. Remote discovery is in `pkg/version/remote.go`.

Stability here refers to the OKD payload tag. The 5.0 MicroShift binary is a community build of pinned 5.0 rc2 source with the same Kubernetes 1.36.3 as that payload. `images/5.0.json` fixes the source, packaging and payload inputs. The new `-oinc.1` image tag avoids reusing a cached 5.1-based image; remote discovery counts only architectures published under the replacement tag.

## Kubeconfig

MicroShift generates a kubeconfig inside the container at `/var/lib/microshift/resources/kubeadmin/<hostname>/kubeconfig`. oinc merges this into the single file named by `KUBECONFIG`, or `~/.kube/config` when unset. It sets the current context to `oinc`; a colon-separated list of kubeconfig paths is not supported by this helper.

The CLI sets the container hostname to `127.0.0.1.nip.io` and maps host port 6443 to the API server. The image separately sets the MicroShift DNS base domain to the same name.

## Console sidecar

The OpenShift Console runs as a separate container (`oinc-console`). Up to 4.22 it uses `quay.io/openshift/origin-console`; from 5.0 it uses the OKD payload's Console, with a native build on arm64.

Setup flow:
1. Apply ConsolePlugin CRD (fetched from `openshift/api` at the correct branch)
2. Create ServiceAccount + cluster-admin ClusterRoleBinding + impersonation RBAC
3. Generate a long-lived bearer token
4. Start console container with env vars pointing at the API server

Note: `origin-console` is amd64-only. On ARM hosts it runs via Rosetta/emulation. The 5.0 Console's CentOS Stream 10 base fails under that emulation, so 5.0 pins a native arm64 image. See [Console images](images.md#console-images).

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

The base image includes OLM, and `kuadrant@latest` selects the Kuadrant catalogue configured in `pkg/addons/kuadrant.go`. Other addon paths use manifests or Helm. OLM being present does not establish compatibility with every OperatorHub catalogue or operator; validate the selected catalogue on the target build.

## Networking

MicroShift runs its own ingress router (based on HAProxy). Ports 80 and 443 inside the container are mapped to 9080 and 9443 on the host for OpenShift Routes. Gateway API HTTPRoutes use the controller and data plane selected by their parent Gateway; the Istio addon creates separate gateway Services. They are not automatically served through those Route ports.

MetalLB addon is needed for `LoadBalancer` type Services (e.g. Istio's ingress gateway). On OpenShift/MicroShift, MetalLB pods need the `privileged` SecurityContextConstraint -- oinc handles this automatically via the `grantSCC` helper.
