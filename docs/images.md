# Image builds

## Registry

Images published at `ghcr.io/jasonmadigan/oinc`.

Tags follow the pattern: `<okd-version>-<arch>`, e.g. `4.22.0-okd-scos.ec.16-arm64`.

## RPM sources

Each version in the workflow matrix picks exactly one source:

| Source | Build arg | Used for |
|-|-|-|
| GitHub release tarball | `RELEASE_TAG` | released versions with a [microshift-io/microshift release](https://github.com/microshift-io/microshift/releases), e.g. 4.20, 4.21 |
| Pinned COPR nightly | `COPR_PIN` | pre-release versions with no GitHub release yet, e.g. 4.22 |

The tarball path downloads `microshift-rpms-<arch>.tgz` from the release tag, verifies it against a per-leg `tarball_sha256` pinned in the workflow matrix, and installs from it as a local repo. GitHub release assets are not guaranteed immutable, so the tag pins a name and the sha256 pins the content.

The COPR path installs from [`@microshift-io/microshift-nightly`](https://copr.fedorainfracloud.org/coprs/g/microshift-io/microshift-nightly/), pinned to an exact version-release for every package. The COPR only builds upstream main and prunes old builds: unpinned installs drift to whatever main currently produces, and pinned builds can vanish once upstream moves on. Switch a version to the tarball path as soon as a GitHub release exists.

Both paths end with a guard: the build fails unless the installed `microshift-release-info` carries the `OKD_VERSION` tag.

## Base image

`quay.io/centos/centos:stream9`

## Build args

| Arg | Description | Example |
|-|-|-|
| `OCP_VERSION` | OCP version for openshift deps mirror URL | `4.22` |
| `OKD_VERSION` | expected OKD tag, asserted against installed `microshift-release-info` | `4.22.0-okd-scos.ec.16` |
| `RELEASE_TAG` | microshift-io GitHub release tag to install RPM tarballs from | `4.20.0_g153ff0ca9_4.20.0_okd_scos.16` |
| `TARBALL_SHA256` | expected sha256 of the downloaded RPM tarball | `536d3081...` |
| `COPR_PIN` | exact RPM version-release to pin in the COPR repo | `5.0.0_202605120437_g8e93344a3_4.22.0_okd_scos.ec.16-1.el9` |
| `WITH_OLM` | set to `1` to install OLM packages | `1` |

Exactly one of `RELEASE_TAG` or `COPR_PIN` must be set. `TARBALL_SHA256` is required with `RELEASE_TAG`.

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
2. MicroShift RPMs installed during the build from the version's source (release tarball or pinned COPR)
3. Pushes to GHCR

Optional `version` input filters to a single OCP version.

## Adding a new version

1. Pick the RPM source. Prefer a [GitHub release](https://github.com/microshift-io/microshift/releases) whose tag matches the OKD version (`release_tag`), recording the sha256 of each `microshift-rpms-<arch>.tgz` asset (`tarball_sha256`). If none exists yet, find the matching build in the [nightly COPR](https://copr.fedorainfracloud.org/coprs/g/microshift-io/microshift-nightly/builds/) and note the exact version-release (`copr_pin`).

2. Add matrix entries in `.github/workflows/images.yml` for both `amd64` and `arm64`, each with `okd_version` plus `release_tag` and `tarball_sha256`, or `copr_pin`

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
