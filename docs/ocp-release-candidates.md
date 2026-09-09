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

Each includes MicroShift, release-info, networking and OLM packages. Both require CRI-O 5.0.x, rather than the 5.1 dependency override used by the current OKD COPR pin. On CentOS Stream 9, the OpenShift 5.0 dependency mirror and `centos-release-nfv-openvswitch` supplied the dependencies in the local rc1 build.

Full OCP release payload metadata was also successfully read with `oc adm release info`, using the configured Red Hat pull secret:

| ARM release image | Verified digest |
|-|-|
| `quay.io/openshift-release-dev/ocp-release:5.0.0-rc.0-aarch64` | `sha256:fb0823afd47b2b4db3cc8151e92f4d25c4aaabe4b544d07c9bc82137d359be22` |
| `quay.io/openshift-release-dev/ocp-release:5.0.0-rc.1-aarch64` | `sha256:cc1fc81246eb447a4697542e4c4e90f20830117238fb83f24a4b577bdbc48b1d` |

The release payloads include native ARM Console images. Those differ from the amd64-only community `quay.io/openshift/origin-console:5.0` image currently used by oinc. Pin a Console component through the matching payload's `console` image reference to test that exact RC.

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

The same Console HTTP/assets/API checks passed against OKD ec.8 with the Stream 9 Console workaround below. The write test created a temporary ConfigMap in the smoke-test namespace, verified its contents, deleted it, and confirmed HTTP 404 afterwards. These checks exercised the HTTP server and authenticated Kubernetes proxy; they were not a browser UI walkthrough.

This is **Red Hat MicroShift with community networking**, not full OCP or the default Red Hat networking configuration. It is useful for testing the RC APIs and components in a container. It does not establish OVN compatibility, persistent-volume behaviour, full OCP cluster-operator behaviour or Red Hat supportability. NetworkPolicy conformance and operator installation were not tested.

## OKD comparison

The pinned `5.0.0-okd-scos.ec.8` image was also booted alongside rc1 using `oinc create --version 5.0 --runtime docker`. It used the existing locally built image; no 5.0 image was published in oinc's GHCR catalogue at test time. The OKD container had no mounts and no Red Hat pull-secret file. Node readiness, all nine base pods, `oinc load-image`, workload startup, Route HTTP and internal service DNS/HTTP passed. Both clusters reported Kubernetes `v1.36.3`.

[OKD ec.9](https://github.com/okd-project/okd/releases/tag/5.0.0-okd-scos.ec.9) was published on 9 September, but the available MicroShift COPR builds still targeted ec.8 when checked. The tested ec.8 RPM was `5.1.0_202609020534_gb19f04dec_5.0.0_okd_scos.ec.8-1.el9`: its MicroShift base metadata reports `5.1.0-0.nightly-arm64-2026-08-20-025249`. This community build combines mainline MicroShift with OKD ec.8 components; it is not an exact counterpart of the Red Hat rc1 build.

The CLI's Console step failed on ARM/OrbStack: `origin-console:5.0` exited with `Fatal glibc error: CPU does not support x86-64-v3`. Selecting `linux/amd64` fixes image selection but does not satisfy that base image's CPU requirement.

[images/Containerfile.console-stream9-experimental](../images/Containerfile.console-stream9-experimental) provides the tested local workaround. It copies the unchanged community Console binary and assets from the pinned 5.0 image onto CentOS Stream 9. It still runs as amd64 under emulation and uses no Red Hat image or pull secret:

```bash
docker build --platform=linux/amd64 \
  -f images/Containerfile.console-stream9-experimental \
  -t oinc-okd-console-test:5.0-stream9 .
```

The failed Console container was manually replaced with this image using the same private Bridge configuration. HTTP 200, JavaScript assets, `branding: okd` and the authenticated node-list API passed. This workaround is not wired into CLI image selection or publishing; recreating the Console through oinc still selects the original image. The complete unmodified `oinc create` flow therefore remains failing for 5.0 on this host, despite the working cluster and manually replaced Console.

The three local test clusters use separate ports:

| Configuration | Container | Console | API | HTTP / HTTPS ingress |
|-|-|-|-|-|
| OKD ec.8 with the Console workaround | `oinc` | 9000 | 6443 | 9080 / 9443 |
| Red Hat MicroShift rc1 with kindnet | `oinc-ocp-rc1` | 19000 | 16443 | 19080 / 19443 |
| Red Hat MicroShift rc0 with kindnet | `oinc-ocp-rc0` | 29000 | 26443 | 29080 / 29443 |

The normal `oinc` kubeconfig context selects OKD. Each Red Hat experiment uses its separate kubeconfig.

## Reproduce the experimental build

[images/Containerfile.ocp-experimental](../images/Containerfile.ocp-experimental) is separate from the normal OKD image and publishing workflow. It accepts `RC=0` or `RC=1` and requires an existing oinc image as its networking source. The tested source was built with:

```bash
docker build -f images/Containerfile -t oinc-local-test:5.0-arm64 \
  --build-arg OCP_VERSION=5.0 \
  --build-arg OKD_VERSION=5.0.0-okd-scos.ec.8 \
  --build-arg COPR_PIN=5.1.0_202609020534_gb19f04dec_5.0.0_okd_scos.ec.8-1.el9 \
  --build-arg DEPS_VERSION=5.1 .
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

Keep the container runtime model. OKD remains the default for CI without a Red Hat pull secret. An eventual Red Hat selection needs separate image tags and release channels, validation and runtime mounting of the pull secret, and the matching OCP Console digest. No Red Hat distribution flag or published RC images are available through the CLI yet. `@latest` remains restricted to stable OKD releases, with prereleases explicitly selected through `@next`.

Full OCP has additional operators and installation requirements. Switching MicroShift's RPM source does not install them. The exact OCP Console can run beside the MicroShift container, but that combination should also be identified accurately.
