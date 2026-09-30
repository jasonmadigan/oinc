# OCP 5.0 release candidates

Verified on 9 September 2026. OKD's `5.0.0-okd-scos.ec.8` is a separate build and must not be described as OCP rc0 or rc1.

## Available artifacts

Red Hat publishes MicroShift RPM repositories for both candidates on amd64 and arm64:

- [MicroShift rc0 release](https://github.com/openshift/microshift/releases/tag/5.0.0-rc.0-202609010159.p0)
- [MicroShift rc1 release](https://github.com/openshift/microshift/releases/tag/5.0.0-rc.1-202609041239.p0)

The repository pattern is:

```text
https://mirror.openshift.com/pub/openshift-v5/{x86_64,aarch64}/microshift/ocp/5.0.0-rc.{0,1}/el9/os/
```

Each includes MicroShift, release-info, networking and OLM packages. Both require CRI-O 5.0.x, rather than the 5.1 dependency override the OKD COPR builds need. On CentOS Stream 9, the OpenShift 5.0 dependency mirror and `centos-release-nfv-openvswitch` supplied the dependencies in the local rc1 build.

Full OCP release payload metadata was also successfully read with `oc adm release info`, using the configured Red Hat pull secret:

| ARM release image | Verified digest |
|-|-|
| `quay.io/openshift-release-dev/ocp-release:5.0.0-rc.0-aarch64` | `sha256:fb0823afd47b2b4db3cc8151e92f4d25c4aaabe4b544d07c9bc82137d359be22` |
| `quay.io/openshift-release-dev/ocp-release:5.0.0-rc.1-aarch64` | `sha256:cc1fc81246eb447a4697542e4c4e90f20830117238fb83f24a4b577bdbc48b1d` |

The release payloads include native ARM Console images. The OKD Console artifacts inspected for these experiments were amd64-only. Pin a Console component through the matching payload's `console` image reference to test that exact RC.

The rc1 Console digest is `sha256:b1e4f3db8cc8ab852f208fca860384e30a20a52fdb774b02750702a21d78819f` in `quay.io/openshift-release-dev/ocp-v5.0-art-dev`. When launching it standalone, set `BRIDGE_BRANDING=ocp`: the bridge binary defaults to `okd`, even in this Red Hat image. The initial local launch omitted that setting and displayed OKD branding; the corrected launch serves `branding: ocp`. Identify the build by its payload digest, not its configurable logo.

The rc0 Console digest is `sha256:2df648ad12710f0ce6da5e67fd7fd5e244da65a82be41bafe0d22b288f3bfd3d` in the same repository. Both original Red Hat Console images were run directly as native ARM containers and verified against their respective release payloads. Neither needed the community Console's base-image workaround. The rc0 image identifies its original OS as RHEL 9.8.

## Container tests

Official Red Hat MicroShift rc0 and rc1 both booted in privileged ARM Docker containers on OrbStack. No separate VM is needed for this configuration. The pull secret was mounted read-only at runtime and was not baked into either image.

An anonymous manifest request for rc1's CoreDNS image (`sha256:9f4ba5b31da4c3dfceff4a4401680dd298a72b252eb2566457bc58bc605b8872` in `quay.io/openshift-release-dev/ocp-v5.0-art-dev`) returned `unauthorized`. That component successfully ran with the pull-secret mount. Registry credentials are required for this Red Hat configuration.

The unmodified Red Hat binaries reported their respective releases:

```text
MicroShift Version: 5.0.0~rc.1
Base OCP Version: 5.0.0-rc.1
```

Stock OVN networking failed because OrbStack's kernel lacks Open vSwitch:

```text
modprobe: FATAL: Module openvswitch not found in directory /lib/modules/7.0.14-orbstack-00380-ga7e0a2dc9535
```

Setting `network.cniPlugin: none` and copying oinc's kindnet/kube-proxy integration bypassed that dependency. Core and OLM RPMs and their release-info remained Red Hat RC builds; the networking manifests, images and CNI binaries came from the local OKD 5.0 ec.8 image. The replacement systemd unit does not start OVN. The experiment disables LVMS with `storage.driver: none`.

| Check on ARM/OrbStack | rc0 | rc1 |
|-|-|-|
| Exact RC binary and release-info | Passed | Passed |
| Node and all nine base pods ready | Passed | Passed |
| Load a local image into CRI-O and run a workload | Passed | Passed |
| HTTP through an OpenShift Route | Passed | Passed |
| Internal service DNS and HTTP | Passed | Passed |
| Exact native ARM RC Console: HTTP 200 and proxied node-list API | Passed | Passed |
| Console initial JS/CSS assets (six), branding and workload-list API | Passed | Passed |
| Console API ConfigMap create/read/delete, with session CSRF handling | Passed | Passed |

The same Console HTTP/assets/API checks passed against OKD ec.8 with the since-removed Stream 9 Console workaround described below. The write test created a temporary ConfigMap in the smoke-test namespace, verified its contents, deleted it, and confirmed HTTP 404 afterwards. These checks exercised the HTTP server and authenticated Kubernetes proxy; they were not a browser UI walkthrough.

This is **Red Hat MicroShift with community networking**, not full OCP or the default Red Hat networking configuration. It is useful for testing the RC APIs and components in a container. It does not establish OVN compatibility, persistent-volume behaviour, full OCP cluster-operator behaviour or Red Hat supportability. NetworkPolicy conformance and operator installation were not tested.

## OKD comparison

The pinned `5.0.0-okd-scos.ec.8` image was also booted alongside rc1 using `oinc create --version 5.0 --runtime docker`. It used the existing locally built image; no 5.0 image was published in oinc's GHCR catalogue at test time. The OKD container had no mounts and no Red Hat pull-secret file. Node readiness, all nine base pods, `oinc load-image`, workload startup, Route HTTP and internal service DNS/HTTP passed. Both clusters reported Kubernetes `v1.36.3`.

[OKD ec.9](https://github.com/okd-project/okd/releases/tag/5.0.0-okd-scos.ec.9) was published on 9 September, but the available MicroShift COPR builds still targeted ec.8 when checked. The tested ec.8 RPM was `5.1.0_202609020534_gb19f04dec_5.0.0_okd_scos.ec.8-1.el9`: its MicroShift base metadata reports `5.1.0-0.nightly-arm64-2026-08-20-025249`. This community build combines mainline MicroShift with OKD ec.8 components; it is not an exact counterpart of the Red Hat rc1 build.

The CLI's Console step failed on ARM/OrbStack: `origin-console:5.0` exited with `Fatal glibc error: CPU does not support x86-64-v3`. Selecting `linux/amd64` fixes image selection but does not satisfy that base image's CPU requirement. A local workaround copied the unchanged amd64 Console onto CentOS Stream 9; with the Console container replaced by hand, HTTP 200, JavaScript assets, `branding: okd` and the authenticated node-list API passed. It was never wired into the CLI, so the unmodified `oinc create` flow still failed. The native arm64 build under [OKD 5.0 GA](#okd-50-ga) replaced it.

The three test clusters used separate ports:

| Configuration | Container | Console | API | HTTP / HTTPS ingress |
|-|-|-|-|-|
| OKD ec.8 with the Console workaround | `oinc` | 9000 | 6443 | 9080 / 9443 |
| Red Hat MicroShift rc1 with kindnet | `oinc-ocp-rc1` | 19000 | 16443 | 19080 / 19443 |
| Red Hat MicroShift rc0 with kindnet | `oinc-ocp-rc0` | 29000 | 26443 | 29080 / 29443 |

The normal `oinc` kubeconfig context selected OKD. Each Red Hat experiment used its separate kubeconfig.

## OKD 5.0 GA

The following results describe the original 5.1-based experiment. It has been superseded in the draft by the [release-aligned source build](#release-aligned-50-build); these historical test results do not validate the replacement.

Verified on 28 September 2026 on ARM/OrbStack (Apple Silicon, macOS).

[OKD `5.0.0-okd-scos.0`](https://github.com/okd-project/okd/releases/tag/5.0.0-okd-scos.0) was released on 17 September: payload `quay.io/okd/scos-release@sha256:eca01dee0f0690a8149d58f0a8c7e3187f624d583c1b713dc660bd1b28c2dfbc`, Kubernetes 1.36.3. At the time, microshift-io had published no release tarball since January, so the experiment used COPR, which held six builds against this tag. The original catalogue pinned the newest, `5.1.0_202609280620_g9c06ead3b_5.0.0_okd_scos.0-1.el9` (COPR build 11043775), present in the main and `devel/` repodata for both architectures. Like ec.8, it is mainline MicroShift (base `5.1.0-0.nightly-arm64-2026-09-24-215747`, x86_64 `5.1.0-0.nightly-2026-09-24-133658`) with OKD components. The x86_64 component references replaced by the community packaging match the payload; arm64 uses microshift-io's rebuilt OKD images. This excludes the retained LVMS entry and separately supplied kindnet image. It requires `cri-o >= 5.1.0, < 5.2.0`, so that experiment used the 5.1 dependency mirror.

The payload's Console, `quay.io/okd/scos-content@sha256:947e41d0b4af41f65107639d7ed9c49aacd5162b59cc2b79d48ad1d37ad701bf` (openshift/console `e15ec744`, on `release-5.0`), is amd64-only and based on CentOS Stream 10. On this host it fails exactly like `origin-console:5.0`, even for `/bin/sh`. The native arm64 image from `images/Containerfile.console` reports the same commit, version string (`v6.0.6-27128-ge15ec74467`), Go 1.26.7 toolchain, CGO setting and module graph (224 dependencies) as the upstream binary. Its 1802 static assets are byte-identical to upstream's.

| Check (arm64, default `oinc create`) | Result |
|-|-|
| Installed RPMs, release-info and running component digests match the pin | Passed |
| Node Ready and all nine base pods ready | Passed |
| `oinc load-image`, Deployment from the loaded image | Passed |
| Internal service DNS and HTTP | Passed |
| HTTP through an OpenShift Route on host port 9080 | Passed |
| Native arm64 Console running, with no restarts or errors in its log | Passed |
| Console page, six initial JS/CSS assets, `branding: okd` | Passed |
| Console proxy reads: nodes, Deployments, Pods | Passed |
| ConfigMap create/read/delete through the Console proxy; POST without the CSRF token returns 403 | Passed |
| Browser: Deployment details page renders live data | Passed |
| `kuadrant@latest,mcp-gateway` with the developer portal, MetalLB pool and default Gateway; the e2e `instances` assertions | Passed |

The table above used locally built images; GHCR held no 5.0 image at the time. The node reports Kubernetes `v1.36.4` and CRI-O `1.36.5`, newer than the payload's 1.36.3, because MicroShift is built from main and CRI-O comes from the 5.1 mirror. Browser console errors were limited to 404s for APIs MicroShift does not serve (`config.openshift.io`, `project.openshift.io`, `helm.openshift.io`, `image.openshift.io`, `metal3.io`) and a 500 from the OLM package-manifest check for `lightspeed-operator`, as there is no marketplace catalogue.

New namespaces are labelled `pod-security.kubernetes.io/enforce: restricted`. Deployment pods were admitted after SCC mutation; a pod created directly by the admin user without a restricted `securityContext` was rejected.

The amd64 Console and x86_64 component digests are anonymously pullable from quay.io. CRI-O pulled the arm64 components from `ghcr.io/microshift-io/okd` without credentials.

On amd64 this host could only build the MicroShift image under emulation, installing `cri-o-5.1.0` and passing the release-info guard; the payload Console cannot run under emulation here.

The first Console publish failed on the arm64 runner with `Exec format error`. podman 4.9, as installed on `ubuntu-24.04-arm`, applies a stage's `FROM --platform` to every later stage, so `--platform=linux/amd64` on the upstream stage made the build and final stages pull amd64 CentOS images. BuildKit does not do this, so the local build had passed. The Containerfile no longer sets a platform, since the digest names a single amd64 manifest, and the workflow now checks the built image's architecture before pushing.

The images were then published from `9fb5eff`, all anonymously pullable:

| Image | Digest |
|-|-|
| `ghcr.io/jasonmadigan/oinc:5.0.0-okd-scos.0-amd64` | `sha256:0f12726371660350ef317071a6fe9519d90d938c385489ffd5692d639aef922b` |
| `ghcr.io/jasonmadigan/oinc:5.0.0-okd-scos.0-arm64` | `sha256:b0c8d05f08723072f32fdbe6c324126da4367ed226eabe7560eef62682775df3` |
| `ghcr.io/jasonmadigan/oinc-console:5.0.0-okd-scos.0-arm64` | `sha256:a6eb0d6790bb372d3b3dc8bed40d064cf53b9269f46cfcf7f7dc5e017c28e50b` |

With the local images removed, a default `oinc create` on this host pulled both arm64 images. The Console, proxy and CSRF checks and the addon assertions above passed again. On amd64, the e2e smoke legs (docker and podman) booted 5.0 with the payload Console, loaded an image and ran a pod.

## Release-aligned 5.0 build

The replacement uses the inputs in `images/5.0.json`: MicroShift `07ab806795491479323b6084c4375770e3c98266` (5.0 rc2 source, Kubernetes 1.36.3), pinned microshift-io packaging, the OKD 5.0 payloads by digest and CRI-O from the 5.0 mirror. Both architectures use the same source and build timestamp. The old COPR build is not an input. This remains community MicroShift with kindnet, and is not an official Red Hat GA binary or full OCP.

The image revision is `5.0.0-okd-scos.0-oinc.1-<arch>`. Original tags remain historical artifacts; the new CLI excludes them from channel discovery. Both replacement architectures were published on 29 September 2026 in [build 36587185381](https://github.com/jasonmadigan/oinc/actions/runs/36587185381). Native amd64 Docker and Podman smoke tests then passed in [E2E run 36586769472](https://github.com/jasonmadigan/oinc/actions/runs/36586769472).

Verified locally on 28 September 2026 on native arm64 through Docker/OrbStack, using the tracked source builder and an unmodified `oinc create --version 5.0`:

| Check | Result |
|-|-|
| Embedded release lock matches `images/5.0.json` | Passed |
| MicroShift RPM and base release are 5.0; node runs Kubernetes 1.36.3 | Passed |
| CRI-O RPM is from the 5.0 stream | Passed; package `5.0.0-202609212354.p2.gee69985.assembly.stream.el9`, upstream runtime 1.36.6 |
| Node Ready and all nine platform pods ready, including OLM | Passed |
| Load a local image and roll out a Deployment | Passed |
| Internal service DNS/HTTP and external HTTP through an OpenShift Route | Passed |
| Console HTML, six initial JS/CSS assets and proxied node/Pod/Deployment reads | Passed |
| Console proxy ConfigMap create/read/delete; missing CSRF header rejected with 403 | Passed |

The local image ID was `sha256:c81cf19f12de59dcac2a1804e5bd36f3d46de93e0385b73d65df03d3c1a8da65`. This run did not repeat the browser walkthrough or addon suite. CRI-O's upstream patch version differs from Kubernetes within the 1.36 line; the dependency mirror remains mutable. Build rejection checks also confirmed that neither the old 5.1 dependency mirror nor a 5.1 COPR pin can be used for a 5.0 image.

## Reproduce the experimental build

[images/Containerfile.ocp-experimental](../images/Containerfile.ocp-experimental) is separate from the normal OKD image and publishing workflow. It accepts `RC=0` or `RC=1` and requires an existing oinc image as its networking source. The historical source was built with the command below before the 5.x minor guards were added. The current Containerfile intentionally rejects this 5.1/5.0 mix; use a retained historical image to repeat that experiment:

```bash
docker build -f images/Containerfile -t oinc-local-test:5.0-arm64 \
  --build-arg OCP_VERSION=5.0 \
  --build-arg OKD_VERSION=5.0.0-okd-scos.ec.8 \
  --build-arg COPR_PIN=5.1.0_202609020534_gb19f04dec_5.0.0_okd_scos.ec.8-1.el9 \
  --build-arg DEPS_VERSION=5.1 images/
```

That COPR pin may be pruned upstream; retain the local image for repeat tests. These commands were tested on ARM. amd64 RC RPMs exist, but the container recipe has not been boot-tested on amd64.

```bash
docker build -f images/Containerfile.ocp-experimental \
  --build-arg OKD_NETWORKING_IMAGE=oinc-local-test:5.0-arm64 \
  --build-arg RC=1 -t oinc-ocp-experimental:5.0.0-rc.1 .

# Set this to an existing Red Hat pull-secret JSON file.
OINC_RC_PULL_SECRET="/absolute/path/to/pull-secret.json"
docker run -d --name oinc-ocp-rc1 --privileged \
  --hostname 127.0.0.1.nip.io --tmpfs /var/lib/containers \
  -p 127.0.0.1:16443:6443 \
  -p 127.0.0.1:19080:80 -p 127.0.0.1:19443:443 \
  --mount "type=bind,source=${OINC_RC_PULL_SECRET},target=/etc/crio/openshift-pull-secret,readonly" \
  oinc-ocp-experimental:5.0.0-rc.1
```

After startup, copy the hostname-specific kubeconfig and change its server port from 6443 to 16443, retaining `127.0.0.1.nip.io` as the hostname for certificate verification. Keep the kubeconfig private:

```bash
umask 077
docker cp oinc-ocp-rc1:/var/lib/microshift/resources/kubeadmin/127.0.0.1.nip.io/kubeconfig ./kubeconfig-ocp-rc1
# Edit the server URL to https://127.0.0.1.nip.io:16443.
kubectl --kubeconfig ./kubeconfig-ocp-rc1 get nodes,pods -A
```

This manual prototype does not register a cluster with oinc, configure the Console or alter the normal kubeconfig. The container's nested image storage is temporary; remove and recreate the experiment for a fresh cluster. Use different container names and host ports to test rc0 alongside rc1.

## Integration direction

Keep the container runtime model. OKD remains the default for CI without a Red Hat pull secret. An eventual Red Hat selection needs separate image tags and release channels, validation and runtime mounting of the pull secret, and the matching OCP Console digest. The historical 5.0 RCs remain outside the CLI. The separate [4.23 preview](ocp-4.23.md) adds an explicit `ocp-4.23` selector and stored pull-secret support. `@latest` remains restricted to stable OKD releases, with prereleases explicitly selected through `@next`.

Full OCP has additional operators and installation requirements. Switching MicroShift's RPM source does not install them. The exact OCP Console can run beside the MicroShift container, but that combination should also be identified accurately.
