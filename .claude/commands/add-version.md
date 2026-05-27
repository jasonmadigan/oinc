---
description: Scan for new MicroShift versions and add them to the catalogue
allowed-tools: Bash, Read, Edit, Glob, Grep, WebFetch, AskUserQuestion
---

Scan for new OCP/MicroShift versions and offer to add them.

## 1. Scan for available releases

Fetch recent releases from the MicroShift repo:

```
gh release list -R microshift-io/microshift --limit 30
```

## 2. Compare against current catalogue

Read `pkg/version/version.go` to get the currently supported versions. Compare with the releases found above.

For each release not already in the catalogue, extract:
- OCP minor version (e.g. 4.22)
- Full release tag (e.g. `4.22.0_g1a2b3c4d5_4.22.0_okd_scos.1`)
- OKD version (the portion after the git hash, with `_` replaced by `-`)
- Available architectures (check if both `microshift-rpms-x86_64.tgz` and `microshift-rpms-aarch64.tgz` exist in the release assets)

## 3. Check upstream resources

For each new version found, verify:
- openshift/api branch exists: `https://api.github.com/repos/openshift/api/branches/release-{version}` (200 = exists)
- Console image exists: `docker manifest inspect quay.io/openshift/origin-console:{version}`
- Ingress operator image exists: `docker manifest inspect quay.io/openshift/origin-cluster-ingress-operator:{version}`

## 4. Present findings

Show a summary table of new versions found, with a status for each upstream resource (available/missing). Use `AskUserQuestion` to let the user pick which version(s) to add. If a version is missing critical resources (no openshift/api branch, no console image), note that clearly.

If no new versions are found, say so and stop.

## 5. Apply changes (only after user confirms)

For each version the user accepts:

**a. Version catalogue** -- edit `pkg/version/version.go`, add new entry at the end of the `catalogue` slice:
```go
{
    Version:       "{minor}",
    MicroShiftTag: "{okd-version}",
    ConsoleTag:    "{minor}",
    APIBranch:     "release-{minor}",
    Arches:        []string{"amd64", "arm64"},
},
```

**b. CI workflow** -- edit `.github/workflows/images.yml`, add matrix entries for both architectures.

**c. README** -- update the supported versions table.

## 6. Verify

- Run: `go build ./...`
- Run: `go vet ./...`
- Build and check: `make build && ./bin/oinc version list`

## 7. Summary

Tell the user what was added. Remind them to:
- Run the image build workflow: `gh workflow run images.yml -f version={version}`
- Review and commit the changes
- Do NOT commit automatically
