---
description: Scan for new MicroShift versions and add them to the catalogue
allowed-tools: Bash, Read, Edit, Glob, Grep, WebFetch, AskUserQuestion
---

Scan for new OCP/MicroShift versions and offer to add them.

RPMs come from the COPR nightly repo (`@microshift-io/microshift-nightly`).
The Containerfile consumes these directly -- no tarballs needed.

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

## 3. Check upstream resources

For each new version, verify:
- openshift/api branch: `gh api repos/openshift/api/branches/release-{version} --jq '.name'`
- Console image: `docker manifest inspect quay.io/openshift/origin-console:{version}`
- Dependencies RPM: check that `microshift-io-dependencies-{version}` exists in COPR (the Containerfile pins this by version)

## 4. Present findings

Show a summary table with resource status. Use `AskUserQuestion` to let the user pick version(s) to add.

## 5. Apply changes (after user confirms)

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

**b. CI workflow** -- edit `.github/workflows/images.yml`, add matrix entries for both architectures.

**c. README + docs/versions.md** -- update supported versions tables.

## 6. Verify

- `go build ./...`
- `go vet ./...`
- `make build && ./bin/oinc version list`

## 7. Summary

Tell the user what was added. Remind them to:
- Run the image build workflow: `gh workflow run images.yml`
- Review and commit the changes
- Do NOT commit automatically
