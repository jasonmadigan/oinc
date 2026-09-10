# CI / CD

## Workflows

### Image builds (`.github/workflows/images.yml`)

Builds MicroShift container images. **Manual dispatch only** (`workflow_dispatch`).

- Triggered manually from GitHub Actions UI
- Optional `version` input to build a single OCP minor (default: all)
- Optional `okd_version`, `copr_pin` and `deps_version` inputs, supplied together with `version`, build a newer pinned COPR release within that minor without changing the matrix. See [image builds](images.md#building-a-newer-okd-release).
- Matrix: version x arch (currently 4.20 + 4.21 + 4.22 + 5.0, each with amd64 + arm64 = 8 jobs)
- ARM builds run on `ubuntu-24.04-arm` runners (native, no emulation)
- Uses podman for builds (not docker)
- Pushes to `ghcr.io/jasonmadigan/oinc`

### E2E smoke (`.github/workflows/e2e.yml`)

End-to-end smoke test. **Runs on pull requests** (and manual dispatch), concurrency-cancelling superseded runs on the same ref. Docs-only changes (`*.md`, `docs/`) skip it.

- Matrix over host runtime: docker and podman on `ubuntu-latest` (the podman leg runs rootful via sudo)
- Runs `go vet`, `go test`, builds the CLI
- Creates a cluster with a pinned catalogue version
- Asserts skopeo is present in the oinc image
- Asserts `oinc load-image` rejects a ref missing host-side
- Builds a trivial local image, loads it with `oinc load-image`, runs a pod from it with `imagePullPolicy: IfNotPresent`
- Re-runs the load to prove idempotence

A separate `rhdh` job (single leg: docker, pinned version, since rhdh + postgres + microshift is memory-heavy) creates a cluster with the rhdh addon, waits for the rollout, then asserts the Route serves the app and guest auth issues a token.

The `instances` job creates a fresh 4.22 cluster with `kuadrant@latest,mcp-gateway`, an automatic MetalLB pool, the default Gateway, and the developer portal. It checks that the default, MCP, and documented consumer Gateways are programmed with addresses and their generated Services use `oinc.io/metallb`. It also checks that `router-default` remains class-less with ingress status, then reinstalls the addons to exercise idempotence.

### CLI releases (`.github/workflows/release.yml`)

Builds and releases CLI binaries. **Triggered by pushing a `v*` tag.**

```bash
git tag v0.1.0
git push origin v0.1.0
```

- Cross-compiles for darwin/linux x amd64/arm64 (4 binaries)
- Injects version via `-ldflags "-X main.buildVersion=${VERSION}"`
- Creates GitHub release with auto-generated notes
- Binaries named `oinc-<os>-<arch>` (e.g. `oinc-darwin-arm64`)

## Permissions

- Image workflow needs `packages: write` to push to GHCR
- Release workflow needs `contents: write` to create releases
- E2E workflow needs `contents: read` only
- All use `${{ github.token }}` (no external secrets)

## Runner architecture

ARM images must be built on native ARM runners (`ubuntu-24.04-arm`). Cross-compilation via QEMU is too slow and unreliable for full OS image builds.

The CLI cross-compiles fine on any runner (pure Go, no CGO).
