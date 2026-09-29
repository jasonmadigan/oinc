# Red Hat MicroShift 4.23 preview

`oinc create --version ocp-4.23` selects Red Hat MicroShift `4.23.0-ec.1` with community kindnet networking and the matching native Red Hat Console. It is a development preview, not full OCP or the default Red Hat networking configuration. The default remains OKD 5.0.

## Credentials and creation

```bash
# Download your Red Hat pull secret, then store it locally.
oinc pull-secret set /path/to/pull-secret.json
oinc pull-secret status
oinc create --version ocp-4.23

# Recreate an existing cluster with the preview instead:
oinc switch ocp-4.23
```

The exact selector is `ocp-4.23.0-ec.1`. Unprefixed `4.23` is not an OKD catalogue entry. `@latest`, `@next` and `version list --remote` continue to discover OKD images only; `version list` also shows the pinned Red Hat preview.

The secret must contain `quay.io` credentials. It is stored with mode `0600` under the OS user config directory (`~/Library/Application Support/oinc` on macOS, `~/.config/oinc` on Linux). `OINC_CONFIG_DIR` overrides that directory. Rootful Podman commands run with the root user's configuration; set the secret in that same context.

OINC mounts the secret read-only at `/etc/crio/openshift-pull-secret` before MicroShift starts. The host Console pull uses a temporary Docker configuration preserving the selected context, or Podman's `--authfile`. Credentials are not baked into the image and the host's registry login configuration is not modified. Image signature policy is unchanged.

`oinc pull-secret remove` removes the local copy. Recreate the cluster after replacing or removing a secret: an existing bind mount retains its original file. This does not revoke the underlying registry credentials.

## Build inputs

Checked on 29 September 2026:

| Input | Pin |
|-|-|
| MicroShift release | `4.23.0-ec.1` |
| RPM version-release | `4.23.0~ec.1-202609281056.p0.g07ab806.assembly.ec.1.el9` |
| MicroShift source | `07ab806795491479323b6084c4375770e3c98266` |
| Kubernetes | `1.36.3` |
| CRI-O RPM dependency | `>= 5.0.0, < 5.1.0`, as required by the official RPM |
| Networking donor | oinc `5.0.0-okd-scos.0-oinc.1`, native architecture |
| Image tags | `ghcr.io/jasonmadigan/oinc:ocp-4.23.0-ec.1-{amd64,arm64}` |

The official [amd64 RPM repository](https://mirror.openshift.com/pub/openshift-v4/x86_64/microshift/ocp-dev-preview/4.23.0-ec.1/el9/os/) and [arm64 RPM repository](https://mirror.openshift.com/pub/openshift-v4/aarch64/microshift/ocp-dev-preview/4.23.0-ec.1/el9/os/) both carry this pin. The build uses the 5.0 dependency mirror because the 4.23 RPM itself requires CRI-O 5.0. It retains the official core and OLM release-info and asserts `release.base == 4.23.0-ec.1`. The base OS and dependency mirror remain mutable.

The native Console references in `pkg/version/version.go` come from these OCP payloads:

- amd64: `quay.io/openshift-release-dev/ocp-release@sha256:04c9fd20fe9f5a4978d759448524cafe77096e1c50fe66f79f0eeeb084ecb7cd`
- arm64: `quay.io/openshift-release-dev/ocp-release@sha256:af01db29dbf0f43299753e8dee28b90ace43801ae7166bd96d56fae32dc3c6c2`

Both Console binaries identify source commit `0216b2de8b57c3375fcc161d2c75a031bef72c1b`. OINC sets OCP branding explicitly.

`images/Containerfile.ocp` copies kindnet, kube-proxy integration, CNI binaries and the systemd service from the tested OKD donor. OVN and LVMS are disabled. NetworkPolicy, storage conformance and full OCP cluster-operator behaviour are not established by this setup.

## Build and publish

The build downloads public RPMs and requires no pull secret. Runtime component and Console pulls do require one.

```bash
docker build -f images/Containerfile.ocp \
  --build-arg OKD_NETWORKING_IMAGE=ghcr.io/jasonmadigan/oinc:5.0.0-okd-scos.0-oinc.1-arm64 \
  -t ghcr.io/jasonmadigan/oinc:ocp-4.23.0-ec.1-arm64 images/
```

Use the amd64 donor and target tag on an amd64 builder. The `ocp-preview` job in `.github/workflows/images.yml` (select `version=ocp-4.23`) builds and publishes both architectures on native GitHub runners. It does not contain or upload runtime pull secrets.

## Validation

On 29 September 2026, a normal `oinc create --version ocp-4.23 --runtime docker` with the locally built native ARM image passed:

- MicroShift and base release `4.23.0-ec.1`, Kubernetes 1.36.3 and the expected CRI-O 5.0 RPM.
- All nine platform pods ready, including kindnet and OLM.
- Shared core release-info image references match the exact ARM OCP payload.
- Read-only pull-secret mount; authenticated native Console pull with the existing Docker context.
- Local image loading, Deployment rollout, internal service DNS/HTTP and HTTP through an OpenShift Route.
- OCP Console branding, six initial assets, proxied node/Pod/Deployment reads, ConfigMap create/read/delete and missing-CSRF rejection.

This was HTTP/API testing, not a browser walkthrough. Native amd64 runtime validation remains outstanding; both architectures were built on native runners in [image run 36594982571](https://github.com/jasonmadigan/oinc/actions/runs/36594982571). The published manifests and image architectures were verified anonymously:

| Architecture | Published image digest |
|-|-|

| amd64 | `sha256:1712e7f31be1fd8c465e6a1d4b331faf7c96b34075c9e2f0eb788cf2eb90fc67` |
| arm64 | `sha256:bc1d1d199184655443ca4d9228ddf0609dac9bf1977c0a008a3bc3da8c5b4d66` |
