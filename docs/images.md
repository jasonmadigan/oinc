# Image builds

## Registry

Images published at `ghcr.io/jasonmadigan/oinc`.

Tags follow the pattern: `<okd-version>-<arch>`, e.g. `5.0.0-okd-scos.ec.8-arm64`.

For the separate, manually built Red Hat MicroShift rc0/rc1 container experiment, see [OCP release candidates](ocp-release-candidates.md). It uses kindnet and a runtime pull secret; it is not part of the OKD publishing workflow or CLI selectors.

## RPM sources

Each version in the workflow matrix picks exactly one source:

| Source | Build arg | Used for |
|-|-|-|
| GitHub release tarball | `RELEASE_TAG` | released versions with a [microshift-io/microshift release](https://github.com/microshift-io/microshift/releases), e.g. 4.20, 4.21 |
| Pinned COPR nightly | `COPR_PIN` | pre-release versions with no GitHub release yet, e.g. 4.22, 5.0 |

The tarball path downloads `microshift-rpms-<arch>.tgz` from the release tag, verifies it against a per-leg `tarball_sha256` pinned in the workflow matrix, and installs from it as a local repo. GitHub release assets are not guaranteed immutable, so the tag pins a name and the sha256 pins the content.

The COPR path installs from [`@microshift-io/microshift-nightly`](https://copr.fedorainfracloud.org/coprs/g/microshift-io/microshift-nightly/), pinned to an exact version-release for every package. The COPR only builds upstream main and prunes old builds: unpinned installs drift to whatever main currently produces, and pinned builds can vanish once upstream moves on. Switch a version to the tarball path as soon as a GitHub release exists.

That COPR project runs in devel mode, so a new build only lands in the `devel/` repodata of each chroot until the project owner regenerates the main repo. The main repodata can therefore sit months behind the newest build. Both repos are enabled during the build with `skip_if_unavailable=1`, and either can satisfy the pin. Both index the same physical build directories, so nothing is downloaded twice.

Both paths end with a guard: the build fails unless the installed `microshift-release-info` carries the `OKD_VERSION` tag.

## Base image

`quay.io/centos/centos:stream9`

## Build args

| Arg | Description | Example |
|-|-|-|
| `OCP_VERSION` | OCP version for openshift deps mirror URL | `5.0` |
| `DEPS_VERSION` | override dependency mirror minor when RPM requirements differ from the payload | `5.1` |
| `OKD_VERSION` | expected OKD tag, asserted against installed `microshift-release-info` | `5.0.0-okd-scos.ec.8` |
| `RELEASE_TAG` | microshift-io GitHub release tag to install RPM tarballs from | `4.20.0_g153ff0ca9_4.20.0_okd_scos.16` |
| `TARBALL_SHA256` | expected sha256 of the downloaded RPM tarball | `536d3081...` |
| `COPR_PIN` | exact RPM version-release to pin in the COPR repo | `5.1.0_202609020534_gb19f04dec_5.0.0_okd_scos.ec.8-1.el9` |
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

## Building a newer OKD release

For a newer COPR build within a supported minor, dispatch the existing workflow with all four inputs. This example reproduces the current 5.0 pin; replace the OKD tag and COPR pin with a verified newer build when available:

```bash
gh workflow run images.yml \
  -f version=5.0 \
  -f okd_version=5.0.0-okd-scos.ec.8 \
  -f copr_pin=5.1.0_202609020534_gb19f04dec_5.0.0_okd_scos.ec.8-1.el9 \
  -f deps_version=5.1
```

Verify the exact COPR pin exists for both architectures and choose the dependency minor required by its CRI-O dependency. The override selects COPR instead of any catalogue tarball source. The installed OKD-tag guard still runs. Each architecture publishes its exact OKD tag; no floating `latest` image is created. Failed pushes fail the workflow.

Once published, `oinc version list --remote` discovers the image. Select its full OKD tag or use `5.0@next` for prereleases. Stable builds become eligible for `@latest`. No catalogue edit or CLI rebuild is required for an existing minor. This does not monitor COPR or automatically build upstream releases.

For releases supplied as GitHub RPM tarballs, use the matrix's `release_tag` and per-architecture checksums as described below.

## Adding a new version

1. Pick the RPM source. Prefer a [GitHub release](https://github.com/microshift-io/microshift/releases) whose tag matches the OKD version (`release_tag`), recording the sha256 of each `microshift-rpms-<arch>.tgz` asset (`tarball_sha256`). If none exists yet, find the matching build in the [nightly COPR](https://copr.fedorainfracloud.org/coprs/g/microshift-io/microshift-nightly/builds/) and note the exact version-release (`copr_pin`). Check the pin resolves for `epel-9-x86_64` and `epel-9-aarch64` in either the main or the `devel/` repodata, since a COPR build can succeed on one arch and not the other.

2. Add the minor to the `version` input choices and matrix entries in `.github/workflows/images.yml` for both `amd64` and `arm64`, each with `okd_version` plus `release_tag` and `tarball_sha256`, or `copr_pin`

3. Add catalogue entry in `pkg/version/version.go`:
   ```go
   {
       Version:       "5.1",
       MicroShiftTag: "5.1.0-okd-scos.1",
       ConsoleTag:    "5.1",
       APIBranch:     "release-5.1",
       Arches:        []string{"amd64", "arm64"},
   },
   ```

4. Run the image workflow, then rebuild the CLI
