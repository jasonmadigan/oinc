---
description: Scan for new MicroShift versions and add them to the catalogue
allowed-tools: Bash, Read, Edit, Glob, Grep, WebFetch, AskUserQuestion
---

Scan for new OCP/MicroShift versions and offer to add them.

RPMs come from one of two sources, set per version in the images.yml matrix:
- `release_tag`: a microshift-io/microshift GitHub release RPM tarball (preferred, immutable)
- `copr_pin`: an exact version-release from the `@microshift-io/microshift-nightly` COPR

The COPR only builds upstream main and prunes old builds, so it is only usable
for the current pre-release version, pinned. Move a version to `release_tag`
as soon as a GitHub release for its OKD tag exists.

## 1. Scan COPR for available versions

Query recent builds to find OKD version strings:

```bash
curl -sL "https://copr.fedorainfracloud.org/api_3/build/list?ownername=@microshift-io&projectname=microshift-nightly&packagename=microshift&limit=50" | \
  python3 -c "
import json, sys
data = json.load(sys.stdin)
seen = set()
for item in data.get('items', []):
    ver = item.get('source_package', {}).get('version', '')
    # extract the okd tag portion (after the git hash)
    parts = ver.split('_g')
    if len(parts) == 2:
        okd = parts[1].split('_', 1)[1].replace('_', '-').rstrip('-1')
        minor = okd.split('.')[0] + '.' + okd.split('.')[1]
        if minor not in seen:
            seen.add(minor)
            print(f'{minor}: {okd}')
"
```

## 2. Compare against current catalogue

Read `pkg/version/version.go` to get currently supported versions. Identify new minor versions not already in the catalogue.

## 3. Pick the RPM source

For each new version:
- Look for a GitHub release whose tag ends in the OKD tag (underscored):
  `gh api repos/microshift-io/microshift/releases --jq '.[].tag_name'`
  If found, use it as `release_tag`, confirm `microshift-rpms-x86_64.tgz` and `microshift-rpms-aarch64.tgz` assets exist,
  and record each asset's sha256 as its `tarball_sha256`:
  `gh api repos/microshift-io/microshift/releases/tags/{tag} --jq '.assets[] | select(.name|startswith("microshift-rpms-")) | "\(.name) \(.digest)"'`
  (release assets are not guaranteed immutable, so the hash pins the content).
- Otherwise pin the COPR build: take the full `version-release` from the step 1 scan
  (e.g. `5.0.0_202605120437_g8e93344a3_4.22.0_okd_scos.ec.16-1.el9`) and use it as `copr_pin`.
  Confirm it exists for both `epel-9-x86_64` and `epel-9-aarch64`.

Also check whether any existing `copr_pin` version now has a GitHub release and offer to switch it to `release_tag`.

## 4. Check upstream resources

For each new version, verify:
- openshift/api branch: `gh api repos/openshift/api/branches/release-{version} --jq '.name'`
- Console image: `docker manifest inspect quay.io/openshift/origin-console:{version}`

## 5. Present findings

Show a summary table with resource status. Use `AskUserQuestion` to let the user pick version(s) to add.

## 6. Apply changes (after user confirms)

**a. Version catalogue** -- edit `pkg/version/version.go`, add entry at end of `catalogue` slice:
```go
{
    Version:       "{minor}",
    MicroShiftTag: "{okd-tag}",
    ConsoleTag:    "{minor}",
    APIBranch:     "release-{minor}",
    Arches:        []string{"amd64", "arm64"},
},
```

**b. CI workflow** -- edit `.github/workflows/images.yml`, add matrix entries for both architectures with `okd_version` and either `release_tag` plus `tarball_sha256` or `copr_pin`.

**c. README + docs/versions.md** -- update supported versions tables.

## 7. Verify

- `go build ./...`
- `go vet ./...`
- `make build && ./bin/oinc version list`

## 8. Summary

Tell the user what was added. Remind them to:
- Run the image build workflow: `gh workflow run images.yml`
- Review and commit the changes
- Do NOT commit automatically
