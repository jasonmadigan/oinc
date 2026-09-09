---
description: Check for newer addon versions and update them
allowed-tools: Bash, Read, Edit, Grep, AskUserQuestion
---

Scan for newer versions of all registered addons and update the requested selection. For OKD/MicroShift releases, OCP RCs or Console compatibility, follow `/add-version` in `.claude/commands/add-version.md` instead.

## 1. Read current versions

Read `pkg/addons/*.go` and the registry in `pkg/addons/addon.go`; account for every registered addon, its default version and dependencies. Extract versions and artifact sources from the current code. The current mappings include:

| Addon | File | Const |
|-|-|-|
| gateway-api | `pkg/addons/gatewayapi.go` | `defaultGatewayAPIVersion` |
| cert-manager | `pkg/addons/certmanager.go` | `defaultCertManagerVersion` |
| metallb | `pkg/addons/metallb.go` | `defaultMetalLBVersion` |
| istio (sail) | `pkg/addons/istio.go` | `defaultSailVersion` + `defaultIstioVersion` |
| kuadrant | `pkg/addons/kuadrant.go` | `defaultKuadrantVersion` |
| rhdh | `pkg/addons/rhdh.go` | `defaultRHDHChartVersion` |
| mcp-gateway | `pkg/addons/mcpgateway.go` | `defaultMCPGatewayChartVersion` |

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

For RHDH and MCP Gateway, inspect the chart repository/OCI reference declared in the addon implementation and verify the candidate with `helm show chart`. Compare chart versions separately from application versions.

Default to stable releases; include prereleases only when explicitly requested. Check release notes and dependency compatibility before selecting a candidate.

## 3. Present findings

Show every registered addon with its current default, proposed version, artifact source and compatibility status. Distinguish a verified up-to-date result from a source lookup that failed.

If the user already selected addons or versions, proceed within that scope. Otherwise present the table and use `AskUserQuestion` to select updates.

If everything is up to date, say so and stop.

## 4. Apply the selected updates

For each selected addon, edit the corresponding file to update the version const(s).

Special cases:
- **istio/sail**: has TWO version consts (`defaultSailVersion` and `defaultIstioVersion`). The sail operator version comes from `istio-ecosystem/sail-operator` releases. The istio version should match what that sail version ships -- check the release notes or README.
- **gateway-api**: the version is a GitHub release tag WITHOUT the `v` prefix in the const.
- **cert-manager, metallb**: version is WITHOUT the `v` prefix.
- **kuadrant, rhdh, mcp-gateway**: use the Helm chart version. Inspect version-specific values, bundled resources and compatibility logic in the implementation, including RHDH defaults tied to its pinned chart.

## 5. Verify

- Run `go test ./...`, `go vet ./...` and `make build` for implementation changes.
- Install the selected versions and dependencies through the normal addon flow on an authorized test cluster. Verify readiness and the changed functionality, including Routes or Console integration where applicable.
- Record the cluster distribution/version and any required registry credentials. Temporary manual changes establish feasibility, not a passing normal install: integrate a durable fix and rerun before marking the update verified.
- Update `docs/addons.md` and other affected docs against the final implementation.

## 6. Summary

List exact chart/application versions, what was tested, failures and any outstanding work. Perform authorized testing instead of deferring it as a reminder. Follow existing user authorization for commits, pushes and draft PRs, and create every commit with `git commit --signoff`. Explicitly report any required validation that could not be completed.
