# Image builds

## Registry

Images published at `ghcr.io/jasonmadigan/oinc`.

Tags follow the pattern: `<okd-version>-<arch>`, e.g. `4.22.0-okd-scos.ec.16-arm64`.

## Source

MicroShift RPMs come from the [`@microshift-io/microshift-nightly`](https://copr.fedorainfracloud.org/coprs/g/microshift-io/microshift-nightly/) COPR repo. Daily builds are available for `epel-9-x86_64` and `epel-9-aarch64`. No tarball downloads, no Red Hat pull secrets needed.

## Base image

`quay.io/centos/centos:stream9`

## Build args

| Arg | Description | Example |
|-|-|-|
| `OCP_VERSION` | OCP version for openshift deps mirror URL | `4.22` |
| `WITH_OLM` | set to `1` to install OLM packages | `1` |

## What the image contains

1. **MicroShift** -- `microshift`, `microshift-release-info`, `microshift-kindnet`, `microshift-kindnet-release-info`
2. **OLM** (when `WITH_OLM=1`) -- `microshift-olm`, `microshift-olm-release-info`
3. **CNI plugins** -- downloaded from `containernetworking/plugins` (v1.8.0), required by kindnet
4. **Firewall rules** -- trusted zone for pod CIDR (10.42.0.0/16) and link-local (169.254.169.1), public zone for API (6443) and etcd (2379/2380)
5. **DNS config** -- base domain set to `127.0.0.1.nip.io`
6. **skopeo** for importing images into CRI-O, guaranteed present

The OpenShift dependencies RPM mirror (`mirror.openshift.com`) provides packages needed by MicroShift at install time. This repo is removed after install.

## Build workflow

`.github/workflows/images.yml` -- manual dispatch via `workflow_dispatch`.

Matrix builds all version/arch combinations in parallel. Each job:
1. Builds image with podman on native arch runner (amd64 on `ubuntu-24.04`, arm64 on `ubuntu-24.04-arm`)
2. MicroShift RPMs installed from COPR during the build (no pre-download step)
3. Pushes to GHCR

Optional `version` input filters to a single OCP version.

## Adding a new version

1. Check COPR for available builds at https://copr.fedorainfracloud.org/coprs/g/microshift-io/microshift-nightly/builds/

2. Add matrix entries in `.github/workflows/images.yml` for both `amd64` and `arm64`

3. Add catalogue entry in `pkg/version/version.go`:
   ```go
   {
       Version:       "4.23",
       MicroShiftTag: "4.23.0-okd-scos.1",
       ConsoleTag:    "4.23",
       APIBranch:     "release-4.23",
       Arches:        []string{"amd64", "arm64"},
   },
   ```

4. Run the image workflow, then rebuild the CLI
