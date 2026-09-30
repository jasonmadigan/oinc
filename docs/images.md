# Image builds

## Registry

Images published at `ghcr.io/jasonmadigan/oinc`.

Tags normally follow `<okd-version>-<arch>`. A replacement build can have a catalogue-pinned revision: 5.0 uses `5.0.0-okd-scos.0-oinc.1-<arch>` to avoid reusing the earlier mixed-version image.

Native Console builds are published at `ghcr.io/jasonmadigan/oinc-console` with the same tag pattern, naming the OKD payload the Console came from. See [Console images](#console-images).

For the separate, manually built Red Hat MicroShift rc0/rc1 container experiment, see [OCP release candidates](ocp-release-candidates.md). It uses kindnet and a runtime pull secret; it is not part of the OKD publishing workflow or CLI selectors.

The Red Hat MicroShift 4.23 preview uses `images/Containerfile.ocp` and the image workflow’s `ocp-preview` job. Its `ocp-4.23.0-ec.1-<arch>` tags require a pull secret at runtime. See [4.23 preview](ocp-4.23.md).

## RPM sources

Each version in the workflow matrix picks exactly one source:

| Source | Build arg | Used for |
|-|-|-|
| GitHub release tarball | `RELEASE_TAG` | released versions with a [microshift-io/microshift release](https://github.com/microshift-io/microshift/releases), e.g. 4.20, 4.21 |
| Pinned COPR nightly | `COPR_PIN` | 4.22 |
| Pinned release source | `LOCAL_RPMS=1` | 5.0, built using `images/5.0.json` |

The tarball path downloads `microshift-rpms-<arch>.tgz` from the release tag, verifies it against a per-leg `tarball_sha256` pinned in the workflow matrix, and installs from it as a local repo. GitHub release assets are not guaranteed immutable, so the tag pins a name and the sha256 pins the content.

The COPR path installs from [`@microshift-io/microshift-nightly`](https://copr.fedorainfracloud.org/coprs/g/microshift-io/microshift-nightly/), pinned to an exact version-release for every package. The COPR only builds upstream main and prunes old builds: unpinned installs drift to whatever main currently produces, and pinned builds can vanish once upstream moves on. Switch a version to the tarball path as soon as a GitHub release exists.

That COPR project runs in devel mode, so a new build only lands in the `devel/` repodata of each chroot until the project owner regenerates the main repo. The main repodata can therefore sit months behind the newest build. Both repos are enabled during the build with `skip_if_unavailable=1`, and either can satisfy the pin. Both index the same physical build directories, so nothing is downloaded twice.

All paths fail unless the installed `microshift-release-info` carries the `OKD_VERSION` tag. For 5.x, the installed MicroShift and CRI-O RPM minors must also match `OCP_VERSION`. A stable OKD tag alone does not establish a matching MicroShift source version.

## Base image

`quay.io/centos/centos:stream9`

## Build args

| Arg | Description | Example |
|-|-|-|
| `OCP_VERSION` | OCP version for openshift deps mirror URL | `5.0` |
| `DEPS_VERSION` | dependency mirror override; for 5.x it must equal `OCP_VERSION`, as must the installed MicroShift and CRI-O RPM minors | `5.0` |
| `OKD_VERSION` | expected OKD tag, asserted against installed `microshift-release-info` | `5.0.0-okd-scos.0` |
| `RELEASE_TAG` | microshift-io GitHub release tag to install RPM tarballs from | `4.20.0_g153ff0ca9_4.20.0_okd_scos.16` |
| `TARBALL_SHA256` | expected sha256 of the downloaded RPM tarball | `536d3081...` |
| `COPR_PIN` | exact RPM version-release to pin in the COPR repo | `5.0.0_202605120437_g8e93344a3_4.22.0_okd_scos.ec.16-1.el9` |
| `LOCAL_RPMS` | install the source-built RPM repository from `images/rpms` | `1` |
| `WITH_OLM` | set to `1` to install OLM packages | `1` |

Select exactly one of `RELEASE_TAG`, `COPR_PIN` or `LOCAL_RPMS=1`. `TARBALL_SHA256` is required with `RELEASE_TAG`. The build context is `images/`, including the `rpms/` directory (`.gitkeep` suffices for remote RPM sources). Source-built RPMs are bind-mounted during installation. Tarball RPMs are downloaded, unpacked and removed within the same build step.

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
1. For source-locked versions, compiles RPMs using the pinned packaging recipes
2. Builds the final image with podman on a native runner (amd64 on `ubuntu-24.04`, arm64 on `ubuntu-24.04-arm`), installing the source-built RPMs, release tarballs or pinned COPR packages
3. Pushes to GHCR

Optional `version` input filters to a single OCP version.

## Console images

Up to 4.22 the Console is `quay.io/openshift/origin-console:<minor>`. It is amd64-only, so ARM hosts run it under emulation.

From 5.0 the Console is built on CentOS Stream 10, whose glibc requires x86-64-v3. amd64 emulation under OrbStack on Apple Silicon does not provide it, so the container exits at once:

```text
Fatal glibc error: CPU does not support x86-64-v3
```

The `origin-console:5.0` image inspected on 28 September 2026 was a 10 August build of openshift/console `main`, not the release. That tag can move. 5.0 therefore uses the OKD payload's Console, pinned per architecture by `ConsoleImages` in `pkg/version/version.go`:

| Arch | Image |
|-|-|
| amd64 | payload Console, `quay.io/okd/scos-content@sha256:947e41d0...` for `5.0.0-okd-scos.0` |
| arm64 | native rebuild of that Console, `ghcr.io/jasonmadigan/oinc-console:5.0.0-okd-scos.0-arm64` |

`images/Containerfile.console` builds the arm64 image. It rebuilds `bridge` from the upstream image's source commit with the CentOS Stream 9 Go toolset, matching upstream's el9 builder, and copies the frontend assets unchanged from the upstream image. The build fails unless the upstream binary's version string names `CONSOLE_COMMIT`. The image workflow's `console` job publishes it; custom OKD builds skip that job and reuse the minor's Console.

To find the inputs for a payload:

```bash
oc adm release info --image-for=console quay.io/okd/scos-release:<okd-version>
oc image info <console-image> -o json | jq -r '.config.config.Labels["io.openshift.build.commit.id"]'
```

Local build:

```bash
docker build -f images/Containerfile.console \
  --build-arg CONSOLE_IMAGE=<console-image> \
  --build-arg CONSOLE_COMMIT=<commit> \
  -t ghcr.io/jasonmadigan/oinc-console:<okd-version>-arm64 images/
```

## Building a newer OKD release

For 5.0, update `images/5.0.json`, the matching workflow matrix fields and the catalogue's replacement image tag. Custom COPR inputs are rejected for source-locked entries. Build and verify both architectures before publishing the new revision.

Other minors still accept the `version`, `okd_version`, `copr_pin` and `deps_version` workflow inputs together. Verify the RPM's source release and CRI-O requirements as well as its OKD payload tag. The image build's version guards still apply. For tarball releases, update the matrix's `release_tag` and architecture-specific checksums.

Remote selectors discover published payloads for supported minors. Replacement image revisions must be recorded in the CLI catalogue; older CLIs do not learn replacement mappings from registry tags.

## Release-aligned source builds

No community MicroShift 5.0 RPM release was available on 28 September 2026. COPR's builds for the stable OKD 5.0 payload compile MicroShift main (5.1), so 5.0 now uses the upstream packaging toolchain with explicit inputs in `images/5.0.json`:

- MicroShift commit `07ab806795491479323b6084c4375770e3c98266`, from `release-5.0` and tagged [`5.0.0-rc.2-202609111451.p0`](https://github.com/openshift/microshift/releases/tag/5.0.0-rc.2-202609111451.p0). Its [Go module pins Kubernetes 1.36.3](https://github.com/openshift/microshift/blob/07ab806795491479323b6084c4375770e3c98266/go.mod), matching the OKD payload. This is a community rebuild of rc2 source, not an official MicroShift GA binary.
- microshift-io packaging commit `b520d54da1bc5f0e48a99057c5cb018b2893b7d0` and Go 1.26.7.
- Both architectures' OKD 5.0 payloads pinned by digest. The ARM payload is microshift-io's rebuild.
- The 5.0 dependency mirror; the source RPM requires `cri-o >= 5.0.0, < 5.1.0`.

`images/build-rpms.py` fetches the pinned packaging commit and adapts its recipes to check out an exact MicroShift commit, use digest-qualified payloads for both architectures, and pin Go. It fails if the expected recipe changes. It retains the upstream community packaging changes and kindnet integration. The build timestamp is fixed so both architectures have the same RPM version.

The payload does not supply every image reference: the pinned packaging supplies kindnet, and its [replacement script](https://github.com/microshift-io/microshift/blob/b520d54da1bc5f0e48a99057c5cb018b2893b7d0/src/image/prebuild.sh) retains the original `lvms_operator` release-info entry. This Containerfile installs neither LVMS nor TopoLVM. Compare the components actually used rather than claiming every release-info digest comes from OKD.

Run on each native Linux architecture, or through Docker on an ARM Mac:

```bash
python3 images/build-rpms.py images/5.0.json --runtime docker --output images/rpms
# Use arm64 or amd64 to match the native builder.
docker build -f images/Containerfile \
  --build-arg OCP_VERSION=5.0 \
  --build-arg OKD_VERSION=5.0.0-okd-scos.0 \
  --build-arg LOCAL_RPMS=1 \
  -t ghcr.io/jasonmadigan/oinc:5.0.0-okd-scos.0-oinc.1-arm64 images/
```

The output directory must be empty apart from `.gitkeep`; use a fresh directory or remove only previously generated RPM output before rebuilding. Generated RPMs are git-ignored. The final image records the lock at `/usr/share/oinc/release.json`. Base-image and dependency repositories can still change, so this pins the release composition rather than promising byte-identical rebuilds.

The replacement tag leaves the original published experiment intact. The updated CLI skips the original tag in channel discovery and uses the replacement for both `--version 5.0` and `--version 5.0.0-okd-scos.0`. A cached old image therefore cannot satisfy a fresh create. Publish both replacement architectures and run the 5.0 e2e jobs before merging or releasing the CLI.

## Adding a new version

1. Pick the RPM source. Prefer a [GitHub release](https://github.com/microshift-io/microshift/releases) whose tag matches the OKD version (`release_tag`), recording the sha256 of each `microshift-rpms-<arch>.tgz` asset (`tarball_sha256`). If none exists yet, find the matching build in the [nightly COPR](https://copr.fedorainfracloud.org/coprs/g/microshift-io/microshift-nightly/builds/) and note the exact version-release (`copr_pin`). Check the pin resolves for `epel-9-x86_64` and `epel-9-aarch64` in either the main or the `devel/` repodata, since a COPR build can succeed on one arch and not the other.

For source builds, add a release lock instead. Check that the MicroShift source and CRI-O minor match the target release; do not compensate for a mainline RPM by silently moving to a newer dependency mirror.

2. Add the minor to the `version` input choices and matrix entries in `.github/workflows/images.yml` for both `amd64` and `arm64`, each with `okd_version` plus `release_tag` and `tarball_sha256`, or `copr_pin`; source builds set `source_lock` and `image_tag` instead

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
