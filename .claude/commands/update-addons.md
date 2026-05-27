---
description: Check for newer addon versions and update them
allowed-tools: Bash, Read, Edit, Grep, AskUserQuestion
---

Scan for newer versions of all addons and offer to update them.

## 1. Read current versions

Read `pkg/addons/*.go` and extract the current default version for each addon:

| Addon | File | Const |
|-|-|-|
| gateway-api | `pkg/addons/gatewayapi.go` | `defaultGatewayAPIVersion` |
| cert-manager | `pkg/addons/certmanager.go` | `defaultCertManagerVersion` |
| metallb | `pkg/addons/metallb.go` | `defaultMetalLBVersion` |
| istio (sail) | `pkg/addons/istio.go` | `defaultSailVersion` + `defaultIstioVersion` |
| kuadrant | `pkg/addons/kuadrant.go` | `defaultKuadrantVersion` |

## 2. Check for latest releases

For each addon, fetch the latest release:

```bash
# gateway-api
gh release list -R kubernetes-sigs/gateway-api --limit 5

# cert-manager
gh release list -R cert-manager/cert-manager --limit 5

# metallb
gh release list -R metallb/metallb --limit 5

# sail operator
gh release list -R istio-ecosystem/sail-operator --limit 5

# kuadrant (helm)
helm repo add kuadrant https://kuadrant.io/helm-charts/ --force-update 2>/dev/null
helm search repo kuadrant/kuadrant-operator --versions | head -5
```

Filter to stable releases only (ignore pre-releases, RCs, alphas, betas).

## 3. Present findings

Show a table comparing current vs latest for each addon:

```
Addon         Current   Latest    Status
gateway-api   1.2.1     1.3.0     update available
cert-manager  1.17.1    1.17.1    up to date
metallb       0.14.9    0.14.10   update available
istio/sail    1.29.0    1.30.0    update available
kuadrant      1.4.1     1.5.0     update available
```

Use `AskUserQuestion` to let the user pick which addons to update (multiSelect).

If everything is up to date, say so and stop.

## 4. Apply updates (only after user confirms)

For each selected addon, edit the corresponding file to update the version const(s).

Special cases:
- **istio/sail**: has TWO version consts (`defaultSailVersion` and `defaultIstioVersion`). The sail operator version comes from `istio-ecosystem/sail-operator` releases. The istio version should match what that sail version ships -- check the release notes or README.
- **gateway-api**: the version is a GitHub release tag WITHOUT the `v` prefix in the const.
- **cert-manager, metallb**: version is WITHOUT the `v` prefix.
- **kuadrant**: version comes from the helm chart version.

## 5. Verify

- Run: `go build ./...`
- Run: `go vet ./...`

## 6. Summary

List what was updated. Remind the user to test the updated addons on a fresh cluster before committing. Do NOT commit automatically.
