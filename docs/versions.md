# Version management

## How versions work

Each supported OCP version is an entry in `pkg/version/version.go`. An entry coordinates:

- **MicroShift image tag** -- the pre-built MicroShift container image on GHCR
- **Console image tag** -- the `origin-console` image from quay.io
- **openshift/api branch** -- for fetching CRDs and feature gates at install time

The last entry in the catalogue is the default. `oinc create` uses it unless `--version` is specified.

## Adding a new version

Use the `/add-version` Claude command to scan for new MicroShift releases and add them. It checks upstream resources, presents findings, and applies changes after confirmation.

Or manually:

1. Find the release tag:
   ```
   gh release list -R microshift-io/microshift --limit 20
   ```

2. Check that upstream resources exist:
   - `openshift/api` branch `release-{version}`
   - Console image `quay.io/openshift/origin-console:{version}`

3. Add catalogue entry in `pkg/version/version.go`

4. Add CI matrix entries in `.github/workflows/images.yml`

5. Update the versions table in `README.md`

6. Build the images:
   ```
   gh workflow run images.yml -f version={version}
   ```

Full details in [images.md](images.md).

## Removing a version

Delete the catalogue entry from `pkg/version/version.go`, the matrix entries from `.github/workflows/images.yml`, and the row from the README table. The GHCR images can be left in place or cleaned up manually.

## Version dependencies

When a new version is added, these things may need updating:

| Component | Where | What to check |
|-|-|-|
| Feature gates | `pkg/addons/ingressoperator.go` | Parses `features.go` at install time, so adapts automatically |
| Config CRDs | `pkg/addons/ingressoperator.go` | Fetched from `openshift/api` at the release branch, adapts automatically |
| Console CRD | `pkg/oinc/console.go` | Fetched from `openshift/api` at the release branch, adapts automatically |
| Ingress operator image | `pkg/addons/ingressoperator.go` | Uses `origin-cluster-ingress-operator:{version}`, adapts automatically |
| Ingress operator manifests | `pkg/addons/ingressoperator.go` | Fetched from `cluster-ingress-operator` at `release-{version}` branch |
| OSSM/Istio version | `pkg/addons/ingressoperator.go` | `istioVersion` const may need updating if the new OSSM ships a different Istio |
| Gateway API CRDs | `pkg/addons/gatewayapi.go` | Version pinned independently, not tied to OCP version |
| Addon versions | `pkg/addons/*.go` | cert-manager, metallb, etc. are pinned independently |

Most things adapt automatically because they derive URLs from the version's `APIBranch` field. The main thing to watch is the `istioVersion` const in the ingress operator addon.
