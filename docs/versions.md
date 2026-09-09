# Version management

## How versions work

Each supported OCP version is an entry in `pkg/version/version.go`. An entry coordinates:

- **MicroShift image tag** -- the pre-built MicroShift container image on GHCR
- **Console image tag** -- the `origin-console` image from quay.io
- **openshift/api branch** -- for fetching CRDs and feature gates at install time

All catalogue builds are OKD MicroShift. OCP version names identify compatibility with the Console and API branch. For the separate Red Hat rc0/rc1 artifacts and local test findings, see [OCP release candidates](ocp-release-candidates.md).

The last **stable** entry in the catalogue is the default. Entries whose OKD tags contain `ec` or `rc` are pre-release and are never the default. `oinc create` selects this pin unless a version is explicitly chosen; the interactive picker marks prereleases.

## Selecting newer builds

| Selector | Behaviour |
|-|-|
| `4.20` | Offline catalogue pin for that minor |
| `5.0` | Explicit opt-in to the catalogue's pinned 5.0 prerelease |
| `5.0.0-okd-scos.ec.8` | Exact OKD image tag, reusing 5.0 Console/API settings |
| `@latest` or `latest` | Newest published stable build across supported minors |
| `4@latest` | Newest published stable build in supported 4.x minors |
| `4.20@latest` | Newest published stable build within 4.20 |
| `5.0@next` | Newest published 5.0 build, including prereleases |
| `@next` or `next` | Newest published build across supported minors, including prereleases |

`@latest` always excludes `ec` and `rc`, regardless of the major version. `5.0@latest` therefore fails until a stable 5.0 OKD image is published; it never falls back to a prerelease. `@next` also accepts a major scope such as `5@next`.

`oinc version list` shows catalogue pins; `oinc version list --remote` queries GHCR and shows published images and their architectures, including builds added after the CLI was compiled. Unrecognised tags and unsupported minors are omitted. Channels select only images published for the host architecture, ordered numerically by version, then stage (`ec`, `rc`, stable), then build number.

Remote lookups require network access and report errors instead of silently using an older pin. Minor pins and full tags resolve offline, though pulling a missing image still needs the registry. A full tag does not prove its image has been published.

Both `create --version` and `switch` accept these selectors. A channel resolves once to an exact tag; an existing cluster is not automatically upgraded. `switch` resolves the selector before deleting the old cluster, so an unavailable channel leaves it intact. It remains a delete-and-create operation, not an in-place upgrade.

A newer build within an existing minor needs only a published oinc image, not a new CLI catalogue entry. New minors still need compatibility settings in the catalogue. See [building a newer OKD release](images.md#building-a-newer-okd-release).

## RPM sources

Released versions install MicroShift RPM tarballs from [microshift-io/microshift GitHub releases](https://github.com/microshift-io/microshift/releases). Pre-release versions with no GitHub release yet install from the `@microshift-io/microshift-nightly` COPR repo, pinned to an exact version-release. The COPR only builds upstream main and prunes old builds, so a pinned COPR version can become unbuildable over time; switch it to the tarball path once a GitHub release appears.

The COPR project runs in devel mode, so recent builds appear only in the `devel/` repodata until the owner regenerates the main repo. Both repos are enabled at build time and either can satisfy the pin.

Every build asserts the installed `microshift-release-info` carries the intended OKD tag and fails otherwise, so an RPM source drifting to a different version cannot publish silently.

The openshift-deps mirror (`mirror.openshift.com`) is still used for dependency packages during the image build.

## Adding a new version

Use the `/add-version` Claude command to discover newer OKD builds, add supported minors or investigate Red Hat RCs. It verifies exact artifacts, preserves stable/prerelease selection, and tests the normal create flow. A manual workaround remains an unresolved integration issue. Requested updates proceed within existing authorization; discovery without a selected target presents candidates first.

Or manually:

1. Pick the RPM source: a [GitHub release](https://github.com/microshift-io/microshift/releases) tag matching the OKD version (preferred), or a pinned version-release from the [nightly COPR](https://copr.fedorainfracloud.org/coprs/g/microshift-io/microshift-nightly/builds/) if no release exists yet

2. Check that upstream resources exist:
   - `openshift/api` branch `release-{version}`
   - openshift-deps mirror directory `{version}-el9-beta`
   - Console image `quay.io/openshift/origin-console:{version}`

3. Add catalogue entry in `pkg/version/version.go`

4. Add the minor to the `version` input choices and CI matrix entries in `.github/workflows/images.yml` with `release_tag` or `copr_pin`

5. Update the versions table in `README.md`

6. Build the images:
   ```
   gh workflow run images.yml -f version={version}
   ```

Full details in [images.md](images.md).

## Removing a version

Delete the catalogue entry from `pkg/version/version.go`, the input choice and matrix entries from `.github/workflows/images.yml`, and the row from the README table. The GHCR images can be left in place or cleaned up manually.

## Version dependencies

When a new version is added, these things may need updating:

| Component | Where | What to check |
|-|-|-|
| Console CRD | `pkg/oinc/console.go` | Fetched from `openshift/api` at the release branch, adapts automatically |
| Gateway API CRDs | `pkg/addons/gatewayapi.go` | Version pinned independently, not tied to OCP version |
| Addon versions | `pkg/addons/*.go` | cert-manager, metallb, etc. are pinned independently |

Most things adapt automatically because they derive URLs from the version's `APIBranch` field.
