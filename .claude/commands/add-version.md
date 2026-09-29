---
description: Update OKD release pins, add supported minors, or investigate Red Hat MicroShift RCs with artifact and runtime verification
allowed-tools: Bash, Read, Edit, Write, Glob, Grep, WebFetch, AskUserQuestion
---

Update the requested release or scan for candidates. For addon updates, use `/update-addons`. Read `docs/versions.md`, `docs/images.md`, the version resolver and image workflow before changing pins. Read `docs/ocp-release-candidates.md` when handling source/payload mismatches, 5.0, Red Hat RCs, ARM Console failures or experimental images; it separates historical experiments from replacement-build validation.

## 1. Establish the current configuration

Read `pkg/version/version.go`, `pkg/version/remote.go`, `pkg/oinc/console.go`, `images/Containerfile` and `.github/workflows/images.yml`. Inspect the working tree and running containers. Identify the requested distribution, exact release, architecture and whether this is a new minor or a newer build within an existing minor.

Keep these identities separate:

- The normal catalogue selects **OKD MicroShift**. An OCP-style minor identifies compatibility settings; it does not select a Red Hat payload.
- Red Hat MicroShift RCs use separate RPMs, release-info and authenticated component images. MicroShift remains a subset of OCP, including when using Red Hat packages.
- A community RPM can combine mainline MicroShift with an OKD component release. Record the source commit/release, RPM version, `microshift version`, release-info base, component digests and node Kubernetes version. A GA payload tag establishes neither a matching MicroShift minor nor GA status for its source.
- Console branding is configurable. Verify the running image digest against the intended artifact. Set `BRIDGE_BRANDING=ocp` for Red Hat branding when launching standalone; the default branding can still say OKD.

Keep oinc container-based. Preserve OKD's path without a Red Hat pull secret. Mount credentials only at runtime for the Red Hat path, keep credential files private, and redact tokens from command output. A successful authenticated pull does not prove anonymous access works; test anonymous access separately when making that claim.

## 2. Discover releases and usable artifacts

Check primary upstream sources and published oinc images:

```bash
make build
gh release list -R okd-project/okd --limit 20
gh release list -R microshift-io/microshift --limit 20
./bin/oinc version list --remote
```

For Red Hat candidates, inspect `openshift/microshift` releases and the exact OCP payload with `oc adm release info`. Use the configured pull secret without printing it. Resolve Console images from the matching payload and inspect their architectures.

For each OKD candidate, choose exactly one RPM source:

- **Release tarball:** a matching `microshift-io/microshift` release, with both `microshift-rpms-x86_64.tgz` and `microshift-rpms-aarch64.tgz`. Verify and record each asset's SHA-256. Unless the upstream release is immutable, assets can be replaced; the digest pins the content.
- **COPR:** an exact version-release from `@microshift-io/microshift-nightly`. Inspect package metadata and per-chroot build status for both epel-9 architectures. Check main and `devel/` repodata; recent packages may appear only in `devel/`. Preserve the complete RPM pin and parse the OKD tag without trimming version digits. Verify the source minor independently: this project builds main, and replacing component images does not change the binary's release. COPR prunes builds, so prefer verified release tarballs when available.
- **Pinned release source:** when available RPMs target a different minor, use the source-build path in `docs/images.md#release-aligned-source-builds`. Inspect the selected commit's vendored Kubernetes version and RPM requirements. Pin the MicroShift and packaging commits, Go version, build timestamp and both payload digests in the release lock. Retain the tested community packaging/networking changes. Label RC-derived source explicitly, even with a GA payload.

Derive `deps_version` from actual RPM requirements, especially CRI-O, and verify the dependency mirror for each architecture. For a release-aligned build, require the source/base and CRI-O RPM minors to match the target; choose compatible source rather than moving the dependency mirror to accommodate mainline RPMs. Record `crio --version` separately from its RPM version: their numbering differs, and upstream patch skew within one Kubernetes minor is distinct from release-minor skew. Base images and dependency mirrors remain mutable unless separately pinned.

Check the `openshift/api` branch and Console image as well. A release announcement, successful manifest lookup or existing local image is not proof that the complete oinc image can be built, published and booted.

Present a table with distribution, exact tag, RPM source/pin, dependency minor, architectures, Console reference and publication/test status. For a named update, proceed within the user's existing authorization. For discovery without a selected target, present the evidence before asking which candidates to update.

## 3. Apply the appropriate update

### New OKD build within a supported minor

Use the existing compatibility settings. For versions without a source lock, the image workflow accepts `version`, `okd_version`, `copr_pin` and `deps_version` together for a custom COPR build; verify their current validation rules in the workflow. An oinc image published under a new standard payload/architecture tag is discoverable without a CLI catalogue edit; publishing an upstream payload alone is insufficient. Update a catalogue pin only when changing the offline minor default is part of the request. Tarball builds use the matrix source and architecture-specific checksums.

### Source builds and replacement images

For source-locked versions, keep the lock, matching workflow fields and catalogue consistent; COPR overrides are rejected. Follow the documented builder with an RPM output directory empty apart from `.gitkeep`, an `images/` final-image context and `LOCAL_RPMS=1`. Verify the packaging adaptations still match the pinned recipes, including both architecture payload lookups. Exercise the native CI builder as well as local Docker: Podman and BuildKit can handle multi-stage platform selection differently.

When correcting an already-published build for the same payload, assign a new image revision through `ImageTag`. Existing local images bypass pulls, so overwriting the old tag cannot reliably deliver the fix. Test minor and full-payload resolution, exclusion of superseded tags from remote discovery, and partial publication: an old architecture tag must not satisfy a missing replacement. Keep old clusters recognisable. Older CLIs do not learn replacement mappings from registry tags; document recreation and the CLI update needed to select the correction.

### New OKD minor

Add the catalogue entry, workflow input choice and matrix entries for every verified architecture. Update the supported-version table and relevant image/version docs. Enable only architectures whose required artifacts and runtime configuration have been verified.

### Red Hat RC

Keep distribution identity, image references and credentials separate from OKD. The experimental recipes are evidence, not implemented CLI distribution support. Read the current source before claiming that a distribution flag or published RC image exists. Record any networking or storage substitutions explicitly.

### Stable and prerelease channels

Keep the offline default stable. `@latest` excludes EC/RC payload tags, `4@latest` stays within stable 4.x, and `@next` opts into prereleases. An explicit prerelease payload pin does not make it a stable default. Channel classification follows the payload tag; disclose RC-derived MicroShift source separately. Preserve numeric ordering, architecture filtering and resolving a switch before deleting the existing cluster.

## 4. Verify the normal runtime path

Build the CLI and candidate image. Inventory stopped containers as well as running ones, and set `KUBECONFIG` to a single separate file for tests. The normal CLI fixes the container names to `oinc`/`oinc-console` and API port to 6443; custom names/API ports require manual experiments and do not validate the normal flow. If preserving stopped containers by temporarily renaming them, restore their names and stopped state afterward. Start the test with those fixed names free: `create` starts an existing cluster container without checking its version, then replaces the Console. Remove only task-owned test resources. Record whether the candidate was locally built or actually pulled from the registry: oinc skips pulling cached images.

For each configuration being claimed as supported, verify:

1. Installed RPM, release-info, running component digests and node version match the intended build. Account for packaging substitutions separately: kindnet has its own pinned image, and the retained LVMS release-info entry is not replaced from OKD. The LVMS package is not installed by this Containerfile. For source builds, compare `/usr/share/oinc/release.json` with the tracked lock; this checks recorded inputs, not every binary or dependency. Confirm the Console's actual architecture and digest.
2. The node and all expected base pods are ready, including DNS, ingress, service CA, networking and OLM when included. Check these explicitly: the CLI readiness namespace list omits kindnet and OLM. Check that expected workloads exist; an empty namespace is not readiness.
3. `oinc load-image` loads a local image, the workload starts, internal service DNS/HTTP works, and an OpenShift Route responds through the mapped host port.
4. The actual Console process stays running. Check its logs: an amd64 pull can succeed while execution fails on ARM emulation because of a glibc/CPU requirement.
5. The Console serves its page and initial JS/CSS assets, has the expected branding, and proxies node/workload reads. Create, read and delete a temporary ConfigMap through the Console proxy using the normal session/CSRF protocol, then verify deletion.
6. If UI behaviour is claimed, exercise it in a browser. Label HTTP/API checks accurately when no browser walkthrough was performed.

Test each release separately. Success on rc1 does not establish rc0 behaviour. Prefer the original compatible image, including a native architecture build when available, over a local base-image substitution.

**A workaround is an unresolved integration issue.** A manually retagged image, replaced Console container, altered base image or out-of-band config can establish feasibility, but cannot turn a failed normal create flow into a passing release test. Implement the durable fix in the build/runtime/publishing path and rerun the normal flow before claiming support. Keep temporary experiments and their limitations explicit while that work remains open.

For code changes run `go test ./...`, `go vet ./...` and `make build`; validate image-workflow changes with `actionlint`. When changing release-composition guards, attempt builds with the mismatched RPM pin and dependency mirror and confirm each fails for the expected reason. Check selectors against the intended published images, including stable-only rejection and missing architectures. A local-only build does not satisfy remote channel verification.

## 5. Publish and report within the authorized scope

Verify docs against the final code and tests. When publishing is authorized, run the workflow from the branch containing the intended changes, inspect every relevant architecture's result, and verify the resulting registry tags/digests. Publish all required replacement architectures and pass native e2e checks before merging or releasing a CLI that selects them. Failed or skipped publication is not a successful release update.

Report exact versions and digests, tested architectures, secret requirements, passed/failed checks, remaining integration work and how to reach any clusters left running. Distinguish built locally, published, normal-flow tested and manually tested. Keep dated findings in `docs/ocp-release-candidates.md` instead of copying volatile versions into this command.

Follow existing user authorization for commits, pushes and draft PRs; every commit must use `git commit --signoff`. If publication or other external action is not authorized, finish the reviewable changes and report the precise remaining action.
